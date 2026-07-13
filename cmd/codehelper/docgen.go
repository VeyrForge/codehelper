package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/selfdoc"
	"github.com/spf13/cobra"
)

func docgenCmd() *cobra.Command {
	var (
		repoPath     string
		outDir       string
		maxCallers   int
		includeTests bool
	)
	c := &cobra.Command{
		Use:   "docgen [path]",
		Short: "Generate human-readable per-package API docs from the index (deterministic, no LLM)",
		Long: "Renders one markdown file per package (plus an index) documenting the public/\n" +
			"exported API surface of your project, straight from the code graph. Bounded in\n" +
			"size (public symbols only), commit-friendly (stable ordering, only changed files\n" +
			"rewritten), and fully local — no LLM. Reuses the existing index (run `analyze`\n" +
			"first). Add it to a git hook or `watch` to keep docs fresh on every change.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := repoPath
			if len(args) > 0 {
				root = args[0]
			}
			if root == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				root = wd
			}
			root, _ = filepath.Abs(root)

			m, err := meta.Read(root)
			if err != nil {
				return fmt.Errorf("no index found (run `codehelper analyze` first): %w", err)
			}
			st, err := graph.Open(paths.DBPath(root))
			if err != nil {
				return err
			}
			defer st.Close()

			opts := selfdoc.Options{MaxCallers: maxCallers, IncludeTests: includeTests}
			if outDir != "" {
				if !filepath.IsAbs(outDir) {
					outDir = filepath.Join(root, outDir)
				}
				opts.OutDir = outDir
			}

			res, err := selfdoc.Generate(cmd.Context(), st, m.RepoName, root, opts)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, res.OutDir)
			if rel == "" {
				rel = res.OutDir
			}
			fmt.Printf("docs written to %s/\n", rel)
			fmt.Printf("  packages: %d   public symbols: %d\n", res.Packages, res.Symbols)
			fmt.Printf("  files: %d updated, %d unchanged\n", res.FilesWritten, res.FilesUnchanged)
			fmt.Printf("  index: %s\n", filepath.Join(rel, "README.md"))
			return nil
		},
	}
	c.Flags().StringVar(&repoPath, "path", "", "project root (default: current directory)")
	c.Flags().StringVar(&outDir, "out", "docs/api", "output directory for generated markdown (relative to project root)")
	c.Flags().IntVar(&maxCallers, "max-callers", 8, "max in-repo callers listed per symbol")
	c.Flags().BoolVar(&includeTests, "include-tests", false, "include test files in the documentation")
	return c
}
