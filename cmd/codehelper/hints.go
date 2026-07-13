package main

import (
	"fmt"
	"os"

	"github.com/VeyrForge/codehelper/internal/hints"
	"github.com/spf13/cobra"
)

func hintsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hints",
		Short: "Global, cross-project learned hints (syncable agent memory)",
		Long: "Manage the global learned-hints store (~/.codehelper/learned_hints.json): rules an " +
			"agent discovered about a stack and should remember next time, keyed by framework/" +
			"language/dependency/project_type and applied to any matching project. The file is " +
			"local-first plain JSON — copy it, or use `export`/`import`, to sync across machines.",
	}
	c.AddCommand(hintsListCmd(), hintsAddCmd(), hintsRemoveCmd(), hintsExportCmd(), hintsImportCmd())
	return c
}

func hintsListCmd() *cobra.Command {
	var scopeType, scope string
	c := &cobra.Command{
		Use:   "list",
		Short: "List learned hints (optionally filtered by scope)",
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := hints.List(scopeType, scope)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("No hints yet. Add via the `hints` MCP tool or `codehelper hints add`.")
				return nil
			}
			for _, h := range list {
				sc := h.Scope
				if sc == "" {
					sc = "*"
				}
				fmt.Printf("  [%s] %s/%s: %s\n", h.ID, h.ScopeType, sc, h.Text)
			}
			return nil
		},
	}
	c.Flags().StringVar(&scopeType, "scope-type", "", "filter: framework|language|dependency|project_type|global")
	c.Flags().StringVar(&scope, "scope", "", "filter: scope value e.g. wordpress, go")
	return c
}

func hintsAddCmd() *cobra.Command {
	var scopeType, scope string
	c := &cobra.Command{
		Use:   "add <text>",
		Short: "Add a learned hint",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := args[0]
			h, err := hints.Add(scopeType, scope, text, "")
			if err != nil {
				return err
			}
			fmt.Printf("added [%s] %s/%s\n", h.ID, h.ScopeType, h.Scope)
			return nil
		},
	}
	c.Flags().StringVar(&scopeType, "scope-type", "global", "framework|language|dependency|project_type|global")
	c.Flags().StringVar(&scope, "scope", "", "scope value e.g. wordpress, go, tailwindcss")
	return c
}

func hintsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove a learned hint by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ok, err := hints.Remove(args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no hint with id %q", args[0])
			}
			fmt.Println("removed")
			return nil
		},
	}
}

func hintsExportCmd() *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "export",
		Short: "Export all hints as JSON (to stdout or a file) for transfer to another machine",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := hints.Export()
			if err != nil {
				return err
			}
			if out == "" {
				fmt.Println(string(data))
				return nil
			}
			if err := os.WriteFile(out, data, 0o644); err != nil {
				return err
			}
			fmt.Printf("exported to %s\n", out)
			return nil
		},
	}
	c.Flags().StringVarP(&out, "out", "o", "", "write to this file instead of stdout")
	return c
}

func hintsImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import <file>",
		Short: "Merge hints from an exported JSON file (deduped)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			added, err := hints.ImportMerge(data)
			if err != nil {
				return err
			}
			fmt.Printf("imported %d new hint(s)\n", added)
			return nil
		},
	}
}
