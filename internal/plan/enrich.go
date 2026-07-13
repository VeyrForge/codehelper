package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/agent"
	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/taskstore"
)

// EnrichConfig controls optional LLM enrichment after deterministic Build.
type EnrichConfig struct {
	LLM       llm.Config
	Tools     agent.ToolCaller
	EnrichLLM bool
}

// BuildEnriched runs Build then optionally enriches narrative fields via one Plan-mode agent turn.
func BuildEnriched(ctx context.Context, in Input, cfg EnrichConfig) (Output, error) {
	out, err := Build(in)
	if err != nil {
		return out, err
	}
	if in.Quick || !cfg.EnrichLLM || !cfg.LLM.Ready() || cfg.Tools == nil {
		return out, nil
	}
	enriched, err := enrichWithLLM(ctx, in, out, cfg)
	if err != nil {
		return out, nil
	}
	return enriched, nil
}

func enrichWithLLM(ctx context.Context, in Input, base Output, cfg EnrichConfig) (Output, error) {
	skJSON, err := json.MarshalIndent(PersistPayload{Plan: base.Plan, Todos: base.Todos}, "", "  ")
	if err != nil {
		return base, err
	}
	prompt := fmt.Sprintf(`Enrich this deterministic plan skeleton for the user request. Use read-only MCP tools if needed to verify assumptions.

User request: %s

Skeleton JSON:
%s

Reply with ONLY a fenced `+"```json"+` block containing {"plan":{...},"todos":[...]}.
Preserve existing_code_found, reuse_candidates, impact_tier, expand_request, freshness, and project_profile from the skeleton.
Fill narrative fields: current_understanding, assumptions, implementation_options, recommended_option, done_criteria, and rich todo descriptions with verification commands.`, in.Request, string(skJSON))

	res, err := agent.Run(ctx, agent.Options{
		Mode:          agent.ModePlan,
		UserText:      prompt,
		LLM:           cfg.LLM,
		Tools:         cfg.Tools,
		WorkspaceRoot: in.RepoRoot,
		MaxToolRounds: 12,
	})
	if err != nil || res == nil || strings.TrimSpace(res.Text) == "" {
		return base, err
	}
	parsed, err := ParsePersistPayload(res.Text)
	if err != nil {
		return base, err
	}
	return mergeEnriched(base, parsed), nil
}

func mergeEnriched(base Output, enriched PersistPayload) Output {
	p := enriched.Plan
	if s := strings.TrimSpace(p.CurrentUnderstanding); s != "" {
		base.Plan.CurrentUnderstanding = s
	}
	if len(p.Assumptions) > 0 {
		base.Plan.Assumptions = dedupeStrings(append(base.Plan.Assumptions, p.Assumptions...))
	}
	if len(p.ImplementationOptions) > 0 {
		base.Plan.ImplementationOptions = p.ImplementationOptions
	}
	if s := strings.TrimSpace(p.RecommendedOption); s != "" {
		base.Plan.RecommendedOption = s
	}
	if len(p.DoneCriteria) > 0 {
		base.Plan.DoneCriteria = p.DoneCriteria
	}
	if p.ResearchSummary != nil && strings.TrimSpace(p.ResearchSummary.Needed) != "" {
		base.Plan.ResearchSummary = p.ResearchSummary
	}

	if len(enriched.Todos) > 0 {
		for i := range enriched.Todos {
			if strings.TrimSpace(enriched.Todos[i].Status) == "" {
				enriched.Todos[i].Status = taskstore.TodoPlanned
			}
			if enriched.Todos[i].ID == "" {
				enriched.Todos[i].ID = fmt.Sprintf("todo-%d", i+1)
			}
		}
		base.Todos = enriched.Todos
	}
	return base
}
