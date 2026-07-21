package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/secrets"
	"github.com/spf13/cobra"
)

// connectionsCmd manages the per-project database and SSH connection profiles
// that project_context reports. Profiles are metadata only — secrets are passed
// by reference (env:VAR), never stored — and multiple of each kind are allowed.
func connectionsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "connections",
		Short: "Manage per-project database and SSH connection profiles",
		Long: `Per-project connection profiles, stored beside the index (never in the repo).

Multiple databases and SSH hosts are supported. Passwords are NEVER stored in the
profile or the repo. Two ways to supply a secret:
  --password-ref env:MY_DB_PASSWORD   the value lives in an env var
  set-secret --name <profile>          store it AES-256-GCM encrypted, out of repo

The agent (via project_context) sees only whether a profile is enabled, read-only,
has a secret, allowed_commands, log sources, and policy caps — never secrets.

Policy (CLI only — never via MCP):
  policy set --verify-allowlist go,npm,make   cap what verify may run locally
  policy set --allow-git                      permit git in verify (off by default)
  policy set --agent-trust none|allowlist_edits
  policy set --github-repo owner/name --github-token-ref env:GITHUB_TOKEN

Log sources (metadata for future log tailing — no reads at config time):
  add-log --name wp --kind wordpress --path wp-content/debug.log
  detect-logs                                 suggest paths from project layout
  rm-log --name wp

SSH recipes and command aliases (for remote_exec / run_alias MCP tools):
  add-recipe --host prod --name tail-log --argv tail,-n,{lines},{path} --params lines,path
  add-alias --name test --argv go,test,./...
  rm-alias --name test

Supported DB drivers: ` + fmt.Sprint(connections.SupportedDrivers()),
	}
	c.AddCommand(
		connectionsListCmd(), connectionsAddDBCmd(), connectionsAddSSHCmd(),
		connectionsAddSiteCmd(),
		connectionsRemoveCmd(), connectionsSetSecretCmd(),
		connectionsEnableCmd("enable", true), connectionsEnableCmd("disable", false),
		connectionsPolicyCmd(), connectionsAddLogCmd(), connectionsDetectLogsCmd(),
		connectionsRemoveLogCmd(), connectionsAddRecipeCmd(), connectionsAddAliasCmd(),
		connectionsRemoveAliasCmd(),
	)
	return c
}

func connectionsRepoRoot(args []string) (string, error) {
	_, repoRoot, err := indexer.ResolveIndexPaths(argPath(args), "")
	if err != nil {
		return "", fmt.Errorf("connection profiles require a git repository: %w", err)
	}
	return repoRoot, nil
}

func printConnections(repoRoot string, cfg connections.Config) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"path":        connections.Path(repoRoot),
		"repo_root":   repoRoot,
		"databases":   cfg.Databases,
		"ssh_hosts":   cfg.SSHHosts,
		"websites":    cfg.WebSites,
		"log_sources": cfg.LogSources,
		"aliases":     cfg.Aliases,
		"policy":      cfg.Policy,
	})
}

func connectionsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [path]",
		Short: "List configured connection profiles",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repoRoot, err := connectionsRepoRoot(args)
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
}

