package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/VeyrForge/codehelper/internal/daemon"
	"github.com/VeyrForge/codehelper/internal/hints"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/setup"
	"github.com/spf13/cobra"
)

// repairAllProjects makes every registered project consistent with the current
// binary, so updating codehelper never silently leaves a project on stale rules,
// config, or index schema. For each initialized project it (idempotently):
//   - rewrites the per-client tool-first rules (CLAUDE.md block + .cursor/rules)
//     and refreshes the MCP config + Claude Code approval;
//   - re-indexes IF the schema/parser version changed (analyze decides — a no-op
//     for up-to-date projects), so a parser change (e.g. new doc-comment indexing)
//     propagates everywhere without a manual per-project re-init;
//   - restarts a running watch daemon so it runs the new binary (a stale daemon
//     would otherwise re-index with old code on the next file change).
//
// Safe to run anytime; never starts a daemon for a project that didn't have one.
func repairAllProjects() {
	reg, err := registry.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "repair: load registry:", err)
		return
	}
	hints.EnsureBuiltin()
	projects := reg.ListProjectSummaries()
	repaired, reindexed, skipped, pruned := 0, 0, 0, 0
	for _, p := range projects {
		root := p.RootPath
		// A deleted project shouldn't linger in the registry forever. If its root
		// directory is gone, prune the entry instead of skipping it every repair.
		// Only prune on a definitive "does not exist" — a permission/transient error
		// is skipped, not deleted.
		if root == "" {
			reg.Remove(p.Name)
			fmt.Fprintf(os.Stderr, "repair: %s — empty path, pruned from registry\n", p.Name)
			pruned++
			continue
		}
		if _, statErr := os.Stat(root); statErr != nil {
			if os.IsNotExist(statErr) {
				reg.Remove(p.Name)
				fmt.Fprintf(os.Stderr, "repair: %s — path deleted, pruned from registry (%s)\n", p.Name, root)
				pruned++
			} else {
				fmt.Fprintf(os.Stderr, "repair: %s — path unreadable, skipping (%v)\n", p.Name, statErr)
				skipped++
			}
			continue
		}
		if !p.Initialized {
			skipped++
			continue
		}
		// Cheap, idempotent: rules + MCP config + Claude approval.
		if err := setup.WriteClientRules(root); err != nil {
			fmt.Fprintf(os.Stderr, "repair: %s — client rules: %v\n", p.Name, err)
		}
		if _, err := setup.ProjectMCP(root, setup.ResolveBinary()); err != nil {
			fmt.Fprintf(os.Stderr, "repair: %s — MCP config: %v\n", p.Name, err)
		}
		_, _ = setup.ClaudeCodeEnable(root, "codehelper")

		// Restart a running daemon so the reindex (and future indexing) uses the new
		// binary; reanalyze reindexes only if the schema/parser version changed.
		wasRunning := daemonRunning(root)
		if wasRunning {
			_ = stopDaemon(root)
		}
		preReindexNeeded := p.SchemaVersion != 0 // best-effort signal for messaging
		if err := reanalyzeAfterUpdate(root, false); err != nil {
			fmt.Fprintf(os.Stderr, "repair: %s — reanalyze: %v\n", p.Name, err)
		} else if preReindexNeeded {
			reindexed++
		}
		if wasRunning {
			autoEnsureWatchDaemon(root, "")
		}
		repaired++
		fmt.Fprintf(os.Stderr, "repair: %s ✓%s\n", p.Name, map[bool]string{true: " (daemon restarted)", false: ""}[wasRunning])
	}
	if pruned > 0 {
		if err := reg.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "repair: save registry after prune:", err)
		}
	}
	// Editors keep a long-lived MCP server alive; kill any started before this
	// (new) binary so they respawn fresh instead of serving stale code forever.
	if n := terminateStaleMCPServers(); n > 0 {
		fmt.Fprintf(os.Stderr, "repair: terminated %d stale MCP server(s) — editors will respawn the new binary on next tool call\n", n)
	}
	fmt.Fprintf(os.Stderr, "repair: %d project(s) repaired, %d pruned (deleted), %d skipped (uninitialized)\n", repaired, pruned, skipped)
}

// daemonRunning reports whether a watch daemon currently owns this project.
func daemonRunning(indexRoot string) bool {
	st, err := daemon.ReadState(indexRoot)
	if err != nil || st == nil || st.PID <= 0 {
		return false
	}
	// The lock is the source of truth: if Acquire fails as already-running, a
	// daemon is alive; if it succeeds, the state file is stale.
	lock, lerr := daemon.Acquire(indexRoot)
	if lerr == nil {
		_ = lock.Release()
		return false
	}
	var already daemon.ErrAlreadyRunning
	return errors.As(lerr, &already) && already.PID > 0
}

func repairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "repair",
		Short: "Make every registered project consistent with the current binary (rules, MCP config, index schema)",
		Long: "Sweeps all registered projects and re-applies the per-client tool-first rules " +
			"(CLAUDE.md + .cursor/rules), refreshes MCP config, re-indexes any project whose schema/parser " +
			"version changed, and restarts running watch daemons on the new binary. Run after updating codehelper " +
			"so every project keeps working — `update`/`upgrade` call this automatically.",
		RunE: func(cmd *cobra.Command, args []string) error {
			repairAllProjects()
			return nil
		},
	}
}
