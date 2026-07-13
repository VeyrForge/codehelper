// Package web (browser_config) stores user-level browser tool settings in
// ~/.codehelper/browser.json. Action step previews are opt-in: disabled by
// default so multi-step flows do not multiply screenshot tokens unless the
// user explicitly enables them.
package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// BrowserConfig is the persisted browser settings (~/.codehelper/browser.json).
type BrowserConfig struct {
	// ActionPreviews allows the browser MCP tool to return a screenshot after
	// each interaction step when preview_actions=true on a call. Off by default.
	ActionPreviews bool `json:"action_previews,omitempty"`
}

// BrowserConfigPath is ~/.codehelper/browser.json.
func BrowserConfigPath() (string, error) {
	dir, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "browser.json"), nil
}

// LoadBrowserConfig reads the stored config; a missing file is not an error.
func LoadBrowserConfig() (BrowserConfig, error) {
	p, err := BrowserConfigPath()
	if err != nil {
		return BrowserConfig{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return BrowserConfig{}, nil
		}
		return BrowserConfig{}, err
	}
	var c BrowserConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return BrowserConfig{}, err
	}
	return c, nil
}

// SaveBrowserConfig writes the config.
func SaveBrowserConfig(c BrowserConfig) error {
	p, err := BrowserConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// EffectiveBrowserConfig merges environment overrides over the stored config.
// CODEHELPER_BROWSER_ACTION_PREVIEWS=1|true|on enables action previews.
func EffectiveBrowserConfig() BrowserConfig {
	c, _ := LoadBrowserConfig()
	if v := strings.TrimSpace(os.Getenv("CODEHELPER_BROWSER_ACTION_PREVIEWS")); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "on", "yes", "enable", "enabled":
			c.ActionPreviews = true
		case "0", "false", "off", "no", "disable", "disabled":
			c.ActionPreviews = false
		}
	}
	return c
}

// PreviewActionsAllowed reports whether step-by-step action screenshots may run.
func PreviewActionsAllowed(requested bool) bool {
	return requested && EffectiveBrowserConfig().ActionPreviews
}
