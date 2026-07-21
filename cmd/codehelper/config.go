package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/projcfg"
	"github.com/VeyrForge/codehelper/internal/web"
	"github.com/VeyrForge/codehelper/internal/websearch"
	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Manage user-level Codehelper settings (~/.codehelper/)",
	}
	c.AddCommand(configLLMCmd())
	c.AddCommand(configProjectCmd())
	c.AddCommand(configSearchCmd())
	c.AddCommand(configBrowserCmd())
	return c
}

// configSearchCmd shows or sets the web-search provider + API keys used by the
// `web_search` MCP tool. Keys are stored in ~/.codehelper/search.json (0600);
// env vars (TAVILY_API_KEY / BRAVE_API_KEY / CODEHELPER_SEARCH_PROVIDER) win.
func configSearchCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "search",
		Short: "Show or set the web_search provider and API keys",
		Long: `Configure the web_search tool's backend.

Free options (no credit card):
  • Tavily — 1000 searches/month, cleanest output for agents — https://tavily.com
  • Brave  — 2000 searches/month, independent index — https://brave.com/search/api
  • DuckDuckGo — keyless fallback, best-effort (no key needed)

  ch config search set --provider tavily --key tvly-xxxx
  ch config search set --provider brave  --key BSA-xxxx
  ch config search show`,
	}

	show := &cobra.Command{
		Use:   "show",
		Short: "Print the effective search config (keys redacted)",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg := websearch.Effective()
			path, _ := websearch.Path()
			out := map[string]any{
				"file":           path,
				"provider":       websearch.ChooseProvider(cfg, ""),
				"configured":     cfg.Provider,
				"tavily_key_set": strings.TrimSpace(cfg.TavilyKey) != "",
				"brave_key_set":  strings.TrimSpace(cfg.BraveKey) != "",
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}

	var provider, key string
	set := &cobra.Command{
		Use:   "set",
		Short: "Set the provider and/or an API key (~/.codehelper/search.json, 0600)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := websearch.Load()
			if err != nil {
				return err
			}
			prov := strings.ToLower(strings.TrimSpace(provider))
			if prov != "" {
				switch prov {
				case websearch.Tavily, websearch.Brave, websearch.DuckDuckGo:
					cfg.Provider = prov
				default:
					return fmt.Errorf("--provider must be tavily|brave|duckduckgo, got %q", provider)
				}
			}
			if k := strings.TrimSpace(key); k != "" {
				// Route the key to whichever provider this set targets.
				switch {
				case prov == websearch.Brave:
					cfg.BraveKey = k
				case prov == websearch.Tavily:
					cfg.TavilyKey = k
				default:
					return fmt.Errorf("--key needs --provider tavily or brave to know where to store it")
				}
			}
			if err := websearch.Save(cfg); err != nil {
				return err
			}
			path, _ := websearch.Path()
			fmt.Printf("saved %s (provider: %s)\n", path, websearch.ChooseProvider(websearch.Effective(), ""))
			return nil
		},
	}
	set.Flags().StringVar(&provider, "provider", "", "tavily | brave | duckduckgo")
	set.Flags().StringVar(&key, "key", "", "API key for the chosen provider")

	c.AddCommand(show, set)
	return c
}

// configBrowserCmd shows or sets browser tool settings (~/.codehelper/browser.json).
func configBrowserCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "browser",
		Short: "Show or set browser automation settings",
		Long: `Browser tool settings stored in ~/.codehelper/browser.json.

  action_previews — when enabled, the browser MCP tool can return a viewport
  screenshot after each interaction step (click, fill, type, …) when you pass
  preview_actions=true on a call. Disabled by default to keep token use lean.

  ch config browser set --action-previews on
  ch config browser show`,
	}

	show := &cobra.Command{
		Use:   "show",
		Short: "Print effective browser settings",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg := web.EffectiveBrowserConfig()
			path, _ := web.BrowserConfigPath()
			out := map[string]any{
				"file":            path,
				"action_previews": cfg.ActionPreviews,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}

	var actionPreviews string
	set := &cobra.Command{
		Use:   "set",
		Short: "Write ~/.codehelper/browser.json",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := web.LoadBrowserConfig()
			if err != nil {
				return err
			}
			if actionPreviews != "" {
				on, err := parseOnOff(actionPreviews)
				if err != nil {
					return fmt.Errorf("--action-previews must be on or off, got %q", actionPreviews)
				}
				cfg.ActionPreviews = on
			}
			if err := web.SaveBrowserConfig(cfg); err != nil {
				return err
			}
			path, _ := web.BrowserConfigPath()
			fmt.Printf("saved %s (action_previews: %v)\n", path, cfg.ActionPreviews)
			return nil
		},
	}
	set.Flags().StringVar(&actionPreviews, "action-previews", "", "on|off — allow step screenshots when preview_actions=true on a browser call")

	c.AddCommand(show, set)
	return c
}

