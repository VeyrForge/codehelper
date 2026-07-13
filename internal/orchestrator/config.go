// Package orchestrator runs guided local investigation workflows with tool
// trace memory, feedback, and rerun support. It is opt-in per project.
package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// Config is the per-project orchestration runtime config.
type Config struct {
	Enabled bool `json:"enabled"`
}

// Default returns the config when no file exists (disabled).
func Default() Config { return Config{Enabled: false} }

// Path is the per-project orchestration config file.
func Path(repoRoot string) string {
	return filepath.Join(paths.RepoIndexDir(repoRoot), "orchestration.json")
}

// Load reads orchestration config; missing file means disabled.
func Load(repoRoot string) (Config, error) {
	data, err := os.ReadFile(Path(repoRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Default(), err
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	return cfg, nil
}

// Save persists orchestration config.
func Save(repoRoot string, cfg Config) error {
	p := Path(repoRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o644)
}

// Enabled reports whether local orchestration is on for repoRoot.
func Enabled(repoRoot string) bool {
	cfg, err := Load(repoRoot)
	if err != nil {
		return false
	}
	return cfg.Enabled
}

// SetEnabled toggles orchestration and persists the config.
func SetEnabled(repoRoot string, on bool) error {
	cfg, err := Load(repoRoot)
	if err != nil {
		return err
	}
	cfg.Enabled = on
	return Save(repoRoot, cfg)
}

// ParseOnOff parses common on/off spellings.
func ParseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes", "enable", "enabled":
		return true, nil
	case "0", "false", "off", "no", "disable", "disabled":
		return false, nil
	default:
		return false, errInvalidOnOff
	}
}

var errInvalidOnOff = &onOffError{}

type onOffError struct{}

func (e *onOffError) Error() string {
	return "expected on or off"
}
