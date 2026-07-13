// Package green manages the optional local "green engine" — the user's local
// embedding + LLM servers that power codehelper's two opt-in LLM features:
// semantic rerank (CODEHELPER_EMBED_URL, query-time, in the MCP server) and
// index-time enrichment (CODEHELPER_ENRICH_URL, in analyze/watch processes).
//
// Design:
//   - One config file (~/.codehelper/green.json) describes each server: how to
//     launch it, its port, health path, and which codehelper env var its URL
//     feeds. User-specific launch paths live HERE, never in the binary.
//   - ExportEnv() is cheap and network-free: every codehelper entrypoint calls it
//     so analyze/watch/query/mcp all point at the green engine when enabled. When
//     disabled (or no config), it sets nothing — codehelper runs its pure
//     deterministic path (BM25 + trigram, no rerank, no enrichment).
//   - The long-lived MCP server additionally SUPERVISES the processes (spawn if
//     down, watchdog-respawn if killed) via manage.go, so "it always works while
//     mcp works". Servers are spawned detached so a single instance survives MCP
//     restarts (no 7B model reload on every reconnect).
//
// Everything is best-effort: a missing config, a failed spawn, or a dead server
// degrades to deterministic — it never blocks a codehelper command.
package green

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// Server describes one managed local server.
type Server struct {
	Name         string            `json:"name"`                        // "embed" | "llm" (free-form label)
	Cmd          string            `json:"cmd"`                         // executable path
	Args         []string          `json:"args"`                        // args; the literal "{{port}}" is replaced with Port
	Port         int               `json:"port"`                        // localhost port it listens on
	HealthPath   string            `json:"health_path"`                 // GET path that returns 200 when ready (default /v1/models)
	URLEnv       string            `json:"url_env"`                     // codehelper env var fed its base URL (e.g. CODEHELPER_EMBED_URL)
	Env          map[string]string `json:"env,omitempty"`               // extra env to export alongside (e.g. the model name var)
	StartTimeout int               `json:"start_timeout_sec,omitempty"` // readiness wait (default 120)
	External     bool              `json:"external,omitempty"`          // true = supervised elsewhere (e.g. systemd); codehelper USES it but never spawns/stops it

}

// Config is the whole green-engine description.
type Config struct {
	Enabled bool     `json:"enabled"`
	Servers []Server `json:"servers"`
}

// ConfigPath is ~/.codehelper/green.json.
func ConfigPath() (string, error) {
	dir, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "green.json"), nil
}

// Load reads the config. ok=false (no error) when the file does not exist, so
// "green never configured" is a normal, silent state.
func Load() (Config, bool, error) {
	path, err := ConfigPath()
	if err != nil {
		return Config{}, false, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, true, nil
}

// Save writes the config (creating ~/.codehelper if needed).
func Save(c Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// BaseURL is the localhost base URL codehelper points at (no trailing path).
func (s Server) BaseURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.Port)
}

// healthURL is the readiness probe endpoint.
func (s Server) healthURL() string {
	p := s.HealthPath
	if p == "" {
		p = "/v1/models"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return s.BaseURL() + p
}

// renderedArgs substitutes {{port}} so one config line works on any port.
func (s Server) renderedArgs() []string {
	out := make([]string, len(s.Args))
	for i, a := range s.Args {
		out[i] = strings.ReplaceAll(a, "{{port}}", strconv.Itoa(s.Port))
	}
	return out
}

// ExportEnv points codehelper at the green engine for THIS process when enabled:
// it sets each server's URLEnv to its base URL plus any extra Env. Cheap and
// network-free — safe to call from every entrypoint. Returns the list of env
// vars set (for logging). When disabled or unconfigured, sets nothing so the
// caller runs deterministic.
//
// It never OVERRIDES an env var the user already exported by hand, so an explicit
// CODEHELPER_EMBED_URL in the shell still wins.
func ExportEnv(c Config) []string {
	if !c.Enabled {
		return nil
	}
	var set []string
	for _, s := range c.Servers {
		if s.URLEnv != "" && os.Getenv(s.URLEnv) == "" {
			_ = os.Setenv(s.URLEnv, s.BaseURL())
			set = append(set, s.URLEnv)
		}
		for k, v := range s.Env {
			if os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}
	}
	return set
}

// LoadAndExport is the one call entrypoints make: load config (silent if absent)
// and export env when enabled. Errors are returned for optional logging but are
// non-fatal by contract.
func LoadAndExport() ([]string, error) {
	c, ok, err := Load()
	if err != nil || !ok {
		return nil, err
	}
	return ExportEnv(c), nil
}