// configProjectCmd shows or flips the per-project MCP runtime config (tools
// on/off + telemetry level) without re-running init — the knob for A/B testing
// codehelper against a built-in-tools-only baseline on the same repo.
func configProjectCmd() *cobra.Command {
	var toolsFlag, trackFlag, minimalFlag string
	var verifyCwd, verifyBuild, verifyTest, verifyLint string
	var browserBaseURL, browserSite, browserRecipe, testCredsNote, browserHeaded, browserAllowPrivate string
	c := &cobra.Command{
		Use:   "project [path]",
		Short: "Show or set per-project MCP tools on/off and telemetry level",
		Long: `Per-project MCP runtime config, stored beside the index.

With no flags it prints the effective config. Flags:
  --tools on|off       expose codehelper tools to the agent (off = baseline mode:
                       server still runs and records usage, but tool calls return
                       a redirect so the agent uses its built-in Read/Grep)
  --minimal on|off     trim the advertised tool list (tools/list) to the main
                       tools to cut tool-definition token cost; hidden tools stay
                       callable by name (CODEHELPER_MINIMAL_TOOLS forces this on
                       for every project)
  --track off|summary   whether to record each call (summary = capped previews
                       + exact byte/token counts, enough for an A/B comparison)
  --verify-cwd DIR     repo-relative directory for verify/diagnostics (e.g. rust)
  --verify-build CMD   override build command for verify
  --verify-test CMD    override test command for verify
  --verify-lint CMD    override lint command for verify
  --browser-base-url   default local/remote HTTP base for setup suggestions
  --browser-site       default connections website name for browser site=
  --browser-recipe     default browser recipe (wp_login|laravel_login|…)
  --browser-headed on|off   default headed mode for browser tool
  --browser-allow-private on|off   default allow_private for LAN URLs
  --test-credentials-note   where test logins live (never a password)

Toggling takes effect on the running MCP server without a restart.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return applyProjectConfig(os.Stdout, projectConfigEdit{
				path: argPath(args), tools: toolsFlag, track: trackFlag, minimal: minimalFlag,
				verifyCwd: verifyCwd, verifyBuild: verifyBuild, verifyTest: verifyTest, verifyLint: verifyLint,
				browserBaseURL: browserBaseURL, browserSite: browserSite, browserRecipe: browserRecipe,
				browserHeaded: browserHeaded, browserAllowPrivate: browserAllowPrivate,
				testCredentialsNote: testCredsNote,
			})
		},
	}
	c.Flags().StringVar(&toolsFlag, "tools", "", "on|off — expose codehelper tools to the agent")
	c.Flags().StringVar(&minimalFlag, "minimal", "", "on|off — advertise only the main tools in tools/list")
	c.Flags().StringVar(&trackFlag, "track", "", "off|summary — whether to record usage telemetry")
	c.Flags().StringVar(&verifyCwd, "verify-cwd", "", "repo-relative cwd for verify/diagnostics")
	c.Flags().StringVar(&verifyBuild, "verify-build", "", "build command override for verify")
	c.Flags().StringVar(&verifyTest, "verify-test", "", "test command override for verify")
	c.Flags().StringVar(&verifyLint, "verify-lint", "", "lint command override for verify")
	c.Flags().StringVar(&browserBaseURL, "browser-base-url", "", "default browser base URL hint")
	c.Flags().StringVar(&browserSite, "browser-site", "", "default connections website name")
	c.Flags().StringVar(&browserRecipe, "browser-recipe", "", "default browser recipe name")
	c.Flags().StringVar(&browserHeaded, "browser-headed", "", "on|off — default headed browser")
	c.Flags().StringVar(&browserAllowPrivate, "browser-allow-private", "", "on|off — default allow_private")
	c.Flags().StringVar(&testCredsNote, "test-credentials-note", "", "where test credentials live (not a password)")
	return c
}

// projectConfigEdit is a requested change to one project's MCP runtime config.
// Empty tools/track mean "leave unchanged"; with both empty the config is only
// shown. It is the single shape shared by `config project` and the top-level
// `codehelper --no-tools/--tools/--track` shortcut.
type projectConfigEdit struct {
	path                string // repo path (or subdir); "" → "."
	tools               string // "", on|off (parseOnOff spellings)
	track               string // "", off|summary
	minimal             string // "", on|off (parseOnOff spellings)
	verifyCwd           string
	verifyBuild         string
	verifyTest          string
	verifyLint          string
	browserBaseURL      string
	browserSite         string
	browserRecipe       string
	browserHeaded       string // "", on|off
	browserAllowPrivate string // "", on|off
	testCredentialsNote string
}

// applyProjectConfig resolves the project, applies the edit, persists it when
// anything changed, and writes the effective config as JSON. It is the one place
// the toggle rules live so every entry point behaves identically.
func applyProjectConfig(w io.Writer, edit projectConfigEdit) error {
	// Resolve the same root the server keys telemetry by (registry RootPath ==
	// analyze indexRoot), so the file we write is the file it reads.
	_, repoRoot, err := indexer.ResolveIndexPaths(edit.path, "")
	if err != nil {
		return fmt.Errorf("setting project config requires a git repository: %w", err)
	}
	cfg, err := projcfg.Load(repoRoot)
	if err != nil {
		return err
	}

	changed := false
	if edit.tools != "" {
		on, err := parseOnOff(edit.tools)
		if err != nil {
			return err
		}
		cfg.ToolsEnabled = on
		// Entering baseline mode with telemetry off would defeat the purpose; keep
		// recording (summary) unless the user is also setting --track.
		if !on && edit.track == "" && cfg.Track == projcfg.TrackOff {
			cfg.Track = projcfg.TrackSummary
		}
		changed = true
	}
	if edit.track != "" {
		switch edit.track {
		case projcfg.TrackOff, projcfg.TrackSummary:
			cfg.Track = edit.track
		default:
			return fmt.Errorf("--track must be off or summary, got %q", edit.track)
		}
		changed = true
	}
	if edit.minimal != "" {
		on, err := parseOnOff(edit.minimal)
		if err != nil {
			return fmt.Errorf("--minimal must be on or off, got %q", edit.minimal)
		}
		cfg.MinimalTools = on
		changed = true
	}
	if edit.verifyCwd != "" {
		cfg.VerifyCwd = strings.TrimSpace(edit.verifyCwd)
		changed = true
	}
	if edit.verifyBuild != "" {
		cfg.VerifyBuild = strings.TrimSpace(edit.verifyBuild)
		changed = true
	}
	if edit.verifyTest != "" {
		cfg.VerifyTest = strings.TrimSpace(edit.verifyTest)
		changed = true
	}
	if edit.verifyLint != "" {
		cfg.VerifyLint = strings.TrimSpace(edit.verifyLint)
		changed = true
	}
	if edit.browserBaseURL != "" {
		cfg.BrowserBaseURL = strings.TrimSpace(edit.browserBaseURL)
		changed = true
	}
	if edit.browserSite != "" {
		cfg.BrowserSite = strings.TrimSpace(edit.browserSite)
		changed = true
	}
	if edit.browserRecipe != "" {
		cfg.BrowserRecipe = strings.TrimSpace(edit.browserRecipe)
		changed = true
	}
	if edit.testCredentialsNote != "" {
		cfg.TestCredentialsNote = strings.TrimSpace(edit.testCredentialsNote)
		changed = true
	}
	if edit.browserHeaded != "" {
		on, err := parseOnOff(edit.browserHeaded)
		if err != nil {
			return fmt.Errorf("--browser-headed must be on or off, got %q", edit.browserHeaded)
		}
		cfg.BrowserHeaded = &on
		changed = true
	}
	if edit.browserAllowPrivate != "" {
		on, err := parseOnOff(edit.browserAllowPrivate)
		if err != nil {
			return fmt.Errorf("--browser-allow-private must be on or off, got %q", edit.browserAllowPrivate)
		}
		cfg.BrowserAllowPrivate = &on
		changed = true
	}

	if changed {
		if err := projcfg.Save(repoRoot, cfg); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	out := map[string]any{
		"path":                  projcfg.Path(repoRoot),
		"repo_root":             repoRoot,
		"tools_enabled":         cfg.ToolsEnabled,
		"minimal_tools":         cfg.MinimalTools,
		"track":                 cfg.Track,
		"verify_cwd":            cfg.VerifyCwd,
		"verify_build":          cfg.VerifyBuild,
		"verify_test":           cfg.VerifyTest,
		"verify_lint":           cfg.VerifyLint,
		"browser_base_url":      cfg.BrowserBaseURL,
		"browser_site":          cfg.BrowserSite,
		"browser_recipe":        cfg.BrowserRecipe,
		"test_credentials_note": cfg.TestCredentialsNote,
	}
	if cfg.BrowserHeaded != nil {
		out["browser_headed"] = *cfg.BrowserHeaded
	}
	if cfg.BrowserAllowPrivate != nil {
		out["browser_allow_private"] = *cfg.BrowserAllowPrivate
	}
	return enc.Encode(out)
}

// argPath returns the first positional path arg, or "." when none was given.
func argPath(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "."
}

// parseOnOff accepts the friendly on/off (plus true/false) spellings for --tools.
func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "enable", "enabled", "1":
		return true, nil
	case "off", "false", "disable", "disabled", "0":
		return false, nil
	default:
		return false, fmt.Errorf("--tools must be on or off, got %q", s)
	}
}

func configLLMCmd() *cobra.Command {
	var baseURL, chatURL, completionPath, model string
	var temperature float64
	var hasTemp bool
	c := &cobra.Command{
		Use:   "llm",
		Short: "Show or set terminal LLM settings (env overrides file)",
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print effective LLM settings (API key redacted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := llm.ConfigFromEnv()
			path, _ := llm.UserFilePath()
			out := map[string]any{
				"file":             path,
				"base_url":         cfg.BaseURL,
				"chat_url":         cfg.ChatURL,
				"completion_path":  cfg.CompletionPath,
				"model":            cfg.Model,
				"completion_url":   cfg.CompletionURL(),
				"ready":            cfg.Ready(),
				"api_key_set":      strings.TrimSpace(cfg.APIKey) != "",
				"api_key_from_env": envSet("CODEHELPER_LLM_API_KEY") || envSet("OPENAI_API_KEY"),
			}
			if cfg.Temperature != nil {
				out["temperature"] = *cfg.Temperature
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}
	set := &cobra.Command{
		Use:   "set",
		Short: "Write ~/.codehelper/llm.json (API key stays env-only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cur, err := llm.LoadUserFile()
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("base-url") {
				cur.BaseURL = strings.TrimSpace(baseURL)
			}
			if cmd.Flags().Changed("chat-url") {
				cur.ChatURL = strings.TrimSpace(chatURL)
			}
			if cmd.Flags().Changed("completion-path") {
				cur.CompletionPath = strings.TrimSpace(completionPath)
			}
			if cmd.Flags().Changed("model") {
				cur.Model = strings.TrimSpace(model)
			}
			if hasTemp {
				cur.Temperature = &temperature
			}
			if err := llm.SaveUserFile(cur); err != nil {
				return err
			}
			path, _ := llm.UserFilePath()
			fmt.Printf("saved %s\n", path)
			fmt.Println("Set CODEHELPER_LLM_API_KEY (or OPENAI_API_KEY) in your shell for the API key.")
			return nil
		},
	}
	set.Flags().StringVar(&baseURL, "base-url", "", "OpenAI-compatible base URL")
	set.Flags().StringVar(&chatURL, "chat-url", "", "Full chat-completions URL override")
	set.Flags().StringVar(&completionPath, "completion-path", "", "Path appended to base URL")
	set.Flags().StringVar(&model, "model", "", "Model id")
	set.Flags().Float64Var(&temperature, "temperature", 0, "Sampling temperature")
	set.PreRunE = func(cmd *cobra.Command, args []string) error {
		hasTemp = cmd.Flags().Changed("temperature")
		return nil
	}
	c.AddCommand(show, set)
	return c
}

func envSet(key string) bool {
	return strings.TrimSpace(os.Getenv(key)) != ""
}
