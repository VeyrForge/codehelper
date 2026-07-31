package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
)

type Budget struct {
	MaxFiles                 int    `json:"max_files"`
	MaxPublicContractChanges int    `json:"max_public_contract_changes"`
	MaxRisk                  string `json:"max_risk"`
	RequiresTests            bool   `json:"requires_tests"`
}

type ChangeBudgetResult struct {
	WithinBudget bool     `json:"within_budget"`
	Warnings     []string `json:"warnings,omitempty"`
}

func ChangeBudgetCheck(ctx context.Context, repoRoot, baseRef string, budget Budget) (*ChangeBudgetResult, error) {
	_ = ctx
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD~1"
	}
	files, err := gitutil.DiffAgainst(repoRoot, baseRef)
	if err != nil {
		return nil, err
	}
	out := &ChangeBudgetResult{WithinBudget: true}
	if budget.MaxFiles > 0 && len(files) > budget.MaxFiles {
		out.WithinBudget = false
		out.Warnings = append(out.Warnings, fmt.Sprintf("Change exceeds budget: %d files changed, expected <= %d.", len(files), budget.MaxFiles))
	}
	return out, nil
}
