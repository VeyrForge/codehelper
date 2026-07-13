package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/spf13/cobra"
)

func profileCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "profile",
		Short: "Generate .codehelper/project_profile.json for the current repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			p, err := profile.Write(root)
			if err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(p, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Println("wrote", profile.Path(root))
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print profile JSON to stdout")
	return c
}
