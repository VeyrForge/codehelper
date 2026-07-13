// Package projcfg holds the small per-project MCP runtime config: whether
// codehelper exposes its tools to the calling agent, and how much usage
// telemetry to record. It lives next to the index (paths.RepoIndexDir) so it
// follows CODEHELPER_INDEX_HOME and never touches the repo, and the MCP server
// re-reads it (mtime-cached) on tool calls so a toggle takes effect without a
// server restart.
//
// The point of the ToolsEnabled switch is an honest A/B comparison: with tools
// off, the server keeps serving the project and recording telemetry, but the
// agent is steered back to its built-in Read/Grep — so you can measure how the
// same workflow does with and without codehelper's context.
package projcfg

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// Track levels control whether each tool call is persisted to the usage log.
// Recording captures capped request/response previews plus exact byte and token
// counts — enough for an A/B comparison without storing whole responses.
const (
	TrackOff     = "off"     // record nothing
	TrackSummary = "summary" // capped Args/Snippet previews + byte/token counts (default)
)

// Config is the per-project MCP runtime config. The zero value is NOT the
// default — always obtain one via Load, which applies Default() when no file
// exists and merges a partial file over it.
type Config struct {
	// ToolsEnabled gates whether codehelper's MCP tools actually run for this
	// project. When false the server still serves the project (so telemetry keeps
	// flowing) but each call is shadow-executed: the real result is recorded for
	// comparison and the agent receives a redirect notice so it falls back to its
	// built-in tools. This is the "baseline" arm of an A/B comparison.
	ToolsEnabled bool `json:"tools_enabled"`
	// Track sets how much of each call is persisted: off | summary | full.
	Track string `json:"track"`
	// MinimalTools trims the advertised tool surface (tools/list) down to the
	// high-frequency main tools for this project, so a client that pays per
	// tool-definition token — Cursor's ~40-tool soft cap, VS Code's per-request
	// budget — spends fewer tokens before the task starts and picks tools more
	// reliably. It does NOT disable anything: hidden tools stay fully callable by
	// name; they just don't appear in the list. The global CODEHELPER_MINIMAL_TOOLS
	// env var forces this on for every project regardless of this flag.
	MinimalTools bool `json:"minimal_tools"`
	// VerifyCwd is the repo-relative directory where lint/build/test commands run
	// (e.g. "rust" when Cargo.toml lives in rust/ not the repo root).
	VerifyCwd string `json:"verify_cwd,omitempty"`
	// VerifyBuild/VerifyTest/VerifyLint override auto-detected verify commands.
	VerifyBuild string `json:"verify_build,omitempty"`
	VerifyTest  string `json:"verify_test,omitempty"`
	VerifyLint  string `json:"verify_lint,omitempty"`
}

// Default is the config used when no file exists: tools on, summary telemetry.
func Default() Config { return Config{ToolsEnabled: true, Track: TrackSummary} }

// Path is the per-project config file (lives with the index, keyed by repo root).
func Path(repoRoot string) string {
	return filepath.Join(paths.RepoIndexDir(repoRoot), "mcp-config.json")
}

// Load reads the project config, returning Default() when the file is absent so
// an un-configured project behaves exactly as before this feature existed. A
// malformed file is an error (not a silent revert) so a typo can't quietly
// disable tracking. Fields omitted in the file keep their Default() value, so a
// file may set only the keys it cares about.
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
	cfg.normalize()
	return cfg, nil
}

// Save writes the project config, creating the index dir if needed.
func Save(repoRoot string, cfg Config) error {
	cfg.normalize()
	p := Path(repoRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(p, data, 0o644)
}

// normalize coerces an unrecognized Track value back to the default rather than
// failing the whole config — an unknown level should not silently behave as off.
func (c *Config) normalize() {
	switch c.Track {
	case TrackOff, TrackSummary:
	default:
		c.Track = TrackSummary
	}
}

// Recording reports whether any telemetry should be written for this project.
func (c Config) Recording() bool { return c.Track != TrackOff }
