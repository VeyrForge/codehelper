// Package connections stores per-project database and SSH connection profiles so
// an agent (and project_context) can see what external systems a project talks
// to — multiple of each are supported. It deliberately holds only NON-SECRET
// metadata plus a reference to where a secret lives (env:VAR); a raw password is
// never accepted or written. Profiles live beside the index (paths.RepoIndexDir),
// so they follow CODEHELPER_INDEX_HOME and never touch the repo, mirroring
// internal/projcfg.
//
// This is the configuration + discovery layer. Actually executing SQL or SSH
// commands is a separate, security-gated capability (see docs/MCP_EXTENSION_IDEAS.md);
// nothing here opens a socket.
package connections

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/verify"
)

const filename = "connections.json"

// SSHHost is a named SSH endpoint. IdentityFile is a path to a private key on the
// local machine (not the key material). JumpHost names another SSHHost to proxy
// through (ProxyJump), enabling multi-hop access to internal DB hosts.
type SSHHost struct {
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	User         string `json:"user,omitempty"`
	Port         int    `json:"port,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
	JumpHost     string `json:"jump_host,omitempty"`
	// Disabled toggles the profile off without deleting it. Absent = enabled, so
	// profiles written before this field keep working. The agent is told only
	// whether a host is enabled, never the identity file's contents.
	Disabled bool `json:"disabled,omitempty"`
	// AllowedCommands is the allowlist of command basenames this host may run once
	// remote execution is wired (e.g. ["journalctl","systemctl","ls"]). Empty means
	// "nothing is pre-approved" — the safe default. This list IS shown to the agent
	// (it bounds what it may attempt); it can't destroy anything by itself.
	AllowedCommands []string `json:"allowed_commands,omitempty"`
	// Recipes are named remote command templates the agent may invoke via remote_exec.
	Recipes []Recipe `json:"recipes,omitempty"`
}

// DBConn is a named database connection profile. PasswordRef is a reference to
// where the secret lives (e.g. "env:STAGING_PG_PASSWORD"), never the secret
// itself. SSHTunnel names an SSHHost to tunnel the connection through.
type DBConn struct {
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Database string `json:"database,omitempty"`
	User     string `json:"user,omitempty"`
	// PasswordRef points at where the secret lives: "env:VAR" (in the environment)
	// or "secret" (in the encrypted, out-of-repo secret store — internal/secrets).
	// It is never the secret itself and is never shown to the agent.
	PasswordRef string `json:"password_ref,omitempty"`
	SSHTunnel   string `json:"ssh_tunnel,omitempty"`
	// ReadOnly marks the connection read-only; the future query path must refuse
	// DDL/DML on it. Shown to the agent as a capability bound.
	ReadOnly bool `json:"read_only,omitempty"`
	// Disabled toggles the profile off without deleting it (absent = enabled).
	Disabled bool `json:"disabled,omitempty"`
}

// SecretRef is the sentinel PasswordRef value meaning "the password lives in the
// encrypted secret store (internal/secrets), keyed by this profile's name".
const SecretRef = "secret"

// UsesSecretStore reports whether the DB's password comes from the encrypted store.
func (d DBConn) UsesSecretStore() bool {
	return strings.EqualFold(strings.TrimSpace(d.PasswordRef), SecretRef)
}

// Enabled reports whether the profile is active (the inverse of Disabled).
func (d DBConn) Enabled() bool { return !d.Disabled }

// Enabled reports whether the SSH host is active.
func (h SSHHost) Enabled() bool { return !h.Disabled }

// Config is the per-project set of connection profiles.
type Config struct {
	Databases  []DBConn       `json:"databases,omitempty"`
	SSHHosts   []SSHHost      `json:"ssh_hosts,omitempty"`
	LogSources []LogSource    `json:"log_sources,omitempty"`
	Aliases    []CommandAlias `json:"aliases,omitempty"`
	Policy     Policy         `json:"policy,omitempty"`
}

// canonicalDrivers maps accepted driver spellings to a canonical name. "basically
// all" the common engines are accepted; aliases fold to one name so a profile is
// stored consistently regardless of how the user spelled it.
var canonicalDrivers = map[string]string{
	"postgres": "postgres", "postgresql": "postgres", "pg": "postgres",
	"mysql": "mysql", "mariadb": "mysql",
	"sqlite": "sqlite", "sqlite3": "sqlite",
	"mssql": "mssql", "sqlserver": "mssql",
	"oracle": "oracle", "oracledb": "oracle",
	"cockroach": "cockroach", "cockroachdb": "cockroach",
	"clickhouse": "clickhouse",
	"mongodb":    "mongodb", "mongo": "mongodb",
	"redis": "redis",
}

// SupportedDrivers lists the canonical driver names, sorted, for help/errors.
func SupportedDrivers() []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(canonicalDrivers))
	for _, v := range canonicalDrivers {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Path is the per-project connections file (beside the index).
func Path(repoRoot string) string {
	return filepath.Join(paths.RepoIndexDir(repoRoot), filename)
}

// Load reads the connections file, returning an empty Config when absent so an
// unconfigured project behaves as if the feature is off. A malformed file is a
// hard error (not a silent reset) so a typo can't quietly drop profiles.
func Load(repoRoot string) (Config, error) {
	var c Config
	b, err := os.ReadFile(Path(repoRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("connections: malformed %s: %w", Path(repoRoot), err)
	}
	c.Policy = NormalizePolicy(c.Policy)
	return c, nil
}

// Save writes the connections file atomically, creating the index dir if needed.
func Save(repoRoot string, c Config) error {
	p := Path(repoRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Empty reports whether no profiles, log sources, or policy are configured.
func (c Config) Empty() bool {
	return len(c.Databases) == 0 && len(c.SSHHosts) == 0 &&
		len(c.LogSources) == 0 && len(c.Aliases) == 0 && !c.Policy.IsConfigured()
}

// normalizeDriver canonicalizes a driver spelling or errors listing what's supported.
func normalizeDriver(driver string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(driver))
	if d == "" {
		return "", fmt.Errorf("driver is required (one of: %s)", strings.Join(SupportedDrivers(), ", "))
	}
	if canon, ok := canonicalDrivers[d]; ok {
		return canon, nil
	}
	return "", fmt.Errorf("unsupported driver %q; supported: %s", driver, strings.Join(SupportedDrivers(), ", "))
}

// validatePasswordRef enforces that a secret is a reference, never inline. Only a
// recognized scheme (env:) is accepted; a bare value is rejected so a raw password
// can't be committed to the profile store by accident.
func validatePasswordRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "env:") && strings.TrimSpace(strings.TrimPrefix(ref, "env:")) != "" {
		return nil
	}
	if strings.EqualFold(ref, SecretRef) {
		return nil // password lives in the encrypted secret store
	}
	return fmt.Errorf("password_ref must be %q, %q, or a reference like %q — never an inline secret", SecretRef, "env:VAR", "env:MY_DB_PASSWORD")
}

// SetEnabled toggles a profile (db or ssh) on/off by name, reporting whether a
// profile matched.
func (c *Config) SetEnabled(name string, enabled bool) bool {
	name = strings.TrimSpace(name)
	found := false
	for i := range c.Databases {
		if strings.EqualFold(c.Databases[i].Name, name) {
			c.Databases[i].Disabled = !enabled
			found = true
		}
	}
	for i := range c.SSHHosts {
		if strings.EqualFold(c.SSHHosts[i].Name, name) {
			c.SSHHosts[i].Disabled = !enabled
			found = true
		}
	}
	return found
}

// AddDatabase validates and upserts a DB profile by name (case-insensitive).
func (c *Config) AddDatabase(db DBConn) error {
	db.Name = strings.TrimSpace(db.Name)
	if db.Name == "" {
		return fmt.Errorf("database name is required")
	}
	canon, err := normalizeDriver(db.Driver)
	if err != nil {
		return err
	}
	db.Driver = canon
	if err := validatePasswordRef(db.PasswordRef); err != nil {
		return err
	}
	if db.SSHTunnel != "" && c.FindSSH(db.SSHTunnel) == nil {
		return fmt.Errorf("ssh_tunnel %q is not a configured ssh host; add it first", db.SSHTunnel)
	}
	out := c.Databases[:0]
	for _, x := range c.Databases {
		if !strings.EqualFold(x.Name, db.Name) {
			out = append(out, x)
		}
	}
	c.Databases = append(out, db)
	sort.Slice(c.Databases, func(i, j int) bool { return c.Databases[i].Name < c.Databases[j].Name })
	return nil
}

// AddSSHHost validates and upserts an SSH host by name (case-insensitive).
func (c *Config) AddSSHHost(h SSHHost) error {
	h.Name = strings.TrimSpace(h.Name)
	if h.Name == "" {
		return fmt.Errorf("ssh host name is required")
	}
	if strings.TrimSpace(h.Hostname) == "" {
		return fmt.Errorf("ssh host %q needs a hostname", h.Name)
	}
	if err := verify.ValidateSSHAllowlist(h.AllowedCommands); err != nil {
		return err
	}
	out := c.SSHHosts[:0]
	for _, x := range c.SSHHosts {
		if !strings.EqualFold(x.Name, h.Name) {
			out = append(out, x)
		}
	}
	c.SSHHosts = append(out, h)
	sort.Slice(c.SSHHosts, func(i, j int) bool { return c.SSHHosts[i].Name < c.SSHHosts[j].Name })
	return nil
}

// Remove deletes a DB or SSH profile by name (either kind), reporting whether
// anything matched.
func (c *Config) Remove(name string) bool {
	name = strings.TrimSpace(name)
	removed := false
	db := c.Databases[:0]
	for _, x := range c.Databases {
		if strings.EqualFold(x.Name, name) {
			removed = true
			continue
		}
		db = append(db, x)
	}
	c.Databases = db
	sh := c.SSHHosts[:0]
	for _, x := range c.SSHHosts {
		if strings.EqualFold(x.Name, name) {
			removed = true
			continue
		}
		sh = append(sh, x)
	}
	c.SSHHosts = sh
	return removed
}

// FindSSH returns the SSH host with the given name, or nil.
func (c *Config) FindSSH(name string) *SSHHost {
	for i := range c.SSHHosts {
		if strings.EqualFold(c.SSHHosts[i].Name, name) {
			return &c.SSHHosts[i]
		}
	}
	return nil
}
