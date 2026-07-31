// Package setupsuggest builds LLM-facing per-project setup steps for browser /
// CMS / remote testing. Agents should propose these to the user before the first
// browser run; users persist overrides in projcfg (mcp-config.json) and
// connections website/SSH profiles — never by inventing credentials.
package setupsuggest

import (
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/projcfg"
	"github.com/VeyrForge/codehelper/internal/setup"
	"github.com/VeyrForge/codehelper/internal/web"
)

// Priority levels for agent presentation order.
const (
	PriorityRequired    = "required"
	PriorityRecommended = "recommended"
	PriorityOptional    = "optional"
)

// Suggestion is one concrete setup action the agent should propose to the user.
type Suggestion struct {
	ID            string   `json:"id"`
	Priority      string   `json:"priority"`
	Title         string   `json:"title"`
	Steps         []string `json:"steps"`
	ProposeToUser bool     `json:"propose_to_user"`
	ConfigKeys    []string `json:"config_keys,omitempty"`
	Done          bool     `json:"done,omitempty"`
}

// Report is the setup_suggestions payload for project_context / kickoff / doctor.
type Report struct {
	Stack              string       `json:"stack"`
	SiteKind           string       `json:"site_kind,omitempty"`
	DefaultRecipe      string       `json:"default_recipe,omitempty"`
	LocalURLHint       string       `json:"local_url_hint,omitempty"`
	CredentialsHint    string       `json:"credentials_hint,omitempty"`
	Suggestions        []Suggestion `json:"suggestions"`
	RemotePatterns     []string     `json:"remote_patterns,omitempty"`
	MCPSnippet         string       `json:"mcp_snippet,omitempty"`
	BrowserDefaults    string       `json:"browser_defaults_hint,omitempty"`
	ProjcfgKeys        []string     `json:"projcfg_keys,omitempty"`
	AgentNote          string       `json:"agent_note"`
	ConfiguredSites    []string     `json:"configured_sites,omitempty"`
	ConfiguredSSHHosts []string     `json:"configured_ssh_hosts,omitempty"`
}

// Input gathers stack + connection state used to build suggestions.
type Input struct {
	RepoRoot    string
	ProjectType string
	Framework   string
	Frameworks  []string
	Connections connections.Config
	Projcfg     projcfg.Config
	BinaryHint  string // optional absolute binary for .mcp.json snippet
	IncludeMCP  bool   // doctor/init: emit example .mcp.json
}