func connectionsAddDBCmd() *cobra.Command {
	var db connections.DBConn
	var path string
	c := &cobra.Command{
		Use:   "add-db",
		Short: "Add or update a database connection profile",
		RunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			if err := cfg.AddDatabase(db); err != nil {
				return err
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	f := c.Flags()
	f.StringVar(&path, "path", ".", "repo path")
	f.StringVar(&db.Name, "name", "", "profile name (required)")
	f.StringVar(&db.Driver, "driver", "", "db driver: "+fmt.Sprint(connections.SupportedDrivers()))
	f.StringVar(&db.Host, "host", "", "host")
	f.IntVar(&db.Port, "port", 0, "port")
	f.StringVar(&db.Database, "database", "", "database name")
	f.StringVar(&db.User, "user", "", "user")
	f.StringVar(&db.PasswordRef, "password-ref", "", "secret reference, e.g. env:MY_DB_PASSWORD (never an inline secret)")
	f.StringVar(&db.SSHTunnel, "ssh-tunnel", "", "name of a configured ssh host to tunnel through")
	f.BoolVar(&db.ReadOnly, "read-only", false, "mark this connection read-only")
	return c
}

func connectionsAddSSHCmd() *cobra.Command {
	var h connections.SSHHost
	var path, allowed string
	c := &cobra.Command{
		Use:   "add-ssh",
		Short: "Add or update an SSH host profile",
		RunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			h.AllowedCommands = splitCommaArg(allowed)
			if err := cfg.AddSSHHost(h); err != nil {
				return err
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	f := c.Flags()
	f.StringVar(&path, "path", ".", "repo path")
	f.StringVar(&h.Name, "name", "", "profile name (required)")
	f.StringVar(&h.Hostname, "host", "", "hostname (required)")
	f.StringVar(&h.User, "user", "", "ssh user")
	f.IntVar(&h.Port, "port", 0, "ssh port")
	f.StringVar(&h.IdentityFile, "identity", "", "path to the private key")
	f.StringVar(&h.JumpHost, "jump", "", "name of another ssh host to proxy through")
	f.StringVar(&allowed, "allowed-commands", "", "comma-separated allowlist of command basenames (e.g. journalctl,systemctl)")
	return c
}

func connectionsAddSiteCmd() *cobra.Command {
	var s connections.WebSite
	var path string
	c := &cobra.Command{
		Use:   "add-site",
		Short: "Add or update an HTTP/WordPress site profile for browser recipes",
		Long: `Stores NON-SECRET site metadata (base URL, user, kind) for browser recipes
like wp_login, laravel_login, django_admin, drupal_login, magento_login, spa_hydrate.
Passwords are never accepted inline — use --password-ref env:VAR or pipe into
connections set-secret --name <site>.

Kinds: wordpress | laravel | django | drupal | magento | spa | generic

Examples:
  codehelper connections add-site --name local-wp --url http://wp-test.local --kind wordpress --user admin --password-ref secret
  codehelper connections add-site --name local-laravel --url http://127.0.0.1:8000 --kind laravel --user test@example.com --password-ref env:APP_PASS
  printf '%s' "$WP_PASS" | codehelper connections set-secret --name local-wp
  codehelper browser test --recipe wp_login --site local-wp

Remote via SSH tunnel (GuardURL-safe loopback):
  ssh -N -L 8080:127.0.0.1:80 user@host
  codehelper connections add-site --name tunneled --url http://127.0.0.1:8080 --kind wordpress --user admin --password-ref secret`,
		RunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			if err := cfg.AddWebSite(s); err != nil {
				return err
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	f := c.Flags()
	f.StringVar(&path, "path", ".", "repo path")
	f.StringVar(&s.Name, "name", "", "profile name (required)")
	f.StringVar(&s.BaseURL, "url", "", "site base URL, e.g. http://wp-test.local (required)")
	f.StringVar(&s.Kind, "kind", "wordpress", "wordpress|laravel|django|drupal|magento|spa|generic")
	f.StringVar(&s.User, "user", "", "login username")
	f.StringVar(&s.LoginPath, "login-path", "", "override login path (default /wp-login.php)")
	f.StringVar(&s.AdminPath, "admin-path", "", "override admin path (default /wp-admin/)")
	f.StringVar(&s.PasswordRef, "password-ref", "", "secret reference: env:VAR or secret (never inline)")
	return c
}

// splitCommaArg parses a comma-separated flag into trimmed, non-empty items.
func splitCommaArg(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// connectionsSetSecretCmd stores an ENCRYPTED password for a DB profile in the
// out-of-repo secret store and points the profile's password_ref at it. The
// plaintext is read from stdin (not a flag) so it never lands in shell history.
func connectionsSetSecretCmd() *cobra.Command {
	var name, path string
	c := &cobra.Command{
		Use:   "set-secret",
		Short: "Store an encrypted password for a DB or website profile (reads plaintext from stdin)",
		Long: `Reads a password from stdin, encrypts it (AES-256-GCM) into the global
secret store outside the repo, and sets the profile's password_ref to "secret".
The plaintext is never written to the repo, the profile file, or any tool output.

Example:  printf '%s' "$DB_PASS" | codehelper connections set-secret --name staging_pg
          printf '%s' "$WP_PASS" | codehelper connections set-secret --name local-wp`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			pw, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			plaintext := strings.TrimRight(string(pw), "\r\n")
			if plaintext == "" {
				return fmt.Errorf("no password on stdin; pipe it in, e.g. printf '%%s' \"$PASS\" | codehelper connections set-secret --name %s", name)
			}
			if err := secrets.Set(repoRoot, name, plaintext); err != nil {
				return err
			}
			// Point the matching DB or website profile at the secret store.
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			for i := range cfg.Databases {
				if strings.EqualFold(cfg.Databases[i].Name, name) {
					cfg.Databases[i].PasswordRef = connections.SecretRef
				}
			}
			for i := range cfg.WebSites {
				if strings.EqualFold(cfg.WebSites[i].Name, name) {
					cfg.WebSites[i].PasswordRef = connections.SecretRef
				}
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"ok": true, "name": name, "stored": "encrypted (out of repo)", "password_ref": connections.SecretRef})
		},
	}
	c.Flags().StringVar(&name, "name", "", "profile name to attach the secret to (required)")
	c.Flags().StringVar(&path, "path", ".", "repo path")
	return c
}

// connectionsEnableCmd builds the enable/disable toggle (shared shape).
func connectionsEnableCmd(use string, enabled bool) *cobra.Command {
	var name, path string
	c := &cobra.Command{
		Use:   use,
		Short: fmt.Sprintf("Turn a connection profile %sd by name (db or ssh)", use),
		RunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			if !cfg.SetEnabled(name, enabled) {
				return fmt.Errorf("no connection profile named %q", name)
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	c.Flags().StringVar(&name, "name", "", "profile name to toggle (required)")
	c.Flags().StringVar(&path, "path", ".", "repo path")
	return c
}

func connectionsRemoveCmd() *cobra.Command {
	var name, path string
	c := &cobra.Command{
		Use:   "rm",
		Short: "Remove a connection profile by name (db or ssh)",
		RunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			if !cfg.Remove(name) {
				return fmt.Errorf("no connection profile named %q", name)
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	c.Flags().StringVar(&name, "name", "", "profile name to remove (required)")
	c.Flags().StringVar(&path, "path", ".", "repo path")
	return c
}

func connectionsPolicyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "policy",
		Short: "Manage per-project security policy (CLI only — never via MCP)",
	}
	c.AddCommand(connectionsPolicySetCmd())
	return c
}

func connectionsPolicySetCmd() *cobra.Command {
	var path, agentTrust, verifyAllow, githubRepo, githubTokenRef string
	var allowGit, githubDisable bool
	c := &cobra.Command{
		Use:   "set",
		Short: "Set security policy caps for verify and agent trust",
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			p := cfg.Policy
			if agentTrust != "" {
				p.AgentTrust = agentTrust
			}
			if verifyAllow != "" {
				p.VerifyAllowlist = splitCommaArg(verifyAllow)
			}
			if cmd.Flags().Changed("allow-git") {
				p.AllowGit = allowGit
			}
			if cmd.Flags().Changed("github-repo") || cmd.Flags().Changed("github-token-ref") || cmd.Flags().Changed("github-disable") {
				if p.GitHub == nil {
					p.GitHub = &connections.GitHubPolicy{}
				}
				if githubRepo != "" {
					p.GitHub.Repo = githubRepo
				}
				if githubTokenRef != "" {
					p.GitHub.TokenRef = githubTokenRef
				}
				if cmd.Flags().Changed("github-disable") {
					p.GitHub.Disabled = githubDisable
				}
			}
			if err := cfg.SetPolicy(p); err != nil {
				return err
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	c.Flags().StringVar(&path, "path", ".", "repo path")
	c.Flags().StringVar(&agentTrust, "agent-trust", "", "none (default) or allowlist_edits")
	c.Flags().StringVar(&verifyAllow, "verify-allowlist", "", "comma-separated basename cap for local verify")
	c.Flags().BoolVar(&allowGit, "allow-git", false, "permit git in local verify (goal.md §19 opt-in)")
	c.Flags().StringVar(&githubRepo, "github-repo", "", "GitHub owner/name for ci_status (optional — detect from remote)")
	c.Flags().StringVar(&githubTokenRef, "github-token-ref", "", "env:VAR reference for GitHub token (never inline)")
	c.Flags().BoolVar(&githubDisable, "github-disable", false, "disable GitHub/ci_status integration")
	return c
}

func connectionsAddLogCmd() *cobra.Command {
	var src connections.LogSource
	var path string
	c := &cobra.Command{
		Use:   "add-log",
		Short: "Add or update a log source profile",
		RunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			if err := cfg.AddLogSource(src); err != nil {
				return err
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	f := c.Flags()
	f.StringVar(&path, "path", ".", "repo path")
	f.StringVar(&src.Name, "name", "", "log source name (required)")
	f.StringVar(&src.Kind, "kind", "", "nginx, apache, wordpress, app, or custom (required)")
	f.StringVar(&src.Path, "log-path", "", "log file path, repo-relative or absolute (required)")
	f.StringVar(&src.SSHHost, "ssh-host", "", "optional SSH host alias for remote paths")
	return c
}

func connectionsDetectLogsCmd() *cobra.Command {
	var path string
	var apply bool
	c := &cobra.Command{
		Use:   "detect-logs",
		Short: "Suggest log paths from project layout (WordPress, nginx, Apache, app logs)",
		RunE: func(_ *cobra.Command, args []string) error {
			repoRoot, err := connectionsRepoRoot(append(args, path))
			if err != nil {
				return err
			}
			suggested := connections.DetectLogSources(repoRoot)
			if !apply {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"suggested": suggested,
					"note":      "dry-run — pass --apply to persist suggestions",
				})
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			for _, s := range suggested {
				_ = cfg.AddLogSource(s)
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	c.Flags().StringVar(&path, "path", ".", "repo path")
	c.Flags().BoolVar(&apply, "apply", false, "persist detected log sources")
	return c
}

func connectionsRemoveLogCmd() *cobra.Command {
	var name, path string
	c := &cobra.Command{
		Use:   "rm-log",
		Short: "Remove a log source by name",
		RunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			if !cfg.RemoveLogSource(name) {
				return fmt.Errorf("no log source named %q", name)
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	c.Flags().StringVar(&name, "name", "", "log source name to remove (required)")
	c.Flags().StringVar(&path, "path", ".", "repo path")
	return c
}

func connectionsAddRecipeCmd() *cobra.Command {
	var path, host, name, argv, params string
	var readOnly bool
	c := &cobra.Command{
		Use:   "add-recipe",
		Short: "Add or update a named SSH recipe on a host (for remote_exec)",
		RunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			r := connections.Recipe{
				Name: name, Argv: splitCommaArg(argv), Params: splitCommaArg(params), ReadOnly: readOnly,
			}
			if err := cfg.AddRecipe(host, r); err != nil {
				return err
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	c.Flags().StringVar(&path, "path", ".", "repo path")
	c.Flags().StringVar(&host, "host", "", "SSH host profile name (required)")
	c.Flags().StringVar(&name, "name", "", "recipe name (required)")
	c.Flags().StringVar(&argv, "argv", "", "comma-separated remote argv, e.g. tail,-n,{lines},{path}")
	c.Flags().StringVar(&params, "params", "", "comma-separated param names referenced in argv")
	c.Flags().BoolVar(&readOnly, "read-only", true, "mark recipe read-only")
	return c
}

func connectionsAddAliasCmd() *cobra.Command {
	var path, name, argv, cwd, remoteHost, remoteRecipe string
	var requiresApproval bool
	c := &cobra.Command{
		Use:   "add-alias",
		Short: "Add or update a command alias (for run_alias MCP tool)",
		RunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			a := connections.CommandAlias{
				Name: name, Argv: splitCommaArg(argv), Cwd: cwd,
				RemoteHost: remoteHost, RemoteRecipe: remoteRecipe, RequiresApproval: requiresApproval,
			}
			if err := cfg.AddAlias(a); err != nil {
				return err
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	c.Flags().StringVar(&path, "path", ".", "repo path")
	c.Flags().StringVar(&name, "name", "", "alias name (required)")
	c.Flags().StringVar(&argv, "argv", "", "comma-separated local argv (or use --remote-host + --remote-recipe)")
	c.Flags().StringVar(&cwd, "cwd", "", "working directory for local alias")
	c.Flags().StringVar(&remoteHost, "remote-host", "", "SSH host for remote alias")
	c.Flags().StringVar(&remoteRecipe, "remote-recipe", "", "recipe name on remote host")
	c.Flags().BoolVar(&requiresApproval, "requires-approval", false, "require approved=true in run_alias")
	return c
}

func connectionsRemoveAliasCmd() *cobra.Command {
	var name, path string
	c := &cobra.Command{
		Use:   "rm-alias",
		Short: "Remove a command alias by name",
		RunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, err := connectionsRepoRoot([]string{path})
			if err != nil {
				return err
			}
			cfg, err := connections.Load(repoRoot)
			if err != nil {
				return err
			}
			if !cfg.RemoveAlias(name) {
				return fmt.Errorf("no alias named %q", name)
			}
			if err := connections.Save(repoRoot, cfg); err != nil {
				return err
			}
			return printConnections(repoRoot, cfg)
		},
	}
	c.Flags().StringVar(&name, "name", "", "alias name to remove (required)")
	c.Flags().StringVar(&path, "path", ".", "repo path")
	return c
}
