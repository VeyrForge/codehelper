package eval

import (
	"context"
	"fmt"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/registry"
)

func (r *Runner) ensureFreshIndex(ctx context.Context, repo registry.Entry) (string, error) {
	mode := r.Config.IndexMode
	if !r.RefreshIndex && mode == IndexAuto {
		mode = IndexSkip
	}
	switch mode {
	case IndexSkip:
		return "skipped", nil
	case IndexForce:
		if err := indexer.Run(ctx, repo.RootPath, indexer.Options{RepoName: repo.Name, Force: true}); err != nil {
			return "", fmt.Errorf("analyze %s: %w", repo.Name, err)
		}
		return "forced", nil
	default:
		fresh := freshness.Inspect(repo.RootPath)
		force := fresh.Stale || fresh.IndexLag == "possible"
		if err := indexer.Run(ctx, repo.RootPath, indexer.Options{RepoName: repo.Name, Force: force}); err != nil {
			return "", fmt.Errorf("analyze %s: %w", repo.Name, err)
		}
		if force {
			return "refreshed", nil
		}
		return "incremental_ok", nil
	}
}
