package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/VeyrForge/codehelper/internal/setup"
	"github.com/spf13/cobra"
)

func setupCmd() *cobra.Command {
	var skipPath bool
	c := &cobra.Command{
		Use:   "setup",
		Short: "Global install: PATH, Cursor MCP (~/.cursor/mcp.json), skills",
		Long: "One-time machine setup so `codehelper` works from any directory. " +
			"Adds the install directory to your shell PATH (idempotent), merges MCP config, " +
			"and installs Cursor skills. Per-project indexing: run `codehelper init` inside each git repo.",
		RunE: func(cmd *cobra.Command, args []string) error {
			bin, err := os.Executable()
			if err != nil {
				return err
			}
			bin, err = filepath.Abs(bin)
			if err != nil {
				return err
			}
			if !skipPath {
				if changed, err := setup.EnsureUserPath(filepath.Dir(bin)); err != nil {
					fmt.Fprintln(os.Stderr, "setup: PATH:", err)
				} else if changed {
					fmt.Fprintln(os.Stderr, "setup: added", filepath.Dir(bin), "to your shell PATH — open a new terminal")
				}
			}
			if pruned, err := setup.CursorGlobal(); err != nil {
				return err
			} else if pruned {
				fmt.Fprintln(os.Stderr, "setup: removed stray global Cursor MCP entry (codehelper is per-project) — reload Cursor to drop the duplicate")
			}
			autoEnsureWatchDaemon("", "")
			autoEnsureCodehelperGitignore("")
			if os.Getenv("SKIP_BROWSER_INSTALL") != "1" {
				setup.RunExtras(filepath.Dir(bin))
			}
			fmt.Fprintln(os.Stderr, "setup: global ready — run `codehelper init` inside any git project")
			return nil
		},
	}
	c.Flags().BoolVar(&skipPath, "skip-path", false, "do not modify shell PATH")
	return c
}