// Build returns stack-aware setup suggestions. Missing connections/config are
// treated as incomplete — the agent should propose them, not invent secrets.
func Build(in Input) Report {
	stack := pickStack(in)
	kind := web.DefaultSiteKind(stack)
	recipe := web.DefaultRecipeForKind(kind)
	localURL := firstNonEmpty(in.Projcfg.BrowserBaseURL, defaultLocalURL(stack))
	credHint := firstNonEmpty(in.Projcfg.TestCredentialsNote, defaultCredentialsHint(stack))

	var siteNames, sshNames []string
	for _, s := range in.Connections.WebSites {
		if s.Enabled() {
			siteNames = append(siteNames, s.Name)
		}
	}
	for _, h := range in.Connections.SSHHosts {
		if h.Enabled() {
			sshNames = append(sshNames, h.Name)
		}
	}
	hasSite := len(siteNames) > 0
	hasMatchingSite := false
	if pref := strings.TrimSpace(in.Projcfg.BrowserSite); pref != "" {
		for _, n := range siteNames {
			if strings.EqualFold(n, pref) {
				hasMatchingSite = true
				break
			}
		}
	} else {
		hasMatchingSite = hasSite
	}

	rep := Report{
		Stack:              stack,
		SiteKind:           kind,
		DefaultRecipe:      recipe,
		LocalURLHint:       localURL,
		CredentialsHint:    credHint,
		RemotePatterns:     remotePatterns(),
		ProjcfgKeys:        projcfg.BrowserConfigKeys(),
		ConfiguredSites:    siteNames,
		ConfiguredSSHHosts: sshNames,
		AgentNote: "Before the first browser run, propose setup_suggestions to the user and wait for confirmation. " +
			"Persist overrides with `codehelper connections add-site` / `codehelper config project` (browser_* keys). " +
			"Never paste passwords into MCP args — use password_ref env:VAR or connections set-secret. " +
			"Local loopback URLs are always allowed; LAN/RFC1918 needs allow_private=true; SSH remotes: port-forward to 127.0.0.1 then browse the local URL (fits GuardURL).",
		BrowserDefaults: "Global: ~/.codehelper/browser.json (action_previews). Env: CODEHELPER_BROWSER_HEADED=1. " +
			"Per-project: mcp-config.json browser_base_url, browser_site, browser_recipe, browser_headed, browser_allow_private, test_credentials_note.",
	}

	if in.IncludeMCP {
		bin := strings.TrimSpace(in.BinaryHint)
		if bin == "" {
			bin = setup.ResolveBinary()
		}
		rep.MCPSnippet = fmt.Sprintf(`{
  "mcpServers": {
    "codehelper": {
      "command": %q,
      "args": ["mcp"]
    }
  }
}`, bin)
	}

	// --- browser binary ---
	rep.Suggestions = append(rep.Suggestions, Suggestion{
		ID:       "browser_install",
		Priority: PriorityRecommended,
		Title:    "Install managed Chromium for browser tool",
		Steps: []string{
			"Run once: `codehelper browser install`",
			"Smoke: `codehelper browser test https://example.com`",
			"Confirm rod-tagged binary via `codehelper doctor`",
		},
		ProposeToUser: true,
	})

	// --- local URL / site profile ---
	if !hasMatchingSite {
		addSiteCmd := fmt.Sprintf(
			"codehelper connections add-site --name local-%s --url %s --kind %s --user <USER> --password-ref secret",
			kind, localURL, kind)
		steps := []string{
			fmt.Sprintf("Confirm the app is reachable at %s (or tell the agent the real base URL).", localURL),
			addSiteCmd,
			fmt.Sprintf("Store password (stdin, never a flag): `printf '%%s' \"$PASS\" | codehelper connections set-secret --name local-%s`", kind),
			fmt.Sprintf("Optional persist defaults: `codehelper config project --browser-site local-%s --browser-base-url %s --browser-recipe %s`", kind, localURL, recipe),
		}
		if credHint != "" {
			steps = append(steps, "Credentials location: "+credHint)
		}
		rep.Suggestions = append(rep.Suggestions, Suggestion{
			ID:            "add_site_profile",
			Priority:      PriorityRequired,
			Title:         "Configure site= connection for " + stack,
			Steps:         steps,
			ProposeToUser: true,
			ConfigKeys:    []string{"browser_site", "browser_base_url", "browser_recipe", "test_credentials_note"},
			Done:          false,
		})
	} else {
		rep.Suggestions = append(rep.Suggestions, Suggestion{
			ID:            "add_site_profile",
			Priority:      PriorityRequired,
			Title:         "Site profile already configured",
			Steps:         []string{fmt.Sprintf("Use browser site=%s recipe=%s (override with url= if needed).", strings.Join(siteNames, "|"), recipe)},
			ProposeToUser: false,
			Done:          true,
			ConfigKeys:    []string{"browser_site", "browser_recipe"},
		})
	}

	// --- headed mode ---
	rep.Suggestions = append(rep.Suggestions, Suggestion{
		ID:       "headed_mode",
		Priority: PriorityOptional,
		Title:    "Optional headed browser (watch the agent)",
		Steps: []string{
			"On a graphical display: pass headed=true (and optional slow_mo) on browser calls",
			"Or set CODEHELPER_BROWSER_HEADED=1 / project browser_headed=true",
			"Over plain SSH/CI: keep headless (headed needs a display or X11/Wayland forward)",
		},
		ProposeToUser: true,
		ConfigKeys:    []string{"browser_headed"},
	})

	// --- remote / SSH ---
	if len(sshNames) == 0 {
		rep.Suggestions = append(rep.Suggestions, Suggestion{
			ID:       "remote_access",
			Priority: PriorityOptional,
			Title:    "Remote site via SSH tunnel or remote base URL",
			Steps: append([]string{
				"Prefer SSH local port-forward so the browser hits loopback (always GuardURL-safe):",
				"  ssh -N -L 8080:127.0.0.1:80 user@remote-host",
				"  Then add-site --url http://127.0.0.1:8080 …",
				"Or add-ssh host profile for remote_exec / log_read; browse a public https:// staging URL if DNS resolves to public IPs",
				"LAN / private IP base URLs require allow_private=true (or browser_allow_private in project config)",
			}, remotePatterns()...),
			ProposeToUser: true,
			ConfigKeys:    []string{"browser_allow_private", "browser_base_url"},
		})
	} else {
		rep.Suggestions = append(rep.Suggestions, Suggestion{
			ID:       "remote_access",
			Priority: PriorityRecommended,
			Title:    "SSH hosts configured — use tunnel pattern for browser",
			Steps: []string{
				fmt.Sprintf("Configured SSH: %s", strings.Join(sshNames, ", ")),
				"For browser QA of a remote app: port-forward to 127.0.0.1:<local> then site base_url=http://127.0.0.1:<local>",
				"Use remote_exec / log_read for server-side checks; browser still needs an HTTP URL that passes GuardURL",
			},
			ProposeToUser: true,
			ConfigKeys:    []string{"browser_allow_private"},
			Done:          true,
		})
	}

	// --- first browser proof ---
	firstBrowser := fmt.Sprintf("browser recipe=%s", recipe)
	if hasMatchingSite || hasSite {
		site := strings.TrimSpace(in.Projcfg.BrowserSite)
		if site == "" && len(siteNames) > 0 {
			site = siteNames[0]
		}
		if site != "" {
			firstBrowser = fmt.Sprintf("browser recipe=%s site=%s wait_hydrate=true", recipe, site)
		}
	} else {
		firstBrowser = fmt.Sprintf("browser url=%s wait_hydrate=true outline=true (after site profile exists, switch to recipe=%s site=…)", localURL, recipe)
	}
	rep.Suggestions = append(rep.Suggestions, Suggestion{
		ID:       "first_browser_run",
		Priority: PriorityRecommended,
		Title:    "First browser proof after setup",
		Steps: []string{
			firstBrowser,
			"On failure: read diagnostics + failure screenshot; fix code or tighten locators via outline/snapshot; retest the same assert",
			"Claim done only with green browser assert AND finish_check.can_claim_done",
		},
		ProposeToUser: false,
	})

	return rep
}

