package plan

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/taskstore"
)

// IntelSnapshot is repo intelligence gathered before plan creation.
type IntelSnapshot struct {
	Understanding         string
	ExistingCode          []taskstore.CodeRef
	ReuseCandidates       []string
	ImpactTier            string
	ImplementationOptions []taskstore.PlanOption
	RecommendedOption     string
	IndexAvailable        bool
}

func gatherIntel(ctx context.Context, root, request string) IntelSnapshot {
	out := IntelSnapshot{
		ImpactTier:     "unknown",
		IndexAvailable: false,
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return out
	}
	st, err := graph.Open(paths.DBPath(root))
	if err != nil {
		out.Understanding = "Index unavailable — run codehelper analyze before trusting symbol search."
		return out
	}
	defer st.Close()

	repoID := filepath.Base(root)
	if m, err := meta.Read(root); err == nil && strings.TrimSpace(m.RepoName) != "" {
		repoID = m.RepoName
	}

	hits, err := retrieval.QueryHybrid(ctx, st, repoID, request, 12)
	if err != nil || len(hits) == 0 {
		out.Understanding = "No indexed symbols matched the request — expand search terms or refresh the index."
		return out
	}
	out.IndexAvailable = true

	seenPath := map[string]struct{}{}
	for _, h := range hits {
		p := strings.TrimSpace(h.Symbol.Path)
		if p == "" {
			continue
		}
		if _, ok := seenPath[p]; ok {
			continue
		}
		seenPath[p] = struct{}{}
		reason := ""
		if len(h.Reasons) > 0 {
			reason = h.Reasons[0]
		}
		out.ExistingCode = append(out.ExistingCode, taskstore.CodeRef{
			Path: p, Symbol: h.Symbol.Name, Reason: reason,
		})
		if h.Symbol.Name != "" {
			out.ReuseCandidates = append(out.ReuseCandidates, h.Symbol.Name)
		}
		if len(out.ExistingCode) >= 8 {
			break
		}
	}

	top := hits[0]
	out.Understanding = fmt.Sprintf("Top match: %s in %s (score %.2f).", top.Symbol.Name, top.Symbol.Path, top.Score)
	if bun, err := retrieval.BuildContext(ctx, st, repoID, top.Symbol.Name); err == nil && bun != nil {
		if len(bun.Callers) > 0 {
			out.Understanding += fmt.Sprintf(" %d caller(s) in graph.", len(bun.Callers))
		}
	}

	if top.Symbol.Name != "" {
		if imp, err := mcpimpact.Analyze(ctx, st, repoID, top.Symbol.Name, 2, "downstream"); err == nil && imp != nil {
			out.ImpactTier = strings.TrimSpace(imp.RiskTier)
			if out.ImpactTier == "" {
				out.ImpactTier = "low"
			}
			if len(imp.Nodes) > 8 {
				out.ImpactTier = "medium"
			}
			if len(imp.Nodes) > 20 {
				out.ImpactTier = "high"
			}
		}
	}

	out.ImplementationOptions = []taskstore.PlanOption{
		{
			ID: "reuse", Title: "Extend existing code",
			Description: "Modify or extend symbols already in the repo.",
			Pros:        []string{"Smallest diff", "Preserves conventions"},
			Cons:        []string{"May require refactoring if symbols are tangled"},
		},
		{
			ID: "new", Title: "Add new module",
			Description: "Create new helpers/services when reuse is unsuitable.",
			Pros:        []string{"Clear separation"},
			Cons:        []string{"Risk of duplication", "More files to maintain"},
		},
	}
	if len(out.ReuseCandidates) > 0 {
		out.RecommendedOption = "reuse"
	} else {
		out.RecommendedOption = "new"
	}

	return out
}
