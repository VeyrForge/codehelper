package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/VeyrForge/codehelper/internal/usage"
	"github.com/spf13/cobra"
)

func usageCmd() *cobra.Command {
	var asJSON bool
	var verbose bool
	var refs int
	c := &cobra.Command{
		Use:   "usage [path]",
		Short: "Per-project tool-usage + token report (codehelper output + real Claude tokens)",
		Long: `Show where context and tokens go for a project.

Three layers:
  • CODEHELPER OUTPUT — how much context each MCP tool injected, by tool /
    session / client (claude-code, cursor, codex). Measurable for every client.
  • CLAUDE MODEL TOKENS — real billed input/output/cache tokens per session,
    parsed from Claude Code's local transcripts (~/.claude/projects).
  • CODEX MODEL TOKENS — real cumulative tokens per session, parsed from Codex
    rollouts (~/.codex/sessions). Cursor does not expose per-session token counts
    locally, so for that client only the codehelper output above is available.

Also surfaces the last verify/diagnostics outcome and a recent-call trail.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
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
			rep, err := usage.BuildReport(root, refs)
			if err != nil {
				return err
			}
			rep.GeneratedAt = time.Now()
			rep.Verbose = verbose
			if asJSON {
				fmt.Println(rep.RenderJSON())
				return nil
			}
			fmt.Print(rep.Render())
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON report")
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "expand the recent-call trail to show each call's input + output preview")
	c.Flags().IntVar(&refs, "refs", 20, "number of recent tool calls in the trail (0 disables)")
	return c
}