func pickStack(in Input) string {
	fw := strings.ToLower(strings.TrimSpace(in.Framework))
	if fw != "" {
		return fw
	}
	pt := strings.ToLower(strings.TrimSpace(in.ProjectType))
	if strings.HasPrefix(pt, "wordpress") || pt == "woocommerce" {
		return "wordpress"
	}
	for _, f := range in.Frameworks {
		f = strings.ToLower(strings.TrimSpace(f))
		switch {
		case strings.Contains(f, "wordpress"), strings.Contains(f, "woocommerce"):
			return "wordpress"
		case strings.Contains(f, "laravel"):
			return "laravel"
		case strings.Contains(f, "django"):
			return "django"
		case strings.Contains(f, "drupal"):
			return "drupal"
		case strings.Contains(f, "magento"):
			return "magento"
		case strings.Contains(f, "next"):
			return "next"
		case strings.Contains(f, "nuxt"), strings.Contains(f, "react"), strings.Contains(f, "vue"), strings.Contains(f, "svelte"), strings.Contains(f, "angular"):
			return "spa"
		}
	}
	if pt != "" && pt != "unknown" {
		return pt
	}
	return "generic"
}

func defaultLocalURL(stack string) string {
	switch strings.ToLower(stack) {
	case "wordpress", "woocommerce":
		return "http://127.0.0.1:8080"
	case "laravel":
		return "http://127.0.0.1:8000"
	case "django":
		return "http://127.0.0.1:8000"
	case "drupal":
		return "http://127.0.0.1:8888"
	case "magento":
		return "http://127.0.0.1:8080"
	case "next", "nuxt", "spa", "react", "vue", "svelte", "angular":
		return "http://127.0.0.1:3000"
	default:
		return "http://127.0.0.1:3000"
	}
}

func defaultCredentialsHint(stack string) string {
	switch strings.ToLower(stack) {
	case "wordpress", "woocommerce":
		return "Ask the user for WP admin user; store password via connections set-secret (never commit). Local docker/env often uses WP_ADMIN_PASSWORD / .env."
	case "laravel":
		return "Ask the user for a seeded test user (database/seeders or .env). Store password via set-secret; do not read .env into MCP args."
	case "django":
		return "Ask the user for a Django superuser (createsuperuser). Store password via set-secret."
	case "drupal":
		return "Ask the user for Drupal admin credentials (or drush uli for one-time login — prefer durable set-secret for recipes)."
	case "magento":
		return "Ask the user for Magento admin user; store password via set-secret. Admin path may be customized (set --admin-path)."
	default:
		return "Ask the user where test credentials live (1Password, CI secrets, local-only env). Never invent or commit passwords."
	}
}

func remotePatterns() []string {
	return []string{
		"SSH tunnel (preferred): ssh -N -L <local>:127.0.0.1:<remote_port> user@host → browser url=http://127.0.0.1:<local> (loopback always allowed by GuardURL)",
		"Remote public HTTPS: browser url=https://staging.example.com (public DNS; no allow_private)",
		"LAN / RFC1918: browser url=http://192.168.x.x:… allow_private=true (or project browser_allow_private)",
		"Headed over SSH: use headless, or forward display (X11/Wayland); headed=true needs a graphical session",
		"DB via SSH: connections add-db --ssh-tunnel <ssh-profile> (separate from browser; browser still needs HTTP)",
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// FormatText renders a human-readable doctor/init block.
func FormatText(rep Report) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("setup suggestions (%s):\n", rep.Stack))
	b.WriteString("  " + rep.AgentNote + "\n")
	if rep.LocalURLHint != "" {
		b.WriteString(fmt.Sprintf("  local_url_hint: %s\n", rep.LocalURLHint))
	}
	if rep.DefaultRecipe != "" {
		b.WriteString(fmt.Sprintf("  default_recipe: %s (kind=%s)\n", rep.DefaultRecipe, rep.SiteKind))
	}
	for _, s := range rep.Suggestions {
		status := s.Priority
		if s.Done {
			status = "done"
		}
		b.WriteString(fmt.Sprintf("  [%s] %s\n", status, s.Title))
		for _, step := range s.Steps {
			b.WriteString("    - " + step + "\n")
		}
	}
	if rep.MCPSnippet != "" {
		b.WriteString("  suggested .mcp.json:\n")
		for _, line := range strings.Split(rep.MCPSnippet, "\n") {
			b.WriteString("    " + line + "\n")
		}
	}
	if rep.BrowserDefaults != "" {
		b.WriteString("  browser defaults: " + rep.BrowserDefaults + "\n")
	}
	return b.String()
}
