package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/patterns"
	"github.com/spf13/cobra"
)

func expandRequestCmd() *cobra.Command {
	var req, projectType, changedArea string
	var asJSON bool
	c := &cobra.Command{
		Use:   "expand-request",
		Short: "Deterministic requirement expansion from pattern packs (stdin JSON or flags)",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			in := patterns.ExpandInput{
				Request:     strings.TrimSpace(req),
				ProjectType: strings.TrimSpace(projectType),
				ChangedArea: strings.TrimSpace(changedArea),
			}
			if in.Request == "" {
				dec := json.NewDecoder(os.Stdin)
				dec.DisallowUnknownFields()
				var stdin patterns.ExpandInput
				if err := dec.Decode(&stdin); err == nil && strings.TrimSpace(stdin.Request) != "" {
					in = stdin
				}
			}
			if strings.TrimSpace(in.Request) == "" {
				return fmt.Errorf("provide --request or stdin JSON with request field")
			}
			packs, err := patterns.LoadAll(root)
			if err != nil {
				return err
			}
			out := patterns.ExpandRequest(in, packs)
			if asJSON {
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("intent=%s feature=%s pattern=%s\n", out.Intent, out.FeatureType, out.PatternID)
			for _, r := range out.InferredRequirements {
				fmt.Println("-", r)
			}
			return nil
		},
	}
	c.Flags().StringVar(&req, "request", "", "user request text")
	c.Flags().StringVar(&projectType, "project-type", "", "e.g. wordpress_woocommerce")
	c.Flags().StringVar(&changedArea, "changed-area", "", "frontend|backend|fullstack")
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return c
}
