package connections

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/verify"
)

const (
	AgentTrustNone           = "none"
	AgentTrustAllowlistEdits = "allowlist_edits"
)

// Policy holds per-project security settings for connections and local verify.
// Only the user may change this via the CLI — never via MCP.
type Policy struct {
	// AgentTrust controls whether an agent may propose allowlist/profile edits.
	// "none" (default): CLI/user only. "allowlist_edits": agent may propose; user approves.
	AgentTrust string `json:"agent_trust,omitempty"`
	// VerifyAllowlist caps which command basenames verify may run locally.
	// When set, the LLM cannot widen beyond this list (intersection only).
	VerifyAllowlist []string `json:"verify_allowlist,omitempty"`
	// AllowGit permits git in local verify when explicitly enabled (goal.md §19).
	AllowGit bool `json:"allow_git,omitempty"`
	// GitHub optional read-only CI integration (token via env: ref only).
	GitHub *GitHubPolicy `json:"github,omitempty"`
}

// GitHubPolicy configures read-only GitHub/CI access for ci_status.
type GitHubPolicy struct {
	// Repo is owner/name override; empty = detect from git remote.
	Repo string `json:"repo,omitempty"`
	// TokenRef is e.g. env:GITHUB_TOKEN — never the token itself.
	TokenRef string `json:"token_ref,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// LogSource is a named log file the agent may tail once remote/local log reading
// is wired. Paths are metadata only — no content is read at config time.
type LogSource struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // nginx, apache, wordpress, app, custom
	Path    string `json:"path"`
	SSHHost string `json:"ssh_host,omitempty"` // optional host alias for remote paths
	// Disabled toggles the source off without deleting it (absent = enabled).
	Disabled bool `json:"disabled,omitempty"`
}

var logKinds = map[string]struct{}{
	"nginx": {}, "apache": {}, "wordpress": {}, "app": {}, "custom": {},
}

// IsConfigured reports whether the user set non-default policy fields.
func (p Policy) IsConfigured() bool {
	p = NormalizePolicy(p)
	return p.AllowGit || len(p.VerifyAllowlist) > 0 || p.AgentTrust == AgentTrustAllowlistEdits ||
		(p.GitHub != nil && !p.GitHub.Disabled && (p.GitHub.Repo != "" || p.GitHub.TokenRef != ""))
}

// NormalizePolicy canonicalizes policy fields and defaults AgentTrust to none.
func NormalizePolicy(p Policy) Policy {
	p.AgentTrust = strings.ToLower(strings.TrimSpace(p.AgentTrust))
	if p.AgentTrust == "" {
		p.AgentTrust = AgentTrustNone
	}
	var allow []string
	seen := map[string]bool{}
	for _, a := range p.VerifyAllowlist {
		a = strings.TrimSpace(a)
		if a == "" || seen[strings.ToLower(a)] {
			continue
		}
		seen[strings.ToLower(a)] = true
		allow = append(allow, a)
	}
	p.VerifyAllowlist = allow
	return p
}

// SetPolicy replaces the project policy after validation.
func (c *Config) SetPolicy(p Policy) error {
	p = NormalizePolicy(p)
	switch p.AgentTrust {
	case AgentTrustNone, AgentTrustAllowlistEdits:
	default:
		return fmt.Errorf("agent_trust must be %q or %q", AgentTrustNone, AgentTrustAllowlistEdits)
	}
	for _, a := range p.VerifyAllowlist {
		if blocked, reason := verify.CommandBlocked([]string{a}); blocked {
			return fmt.Errorf("verify_allowlist: %s", reason)
		}
	}
	if p.GitHub != nil {
		if err := validateGitHubPolicy(*p.GitHub); err != nil {
			return err
		}
	}
	c.Policy = p
	return nil
}

func validateGitHubPolicy(g GitHubPolicy) error {
	if strings.TrimSpace(g.TokenRef) != "" {
		if !strings.HasPrefix(strings.TrimSpace(g.TokenRef), "env:") ||
			strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(g.TokenRef), "env:")) == "" {
			return fmt.Errorf("github token_ref must be env:VAR — never inline")
		}
	}
	if r := strings.TrimSpace(g.Repo); r != "" {
		parts := strings.Split(r, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("github repo must be owner/name")
		}
	}
	return nil
}

// EffectiveVerifyAllowlist returns the allowlist verify should use. When the
// project sets VerifyAllowlist, the request list can only narrow it — never widen.
func (p Policy) EffectiveVerifyAllowlist(requested []string) []string {
	p = NormalizePolicy(p)
	if len(p.VerifyAllowlist) == 0 {
		return requested
	}
	if len(requested) == 0 {
		return append([]string(nil), p.VerifyAllowlist...)
	}
	var out []string
	project := map[string]bool{}
	for _, a := range p.VerifyAllowlist {
		project[strings.ToLower(strings.TrimSpace(a))] = true
	}
	for _, r := range requested {
		r = strings.TrimSpace(r)
		if r != "" && project[strings.ToLower(r)] {
			out = append(out, r)
		}
	}
	return out
}

// VerifyBlockPolicy returns the command block policy for local verify runs.
func VerifyBlockPolicy(repoRoot string) verify.BlockPolicy {
	cfg, err := Load(repoRoot)
	if err != nil {
		return verify.BlockPolicy{}
	}
	return verify.BlockPolicy{AllowGit: cfg.Policy.AllowGit}
}

// ResolveVerifyAllowlist loads project policy and merges with a request allowlist.
func ResolveVerifyAllowlist(repoRoot string, requested []string) []string {
	cfg, err := Load(repoRoot)
	if err != nil {
		return requested
	}
	return cfg.Policy.EffectiveVerifyAllowlist(requested)
}

// AddLogSource validates and upserts a log source by name.
func (c *Config) AddLogSource(src LogSource) error {
	src.Name = strings.TrimSpace(src.Name)
	if src.Name == "" {
		return fmt.Errorf("log source name is required")
	}
	src.Kind = strings.ToLower(strings.TrimSpace(src.Kind))
	if _, ok := logKinds[src.Kind]; !ok {
		return fmt.Errorf("log kind must be one of: nginx, apache, wordpress, app, custom")
	}
	src.Path = strings.TrimSpace(src.Path)
	if src.Path == "" {
		return fmt.Errorf("log source %q needs a path", src.Name)
	}
	if src.SSHHost != "" && c.FindSSH(src.SSHHost) == nil {
		return fmt.Errorf("ssh_host %q is not a configured ssh host; add it first", src.SSHHost)
	}
	out := c.LogSources[:0]
	for _, x := range c.LogSources {
		if !strings.EqualFold(x.Name, src.Name) {
			out = append(out, x)
		}
	}
	c.LogSources = append(out, src)
	sortLogSources(c)
	return nil
}

// RemoveLogSource deletes a log source by name.
func (c *Config) RemoveLogSource(name string) bool {
	name = strings.TrimSpace(name)
	removed := false
	out := c.LogSources[:0]
	for _, x := range c.LogSources {
		if strings.EqualFold(x.Name, name) {
			removed = true
			continue
		}
		out = append(out, x)
	}
	c.LogSources = out
	return removed
}

func sortLogSources(c *Config) {
	sort.Slice(c.LogSources, func(i, j int) bool {
		return c.LogSources[i].Name < c.LogSources[j].Name
	})
}

// DetectLogSources scans the repo for common log paths (WordPress, nginx, Apache,
// app logs). Returns suggestions only — nothing is persisted until the user adds them.
func DetectLogSources(repoRoot string) []LogSource {
	repoRoot = filepath.Clean(repoRoot)
	var out []LogSource
	add := func(name, kind, rel string) {
		p := filepath.Join(repoRoot, rel)
		if _, err := os.Stat(p); err != nil {
			return
		}
		out = append(out, LogSource{Name: name, Kind: kind, Path: rel})
	}

	// WordPress debug log (local).
	if fileExists(filepath.Join(repoRoot, "wp-config.php")) ||
		fileExists(filepath.Join(repoRoot, "wp-load.php")) {
		add("wordpress-debug", "wordpress", "wp-content/debug.log")
	}

	// Common repo-local app logs.
	for _, c := range []struct{ name, kind, rel string }{
		{"app-error", "app", "storage/logs/laravel.log"},
		{"app-log", "app", "logs/error.log"},
		{"app-log", "app", "log/error.log"},
		{"app-log", "app", "var/log/dev.log"},
	} {
		add(c.name, c.kind, c.rel)
	}

	// Template paths (remote or absolute) — included when docker-compose hints exist.
	if hasNginxHint(repoRoot) {
		out = append(out, LogSource{
			Name: "nginx-error", Kind: "nginx", Path: "/var/log/nginx/error.log",
		})
	}
	if hasApacheHint(repoRoot) {
		out = append(out, LogSource{
			Name: "apache-error", Kind: "apache", Path: "/var/log/apache2/error.log",
		})
	}
	return dedupeLogSources(out)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func hasNginxHint(repoRoot string) bool {
	for _, rel := range []string{"docker-compose.yml", "docker-compose.yaml", "nginx.conf", "conf/nginx.conf"} {
		if fileExists(filepath.Join(repoRoot, rel)) {
			return true
		}
	}
	return false
}

func hasApacheHint(repoRoot string) bool {
	for _, rel := range []string{"docker-compose.yml", "docker-compose.yaml", ".htaccess", "httpd.conf", "apache2.conf"} {
		if fileExists(filepath.Join(repoRoot, rel)) {
			return true
		}
	}
	return false
}

func dedupeLogSources(in []LogSource) []LogSource {
	seen := map[string]bool{}
	var out []LogSource
	for _, s := range in {
		key := s.Kind + "\x00" + s.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}
