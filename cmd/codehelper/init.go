package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/helpcatalog"
	"github.com/VeyrForge/codehelper/internal/hints"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/projcfg"
	"github.com/VeyrForge/codehelper/internal/setup"
	"github.com/VeyrForge/codehelper/internal/setupsuggest"
	"github.com/spf13/cobra"
)

func initCmd() *cobra.Command {
	var force bool
	var noMCP bool
	var noTools bool
	var track bool
	c := &cobra.Command{
		Use:   "init [path]",
		Short: "Initialize codehelper in any git repo (gitignore, index, watch daemon)",
		Long: "Prepare a project directory for Codehelper from anywhere on your machine. " +
			"Ensures `.codehelper/` is listed in `.gitignore` when that file exists, " +
			"runs `analyze` for the repo (or shard), and starts the watch daemon. " +
			"Run once per clone; use `codehelper analyze --force` to rebuild the index.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return err
			}
			gitRoot, err := gitutil.FindGitRoot(root)
			if err != nil {
				return fmt.Errorf("init requires a git repository: %w", err)
			}
			if added, err := gitutil.EnsureCodehelperGitignored(gitRoot); err != nil {
				return err
			} else if added {
				fmt.Fprintln(os.Stderr, "init: appended .codehelper/ to .gitignore")
			}
			if err := runAnalyze(cmd.Context(), root, analyzeFlags{force: force}); err != nil {
				return err
			}
			// Persist the per-project MCP runtime config (tools on/off + telemetry).
			// Disabling tools makes this a "baseline" project: the server keeps
			// recording (so you can A/B against a tools-on project) but the agent
			// works without codehelper's context.
			pcfg := projcfg.Default()
			pcfg.ToolsEnabled = !noTools
			if !track {
				pcfg.Track = projcfg.TrackOff
			}
			if err := projcfg.Save(gitRoot, pcfg); err != nil {
				fmt.Fprintln(os.Stderr, "init: project config:", err)
			} else {
				fmt.Fprintf(os.Stderr, "init: MCP tools %s, telemetry %q for this project\n", enabledLabel(pcfg.ToolsEnabled), pcfg.Track)
			}
			if !noMCP {
				// Editor wiring is a convenience, not load-bearing for indexing —
				// warn but don't fail init over any of these steps.
				if written, err := setup.ProjectMCP(gitRoot, setup.ResolveBinary()); err != nil {
					fmt.Fprintln(os.Stderr, "init: MCP config:", err)
				} else {
					for _, p := range written {
						fmt.Fprintln(os.Stderr, "init: wired MCP server in", p)
					}
				}
				// .mcp.json is necessary but not sufficient for Claude Code: it gates
				// project servers behind an approval. Record it so the tools actually
				// load instead of silently never appearing.
				if enabled, err := setup.ClaudeCodeEnable(gitRoot, "codehelper"); err != nil {
					fmt.Fprintln(os.Stderr, "init: Claude Code enable:", err)
				} else if enabled {
					fmt.Fprintln(os.Stderr, "init: approved codehelper for Claude Code (restart Claude Code to load it)")
				}
				// Codex has no per-project config or approval gate — register globally.
				if added, err := setup.CodexMCP("codehelper"); err != nil {
					fmt.Fprintln(os.Stderr, "init: Codex MCP:", err)
				} else if added {
					fmt.Fprintln(os.Stderr, "init: registered codehelper for Codex (~/.codex/config.toml)")
				}
				// A connected MCP is invisible unless the agent is told to use it.
				// Each client reads a different file: Codex→AGENTS.md (written by
				// analyze), Claude Code→CLAUDE.md, Cursor→.cursor/rules. Without these
				// the tools show "connected" but never get called.
				if err := setup.WriteClientRules(gitRoot); err != nil {
					fmt.Fprintln(os.Stderr, "init: client rules:", err)
				} else {
					fmt.Fprintln(os.Stderr, "init: wrote tool-first rules (CLAUDE.md block + .cursor/rules/codehelper.mdc) so the agent actually calls the tools")
				}
				if err := helpcatalog.WriteProjectReference(gitRoot); err != nil {
					fmt.Fprintln(os.Stderr, "init: MCP_TOOLS.md:", err)
				} else {
					fmt.Fprintln(os.Stderr, "init: wrote .codehelper/MCP_TOOLS.md tool catalog")
				}
				hints.EnsureBuiltin()
				fmt.Fprintln(os.Stderr, "init: Cursor, Claude Code & Codex are set to load codehelper's tools for this project")
			}
			// Stack-aware browser/CMS setup suggestions (propose to user; do not invent secrets).
			if pr, perr := profile.ReadOrGenerate(gitRoot); perr == nil && pr != nil {
				conn, _ := connections.Load(gitRoot)
				sug := setupsuggest.Build(setupsuggest.Input{
					RepoRoot:    gitRoot,
					ProjectType: pr.ProjectType,
					Framework:   pr.Framework,
					Connections: conn,
					Projcfg:     pcfg,
					IncludeMCP:  true,
					BinaryHint:  setup.ResolveBinary(),
				})
				fmt.Fprint(os.Stderr, setupsuggest.FormatText(sug))
			}
			fmt.Fprintln(os.Stderr, "init: ready — index + watch daemon active for", gitRoot)
			return nil
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "force full re-index")
	c.Flags().BoolVar(&noMCP, "no-mcp", false, "do not write per-project Cursor/Claude Code MCP config")
	c.Flags().BoolVar(&noTools, "no-tools", false, "run the MCP server for this project in baseline mode: serve + track usage but return no tool results to the agent (for A/B comparison)")
	c.Flags().BoolVar(&track, "track", true, "record per-project tool-usage telemetry (use --track=false to disable)")
	return c
}

// enabledLabel renders a tools on/off state for CLI output.
func enabledLabel(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}
