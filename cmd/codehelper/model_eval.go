package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/modeleval"
	"github.com/spf13/cobra"
)

func modelEvalCmd() *cobra.Command {
	var suitePath string
	var model string
	var asJSON bool
	c := &cobra.Command{
		Use:   "model-eval",
		Short: "Run local model-eval suite (set CODEHELPER_MODEL_EVAL_CMD template)",
		Long:  modelEvalLongHelp,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(suitePath) == "" {
				return fmt.Errorf("--suite path required")
			}
			f, err := os.Open(suitePath)
			if err != nil {
				return err
			}
			defer f.Close()
			suite, err := modeleval.LoadSuite(f)
			if err != nil {
				return err
			}
			if strings.TrimSpace(model) == "" {
				model = "local"
			}
			ctx := context.Background()
			res, err := modeleval.RunFromEnv(ctx, model, suite)
			if err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(res, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("model=%s passed=%d failed=%d\n", res.Model, res.Passed, res.Failed)
			for _, o := range res.Results {
				st := "FAIL"
				if o.Pass {
					st = "OK"
				}
				fmt.Printf("[%s] %s %s\n", st, o.Name, o.Detail)
			}
			if res.Failed > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
	c.Flags().StringVar(&suitePath, "suite", "", "path to model-eval suite JSON")
	c.Flags().StringVar(&model, "model", "", "model label substituted into CODEHELPER_MODEL_EVAL_CMD")
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON result")
	return c
}
