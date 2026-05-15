package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/cmd/internal/fileutil"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/progress"
	"github.com/ollama/ollama/types/model"
	"golang.org/x/term"
)

// Pi implements Runner and Editor for Pi (Pi Coding Agent) integration
type Pi struct{}

const (
	piNpmPackage       = "@earendil-works/pi-coding-agent"
	piLegacyNpmPackage = "@mariozechner/pi-coding-agent"
	piWebSearchSource  = "npm:@ollama/pi-web-search"
	piWebSearchPkg     = "@ollama/pi-web-search"
)

func (p *Pi) String() string { return "Pi" }

func (p *Pi) Run(model string, args []string) error {
	status := newPiLaunchStatus()
	defer status.StopAndClear()

	status.Set("Preparing Pi...")
	if err := ensureNpmInstalled(); err != nil {
		return err
	}

	status.Set("Checking Pi installation...")
	bin, err := ensurePiInstalledWithStatus(status)
	if err != nil {
		return err
	}

	ensurePiWebSearchPackageWithStatus(bin, status)

	status.Set("Launching Pi...")
	status.StopAndClear()

	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

type piLaunchStatus struct {
	progress *progress.Progress
	spinner  *progress.Spinner
}

func newPiLaunchStatus() *piLaunchStatus {
	return &piLaunchStatus{}
}

func (s *piLaunchStatus) Set(message string) {
	if s == nil || !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}
	if s.progress == nil {
		s.progress = progress.NewProgress(os.Stderr)
		s.spinner = progress.NewSpinner("")
		s.progress.Add("pi", s.spinner)
		return
	}
}

func (s *piLaunchStatus) StopAndClear() {
	if s == nil || s.progress == nil {
		return
	}
	s.progress.StopAndClear()
	s.progress = nil
	s.spinner = nil
}

func ensureNpmInstalled() error {
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("npm (Node.js) is required to launch pi\n\nInstall it first:\n  https://nodejs.org/\n\nThen re-run:\n  ollama launch pi")
	}
	return nil
}

func ensurePiInstalled() (string, error) {
	return ensurePiInstalledWithStatus(nil)
}

func ensurePiInstalledWithStatus(status *piLaunchStatus) (string, error) {
	if _, err := exec.LookPath("pi"); err == nil {
		pkg, pkgErr := installedPiPackage()
		if pkgErr == nil && pkg == piLegacyNpmPackage {
			status.StopAndClear()
			ok, err := ConfirmPrompt("Switch Pi to the official package? Your settings and extensions will be kept.")
			if err != nil {
				return "", err
			}
			if !ok {
				return "", fmt.Errorf("pi migration cancelled\n\nTo migrate later, re-run:\n  ollama launch pi\n\nOr migrate manually:\n  npm uninstall -g %s\n  npm install -g %s", piLegacyNpmPackage, piNpmPackage)
			}

			status.Set("Updating Pi...")
			if err := uninstallLegacyPiPackage(); err != nil {
				return "", err
			}
			if err := installPiPackage(); err != nil {
				return "", err
			}
		}
		return "pi", nil
	}

	if _, err := exec.LookPath("npm"); err != nil {
		return "", fmt.Errorf("pi is not installed and required dependencies are missing\n\nInstall the following first:\n  npm (Node.js): https://nodejs.org/\n\nThen re-run:\n  ollama launch pi")
	}

	status.StopAndClear()
	ok, err := ConfirmPrompt("Install Pi with npm?")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("pi installation cancelled")
	}

	status.Set("Installing Pi...")
	if err := installPiPackage(); err != nil {
		return "", err
	}

	if _, err := exec.LookPath("pi"); err != nil {
		return "", fmt.Errorf("pi was installed but the binary was not found on PATH\n\nYou may need to restart your shell")
	}

	return "pi", nil
}

func installPiPackage() error {
	if err := runQuietCommand("npm", "install", "-g", piNpmPackage+"@latest"); err != nil {
		return fmt.Errorf("failed to install pi: %w", err)
	}
	return nil
}

func uninstallLegacyPiPackage() error {
	if err := runQuietCommand("npm", "uninstall", "-g", piLegacyNpmPackage); err != nil {
		return fmt.Errorf("failed to remove legacy pi package: %w", err)
	}
	return nil
}

func runQuietCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}

func installedPiPackage() (string, error) {
	if _, err := exec.LookPath("npm"); err != nil {
		return "", err
	}

	installed, err := npmPackageInstalled(piNpmPackage)
	if err != nil {
		return "", err
	}
	if installed {
		return piNpmPackage, nil
	}

	installed, err = npmPackageInstalled(piLegacyNpmPackage)
	if err != nil {
		return "", err
	}
	if installed {
		return piLegacyNpmPackage, nil
	}

	return "", nil
}

func npmPackageInstalled(pkg string) (bool, error) {
	cmd := exec.Command("npm", "ls", "-g", pkg, "--depth=0", "--json")
	out, err := cmd.Output()

	var payload struct {
		Dependencies map[string]json.RawMessage `json:"dependencies"`
	}

	if parseErr := json.Unmarshal(out, &payload); parseErr == nil {
		_, ok := payload.Dependencies[pkg]
		if ok {
			return true, nil
		}
		return false, nil
	}

	if err == nil {
		return false, nil
	}

	if exitErr, ok := err.(*exec.ExitError); ok {
		msg := strings.TrimSpace(string(exitErr.Stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		if msg == "" {
			return false, err
		}
		return false, fmt.Errorf("%w: %s", err, msg)
	}

	return false, err
}

func ensurePiWebSearchPackageWithStatus(bin string, status *piLaunchStatus) {
	if !shouldManagePiWebSearch() {
		return
	}

	status.Set("Checking Pi web search package...")

	installed, err := piPackageInstalled(bin, piWebSearchSource)
	if err != nil {
		return
	}

	if !installed {
		status.Set("Installing " + piWebSearchPkg + "...")
		if err := runQuietCommand(bin, "install", piWebSearchSource); err != nil {
			return
		}
		return
	}

	status.Set("Updating " + piWebSearchPkg + "...")
	if err := runQuietCommand(bin, "update", piWebSearchSource); err != nil {
		return
	}
}

func shouldManagePiWebSearch() bool {
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return true
	}

	disabled, known := cloudStatusDisabled(context.Background(), client)
	if known && disabled {
		return false
	}
	return true
}

func piPackageInstalled(bin, source string) (bool, error) {
	cmd := exec.Command(bin, "list")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return false, err
		}
		return false, fmt.Errorf("%w: %s", err, msg)
	}

	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, source) {
			return true, nil
		}
	}

	return false, nil
}

func (p *Pi) Paths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	var paths []string
	modelsPath := filepath.Join(home, ".pi", "agent", "models.json")
	if _, err := os.Stat(modelsPath); err == nil {
		paths = append(paths, modelsPath)
	}
	settingsPath := filepath.Join(home, ".pi", "agent", "settings.json")
	if _, err := os.Stat(settingsPath); err == nil {
		paths = append(paths, settingsPath)
	}
	return paths
}

