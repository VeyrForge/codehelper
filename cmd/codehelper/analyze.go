package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/helpcatalog"
	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/spf13/cobra"
)

type analyzeFlags struct {
	force        bool
	name         string
	shard        string
	subdir       string
	invalidation string
	progressJSON bool
}

func runAnalyze(ctx context.Context, root string, f analyzeFlags) error {
	mode := indexer.InvalidationMode(strings.TrimSpace(strings.ToLower(f.invalidation)))
	if mode != "" && mode != indexer.InvalidationLazy && mode != indexer.InvalidationEager {
		return errInvalidInvalidation
	}
	var progOut io.Writer
	if f.progressJSON {
		progOut = os.Stderr
	}
	if err := indexer.Run(ctx, root, indexer.Options{
		Force:        f.force,
		RepoName:     f.name,
		ShardName:    f.shard,
		IndexSubdir:  f.subdir,
		Invalidation: mode,
		ProgressJSON: progOut,
	}); err != nil {
		return err
	}
	gitRoot, indexRoot, err := indexer.ResolveIndexPaths(root, f.subdir)
	if err != nil {
		return err
	}
	reg, err := registry.Load()
	if err != nil {
		return err
	}
	rn := strings.TrimSpace(f.name)
	if rn == "" {
		rn = strings.TrimSpace(f.shard)
	}
	if rn == "" {
		rn = filepath.Base(indexRoot)
	}
	cmt, _ := gitutil.HeadCommit(gitRoot)
	if err := reg.Upsert(rn, indexRoot, cmt, meta.SchemaVersion); err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}
	autoEnsureWatchDaemon(root, f.subdir)
	autoEnsureCodehelperGitignore(root)
	// Best-effort: keep a generated tool catalog under .codehelper/ for projects
	// that don't ship docs/MCP_TOOLS.md.
	_ = helpcatalog.WriteProjectReference(indexRoot)
	return nil
}

func analyzeCmd() *cobra.Command {
	var f analyzeFlags
	c := &cobra.Command{
		Use:   "analyze [path]",
		Short: "Index repository into knowledge graph",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			return runAnalyze(cmd.Context(), root, f)
		},
	}
	c.Flags().BoolVarP(&f.force, "force", "f", false, "force full re-index")
	c.Flags().StringVar(&f.name, "name", "", "repository name for registry")
	c.Flags().StringVar(&f.shard, "shard", "", "logical repo name when indexing a subdirectory (overrides default basename)")
	c.Flags().StringVar(&f.subdir, "path", "", "subdirectory under git root to index as its own workspace")
	c.Flags().StringVar(&f.invalidation, "invalidation", "eager", "transitive invalidation: eager|lazy")
	c.Flags().BoolVar(&f.progressJSON, "progress-json", false, "emit JSON progress lines on stderr (_ch_progress)")
	return c
}

var errInvalidInvalidation = fmt.Errorf("invalidation must be eager or lazy")
