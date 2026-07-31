package main

import (
	"os"

	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/spf13/cobra"
)

func cleanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clean [path]",
		Short: "Remove .codehelper index directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			dir := paths.RepoIndexDir(root)
			return os.RemoveAll(dir)
		},
	}
}
