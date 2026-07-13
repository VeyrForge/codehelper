package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/spf13/cobra"
)

func projectsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "projects",
		Short: "List indexed projects on this machine",
		Long: "Shows every project registered in ~/.codehelper/registry.json and whether " +
			"each has an initialized index. MCP/agent tools only expose the **current** " +
			"workspace project; use `codehelper init` in a repo before using tools there.",
	}
	c.AddCommand(projectsListCmd(), projectsCurrentCmd(), projectsForgetCmd(), projectsPruneCmd())
	return c
}

// projectsForgetCmd deregisters named projects from the registry (does NOT touch
// the repo on disk; only removes the registry entry, and optionally its index dir).
func projectsForgetCmd() *cobra.Command {
	var purgeIndex bool
	c := &cobra.Command{
		Use:   "forget <name|path> [more...]",
		Short: "Remove projects from the registry (keep the repo; optionally delete its index)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			removed := 0
			for _, a := range args {
				name := a
				// allow passing a path: match it to a registered entry by root_path
				if filepath.IsAbs(a) {
					ap, _ := filepath.Abs(a)
					for _, e := range reg.List() {
						if filepath.Clean(e.RootPath) == filepath.Clean(ap) {
							name = e.Name
							break
						}
					}
				}
				e, ok := reg.Get(name)
				if !ok {
					fmt.Printf("  skip %-20s (not registered)\n", name)
					continue
				}
				reg.Remove(name)
				removed++
				fmt.Printf("  forgot %-18s %s\n", name, e.RootPath)
				if purgeIndex && e.RootPath != "" {
					idx := filepath.Join(e.RootPath, ".codehelper")
					if err := os.RemoveAll(idx); err == nil {
						fmt.Printf("         deleted index %s\n", idx)
					}
				}
			}
			if removed == 0 {
				return nil
			}
			return reg.Save()
		},
	}
	c.Flags().BoolVar(&purgeIndex, "purge-index", false, "also delete each project's .codehelper index directory")
	return c
}

// projectsPruneCmd removes stale/foreign registry entries automatically: any whose
// root_path no longer exists, lives under a .testbeds fixtures dir, or sits in a
// temp/scratch location. This is the "keep only my real projects" cleanup.
func projectsPruneCmd() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "prune",
		Short: "Drop registry entries that are missing, test fixtures, or in temp dirs",
		Long: "Removes registry entries whose root_path is gone, lives under a " +
			"`.testbeds/` fixtures directory, or sits in /tmp scratch space — leaving " +
			"only real projects. Repos on disk are never touched. Use --dry-run to preview.",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			var drop []registry.Entry
			for _, e := range reg.List() {
				if isStaleEntry(e.RootPath) {
					drop = append(drop, e)
				}
			}
			sort.Slice(drop, func(i, j int) bool { return drop[i].Name < drop[j].Name })
			if len(drop) == 0 {
				fmt.Println("Nothing to prune — registry only contains live projects.")
				return nil
			}
			fmt.Printf("%s %d stale/foreign entr%s:\n", map[bool]string{true: "Would prune", false: "Pruning"}[dryRun], len(drop), map[bool]string{true: "y", false: "ies"}[len(drop) == 1])
			for _, e := range drop {
				reason := pruneReason(e.RootPath)
				fmt.Printf("  - %-18s %s  [%s]\n", e.Name, e.RootPath, reason)
				if !dryRun {
					reg.Remove(e.Name)
				}
			}
			if dryRun {
				fmt.Println("\n(dry run — nothing changed; re-run without --dry-run to apply)")
				return nil
			}
			if err := reg.Save(); err != nil {
				return err
			}
			fmt.Printf("\nDone. %d real project(s) remain.\n", len(reg.List()))
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be pruned without changing anything")
	return c
}

func isStaleEntry(root string) bool { return pruneReason(root) != "" }

// pruneReason returns a short label if the path is not a real project root, else "".
func pruneReason(root string) string {
	if root == "" {
		return "no path"
	}
	if _, err := os.Stat(root); err != nil {
		return "missing"
	}
	clean := filepath.ToSlash(filepath.Clean(root))
	switch {
	case strings.Contains(clean, "/.testbeds/"):
		return "test fixture"
	case strings.HasPrefix(clean, "/tmp/"), strings.Contains(clean, "/scratchpad/"):
		return "temp/scratch"
	}
	return ""
}

func projectsListCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List all registered projects and initialization status",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			list := reg.ListProjectSummaries()
			if asJSON {
				b, _ := json.MarshalIndent(map[string]any{"projects": list, "count": len(list)}, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			if len(list) == 0 {
				fmt.Println("No projects registered. Run `codehelper init` inside a git repo.")
				return nil
			}
			fmt.Printf("%d registered project(s):\n", len(list))
			for _, p := range list {
				state := "not initialized"
				if p.Initialized {
					state = "initialized (" + p.IndexStatus + ")"
				}
				fmt.Printf("  %-20s %s  [%s]\n", p.Name, p.RootPath, state)
			}
			fmt.Println("\nTools (MCP, agent, serve) only access the current workspace — not other projects.")
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON")
	return c
}

func projectsCurrentCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "current [path]",
		Short: "Show whether a directory's project is initialized",
		Args:  cobra.MaximumNArgs(1),
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
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			e, inReg := reg.EntryForWorkspace(root)
			initOK, indexStatus := registry.InitStatus(root)
			out := map[string]any{
				"path":          root,
				"initialized":   initOK,
				"index_status":  indexStatus,
				"in_registry":   inReg,
				"registry_name": "",
				"registry_root": "",
			}
			if inReg {
				out["registry_name"] = e.Name
				out["registry_root"] = e.RootPath
			}
			if !initOK {
				out["hint"] = "run `codehelper init` in this git repo"
			}
			if asJSON {
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			if !inReg && !initOK {
				fmt.Printf("%s: not registered and not initialized\n", root)
				fmt.Println("Run `codehelper init` in a git repository root.")
				return nil
			}
			if inReg {
				fmt.Printf("registry: %s (%s)\n", e.Name, e.RootPath)
			}
			if initOK {
				fmt.Printf("initialized: yes (index %s)\n", indexStatus)
			} else {
				fmt.Printf("initialized: no (index %s)\n", indexStatus)
				fmt.Println("Run `codehelper init` to index this project.")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON")
	return c
}
