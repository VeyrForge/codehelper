package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/patterns"
	"github.com/spf13/cobra"
)

func featurePatternsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "patterns",
		Short: "Install bundled feature pattern packs into .codehelper/patterns/",
	}
	c.AddCommand(featurePatternsInstallCmd())
	return c
}

func featurePatternsInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install [pack|all]",
		Short: "Copy bundled pattern JSON (e.g. modal_form or all)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := strings.ToLower(strings.TrimSpace(args[0]))
			dir := filepath.Join(".codehelper", "patterns")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if arg == "all" {
				names := []string{"modal_form"}
				for _, name := range names {
					if err := installOnePattern(dir, name); err != nil {
						return err
					}
				}
				fmt.Println("installed packs into", dir)
				return nil
			}
			if err := installOnePattern(dir, arg); err != nil {
				return err
			}
			fmt.Println("installed", filepath.Join(dir, strings.TrimSuffix(arg, ".json")+".json"))
			return nil
		},
	}
}

func installOnePattern(dir, name string) error {
	b, err := patterns.BundledPackJSON(name)
	if err != nil {
		return err
	}
	base := strings.TrimSuffix(strings.TrimSpace(name), ".json") + ".json"
	return os.WriteFile(filepath.Join(dir, base), b, 0o644)
}
