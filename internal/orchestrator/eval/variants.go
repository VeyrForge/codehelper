package eval

import (
	"context"
	"fmt"
	"time"

	"github.com/VeyrForge/codehelper/internal/registry"
)

// VariantsReport holds multiple benchmark runs (format/index permutations).
type VariantsReport struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Variants    []VariantResult `json:"variants"`
	Analysis    VariantAnalysis `json:"analysis"`
}

// VariantResult is one named benchmark run.
type VariantResult struct {
	Name   string `json:"name"`
	Config Config `json:"config"`
	Report Report `json:"report"`
}

// VariantAnalysis summarizes cross-variant deltas.
type VariantAnalysis struct {
	IndexImpact  []string `json:"index_impact,omitempty"`
	FormatImpact []string `json:"format_impact,omitempty"`
	QualityNotes []string `json:"quality_notes,omitempty"`
}

// RunAllVariants executes each named variant sequentially.
func (r *Runner) RunAllVariants(ctx context.Context, reg *registry.Registry, variants []NamedConfig) (VariantsReport, error) {
	vr := VariantsReport{GeneratedAt: time.Now().UTC()}
	for _, v := range variants {
		runner := *r
		runner.Config = v.Config
		runner.RefreshIndex = v.Config.IndexMode != IndexSkip
		rep, err := runner.RunAll(ctx, reg)
		if err != nil {
			return vr, err
		}
		vr.Variants = append(vr.Variants, VariantResult{Name: v.Name, Config: v.Config, Report: rep})
	}
	vr.Analysis = analyzeVariants(vr.Variants)
	return vr, nil
}

func analyzeVariants(variants []VariantResult) VariantAnalysis {
	var a VariantAnalysis
	byName := map[string]VariantResult{}
	for _, v := range variants {
		byName[v.Name] = v
	}
	if fresh, ok := byName["fresh_index_toon"]; ok {
		if skip, ok2 := byName["skip_index_toon"]; ok2 {
			dScore := fresh.Report.Overall.OrchestrateAvg - skip.Report.Overall.OrchestrateAvg
			dTok := fresh.Report.Overall.OrchestrateAgentTokens - skip.Report.Overall.OrchestrateAgentTokens
			a.IndexImpact = append(a.IndexImpact,
				fmt.Sprintf("fresh vs skip_index: orchestrate score %+.3f, agent tokens %+d", dScore, dTok))
		}
		if force, ok2 := byName["force_index_toon"]; ok2 {
			dScore := force.Report.Overall.OrchestrateAvg - fresh.Report.Overall.OrchestrateAvg
			a.IndexImpact = append(a.IndexImpact,
				fmt.Sprintf("force vs fresh_index: orchestrate score %+.3f", dScore))
		}
	}
	if toon, ok := byName["fresh_index_toon"]; ok {
		if jsonV, ok2 := byName["fresh_index_manual_json"]; ok2 {
			dScore := toon.Report.Overall.ManualAvg - jsonV.Report.Overall.ManualAvg
			dTok := jsonV.Report.Overall.ManualTokens - toon.Report.Overall.ManualTokens
			pct := 0
			if jsonV.Report.Overall.ManualTokens > 0 {
				pct = 100 * dTok / jsonV.Report.Overall.ManualTokens
			}
			if dTok >= 0 {
				a.FormatImpact = append(a.FormatImpact,
					fmt.Sprintf("manual toon vs json: score %+.3f, manual tokens −%d (~%d%%) with TOON", dScore, dTok, pct))
			} else {
				a.FormatImpact = append(a.FormatImpact,
					fmt.Sprintf("manual toon vs json: score %+.3f, json chain −%d tokens (~%d%%) smaller (multi-tool concat)", dScore, -dTok, -pct))
			}
		}
		if orchJSON, ok2 := byName["orch_response_json"]; ok2 {
			dAgent := orchJSON.Report.Overall.OrchestrateAgentTokens - toon.Report.Overall.OrchestrateAgentTokens
			if dAgent > 0 {
				a.FormatImpact = append(a.FormatImpact,
					fmt.Sprintf("orchestrate agent payload: TOON −%d tokens vs JSON", dAgent))
			} else {
				a.FormatImpact = append(a.FormatImpact,
					fmt.Sprintf("orchestrate agent payload: JSON −%d tokens vs TOON", -dAgent))
			}
		}
	}
	if len(variants) > 0 {
		best := variants[0]
		for _, v := range variants[1:] {
			if v.Report.Overall.OrchestrateAvg > best.Report.Overall.OrchestrateAvg {
				best = v
			}
		}
		a.QualityNotes = append(a.QualityNotes,
			fmt.Sprintf("best orchestrate quality: %s (avg %.3f)", best.Name, best.Report.Overall.OrchestrateAvg))
	}
	return a
}