func (p *Pi) Edit(models []string) error {
	if len(models) == 0 {
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	configPath := filepath.Join(home, ".pi", "agent", "models.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}

	config := make(map[string]any)
	if data, err := os.ReadFile(configPath); err == nil {
		_ = json.Unmarshal(data, &config)
	}

	providers, ok := config["providers"].(map[string]any)
	if !ok {
		providers = make(map[string]any)
	}

	ollama, ok := providers["ollama"].(map[string]any)
	if !ok {
		ollama = map[string]any{
			"baseUrl": envconfig.Host().String() + "/v1",
			"api":     "openai-completions",
			"apiKey":  "ollama",
		}
	}

	existingModels, ok := ollama["models"].([]any)
	if !ok {
		existingModels = make([]any, 0)
	}

	// Build set of selected models to track which need to be added
	selectedSet := make(map[string]bool, len(models))
	for _, m := range models {
		selectedSet[m] = true
	}

	// Build new models list:
	// 1. Keep user-managed models (no _launch marker) - untouched
	// 2. Keep ollama-managed models (_launch marker) that are still selected,
	//    except stale cloud entries that should be rebuilt below
	// 3. Add new ollama-managed models
	var newModels []any
	for _, m := range existingModels {
		if modelObj, ok := m.(map[string]any); ok {
			if id, ok := modelObj["id"].(string); ok {
				// User-managed model (no _launch marker) - always preserve
				if !isPiOllamaModel(modelObj) {
					newModels = append(newModels, m)
				} else if selectedSet[id] {
					// Rebuild stale managed cloud entries so createConfig refreshes
					// the whole entry instead of patching it in place.
					if !hasContextWindow(modelObj) {
						if _, ok := lookupCloudModelLimit(id); ok {
							continue
						}
					}
					newModels = append(newModels, m)
					selectedSet[id] = false
				}
			}
		}
	}

	// Add newly selected models that weren't already in the list
	client := api.NewClient(envconfig.Host(), http.DefaultClient)
	ctx := context.Background()
	for _, model := range models {
		if selectedSet[model] {
			newModels = append(newModels, createConfig(ctx, client, model))
		}
	}

	ollama["models"] = newModels
	providers["ollama"] = ollama
	config["providers"] = providers

	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := fileutil.WriteWithBackup(configPath, configData, "pi"); err != nil {
		return err
	}

	// Update settings.json with default provider and model
	settingsPath := filepath.Join(home, ".pi", "agent", "settings.json")
	settings := make(map[string]any)
	if data, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(data, &settings)
	}

	settings["defaultProvider"] = "ollama"
	settings["defaultModel"] = models[0]

	settingsData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteWithBackup(settingsPath, settingsData, "pi")
}

func (p *Pi) Models() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	configPath := filepath.Join(home, ".pi", "agent", "models.json")
	config, err := fileutil.ReadJSON(configPath)
	if err != nil {
		return nil
	}

	providers, _ := config["providers"].(map[string]any)
	ollama, _ := providers["ollama"].(map[string]any)
	models, _ := ollama["models"].([]any)

	var result []string
	for _, m := range models {
		if modelObj, ok := m.(map[string]any); ok {
			if id, ok := modelObj["id"].(string); ok {
				result = append(result, id)
			}
		}
	}
	slices.Sort(result)
	return result
}

// isPiOllamaModel reports whether a model config entry is managed by ollama launch
func isPiOllamaModel(cfg map[string]any) bool {
	if v, ok := cfg["_launch"].(bool); ok && v {
		return true
	}
	return false
}

func hasContextWindow(cfg map[string]any) bool {
	switch v := cfg["contextWindow"].(type) {
	case float64:
		return v > 0
	case int:
		return v > 0
	case int64:
		return v > 0
	default:
		return false
	}
}

// createConfig builds Pi model config with capability detection
func createConfig(ctx context.Context, client *api.Client, modelID string) map[string]any {
	cfg := map[string]any{
		"id":      modelID,
		"_launch": true,
	}
	if l, ok := lookupCloudModelLimit(modelID); ok {
		cfg["contextWindow"] = l.Context
	}

	applyCloudContextFallback := func() {
		if l, ok := lookupCloudModelLimit(modelID); ok {
			cfg["contextWindow"] = l.Context
		}
	}

	resp, err := client.Show(ctx, &api.ShowRequest{Model: modelID})
	if err != nil {
		applyCloudContextFallback()
		return cfg
	}

	// Set input types based on vision capability
	if slices.Contains(resp.Capabilities, model.CapabilityVision) {
		cfg["input"] = []string{"text", "image"}
	} else {
		cfg["input"] = []string{"text"}
	}

	// Set reasoning based on thinking capability
	if slices.Contains(resp.Capabilities, model.CapabilityThinking) {
		cfg["reasoning"] = true
	}

	// Extract context window from ModelInfo. For known cloud models, the
	// pre-filled shared limit remains unless the server provides a positive value.
	hasContextWindow := false
	for key, val := range resp.ModelInfo {
		if strings.HasSuffix(key, ".context_length") {
			if ctxLen, ok := val.(float64); ok && ctxLen > 0 {
				cfg["contextWindow"] = int(ctxLen)
				hasContextWindow = true
			}
			break
		}
	}
	if !hasContextWindow {
		applyCloudContextFallback()
	}

	return cfg
}
