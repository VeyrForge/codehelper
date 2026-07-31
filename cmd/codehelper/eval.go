package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/eval"
	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/spf13/cobra"
)

func evalCmd() *cobra.Command {
	var asJSON bool
	var suitePath string
	var repoFlag string
	var golden bool
	c := &cobra.Command{
		Use:   "eval [path]",
		Short: "Run retrieval + intake-prompt eval suite (CI gate)",
		Long:  evalLongHelp,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			abs, err := filepath.Abs(root)
			if err != nil {
				return err
			}

			_, indexRoot, perr := indexer.ResolveIndexPaths(abs, "")
			if perr != nil {
				indexRoot = abs
			}

			repoName := strings.TrimSpace(repoFlag)
			if repoName == "" {
				if reg, err := registry.Load(); err == nil {
					if name, err := reg.ResolveNameInWorkspace("", indexRoot); err == nil {
						repoName = name
					}
				}
			}
			if repoName == "" {
				repoName = filepath.Base(indexRoot)
			}

			suite := eval.Default()
			if golden {
				suite, err = eval.Golden()
				if err != nil {
					return err
				}
			}
			if strings.TrimSpace(suitePath) != "" {
				f, err := os.Open(suitePath)
				if err != nil {
					return err
				}
				defer f.Close()
				suite, err = eval.LoadSuite(f)
				if err != nil {
					return err
				}
			}

			ctx := context.Background()
			res, err := eval.Run(ctx, indexRoot, repoName, suite, nil)
			if err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(res, "", "  ")
				fmt.Println(string(b))
			} else {
				fmt.Printf("eval summary: total=%d passed=%d failed=%d\n", res.Total, res.Passed, res.Failed)
				for _, cs := range res.Cases {
					marker := "PASS"
					if !cs.Pass {
						marker = "FAIL"
					}
					line := fmt.Sprintf("  %s %s", marker, cs.Name)
					if cs.Detail != "" {
						line += " -- " + cs.Detail
					}
					fmt.Println(line)
				}
			}
			if res.Failed > 0 {
				os.Exit(2)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable result")
	c.Flags().StringVar(&suitePath, "suite", "", "path to JSON suite (defaults to bundled smoke suite)")
	c.Flags().StringVar(&repoFlag, "repo", "", "repository name override (defaults to single registered repo)")
	c.Flags().BoolVar(&golden, "golden", false, "run extended golden retrieval benchmark suite")
	return c
}
