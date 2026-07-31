package review

import (
	"context"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
)

type BlastRadius struct {
	BlastRadius    string `json:"blast_radius"`
	SafeToAutoEdit bool   `json:"safe_to_auto_edit"`
	Reason         string `json:"reason"`
}

func BuildBlastRadius(ctx context.Context, st *graph.Store, repoName, target string) (*BlastRadius, error) {
	res, err := mcpimpact.Analyze(ctx, st, repoName, target, 3, "downstream")
	if err != nil {
		return nil, err
	}
	size := len(res.Nodes)
	out := &BlastRadius{
		BlastRadius:    "small",
		SafeToAutoEdit: true,
		Reason:         "Limited downstream impact.",
	}
	if size > 20 {
		out.BlastRadius = "large"
		out.SafeToAutoEdit = false
		out.Reason = "Touches many downstream callers."
	}
	return out, nil
}
