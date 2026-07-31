package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func rulesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "rules",
		Short: "Manage framework review rule packs",
	}
	c.AddCommand(rulesInstallCmd())
	return c
}

func rulesInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install [pack]",
		Short: "Install framework-specific review rule pack",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pack := args[0]
			src := filepath.Join("internal", "rules", "packs", pack+".json")
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(".codehelper"), 0o755); err != nil {
				return err
			}
			dst := filepath.Join(".codehelper", "rules-"+pack+".json")
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return err
			}
			fmt.Println("installed", dst)
			return nil
		},
	}
}
