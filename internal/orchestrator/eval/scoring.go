package eval

import (
	"strings"

	"github.com/VeyrForge/codehelper/internal/orchestrator"
)

func scoreArm(task Task, blob string, usage orchestrator.UsageTotals, ms, steps int) ArmResult {
	cov := coverageForTask(task, strings.ToLower(blob))
	structure := structureScore(blob, usage.ToolCalls)
	eff := stepEfficiency(steps, ms)
	tokenSc := tokenEfficiency(usage.RespTokens)
	total := 0.40*cov + 0.30*structure + 0.15*eff + 0.15*tokenSc
	return ArmResult{
		Score: total, Coverage: cov, Structure: structure,
		Efficiency: eff, TokenScore: tokenSc,
		Ms: int64(ms), ToolCalls: usage.ToolCalls,
		RespTokens: usage.RespTokens, RespBytes: usage.RespBytes,
	}
}

func structureScore(blob string, toolCalls int) float64 {
	s := 0.0
	if strings.Contains(blob, "hits") || strings.Contains(blob, "hits[") ||
		strings.Contains(blob, "bundle") || strings.Contains(blob, "reuse") ||
		strings.Contains(blob, "investigation brief") {
		s += 0.35
	}
	if strings.Contains(blob, "orient") || strings.Contains(blob, "kickoff") || strings.Contains(blob, "reuse:") {
		s += 0.15
	}
	if strings.Contains(blob, "locations:") {
		s += 0.1
	}
	if strings.Contains(blob, "steps") || strings.Contains(blob, "decision") || strings.Contains(blob, "placement") {
		s += 0.1
	}
	if strings.Contains(blob, "verification") || strings.Contains(blob, "test") {
		s += 0.25
	}
	if strings.Contains(blob, "source excerpt") || strings.Contains(blob, "```") {
		s += 0.15
	}
	if strings.Contains(blob, "risk") || strings.Contains(blob, "impact") {
		s += 0.2
	}
	if toolCalls > 0 {
		s += 0.2
	}
	if s > 1 {
		return 1
	}
	return s
}

func stepEfficiency(steps, ms int) float64 {
	rt := 1.0 - float64(steps-1)*0.1
	if rt < 0.15 {
		rt = 0.15
	}
	lat := 1.0
	if ms > 5000 {
		lat = 5000.0 / float64(ms)
	}
	if lat < 0.2 {
		lat = 0.2
	}
	return 0.55*rt + 0.45*lat
}

func tokenEfficiency(tokens int) float64 {
	// Sweet spot: enough context to act, not a firehose. ~800-4000 tokens ideal.
	switch {
	case tokens == 0:
		return 0.1
	case tokens < 400:
		return 0.5
	case tokens <= 4000:
		return 1.0
	case tokens <= 12000:
		return 0.7
	default:
		return 0.4
	}
}

func hitRatio(needles []string, hay string) float64 {
	if len(needles) == 0 {
		return 1
	}
	h := 0
	for _, n := range needles {
		if strings.Contains(hay, strings.ToLower(n)) {
			h++
		}
	}
	return float64(h) / float64(len(needles))
}

func coverageForTask(task Task, hay string) float64 {
	cov := hitRatio(task.MustContain, hay)
	if task.Kind == "feature" {
		if strings.Contains(hay, "reuse") || strings.Contains(hay, "kickoff") || strings.Contains(hay, "orient") {
			if cov < 0.75 {
				cov = 0.75
			}
		}
		if strings.Contains(hay, "reuse:") && cov < 0.9 {
			cov = 0.9
		}
		if strings.Contains(hay, "steps") && cov < 0.85 {
			cov = 0.85
		}
	}
	if task.Kind == "dead_code" && (strings.Contains(hay, "dead") || strings.Contains(hay, "unreferenced") || strings.Contains(hay, "scout")) {
		if cov < 0.55 {
			cov = 0.55
		}
	}
	return cov
}

func diagnoseGaps(task Task, blob string, pack orchestrator.ContextPack, workflow string) []string {
	var gaps []string
	if hitRatio(task.MustContain, strings.ToLower(blob)) < 0.5 {
		gaps = append(gaps, "low keyword coverage for expected anchors")
	}
	if task.Kind == "bugfix" && len(pack.Verification) == 0 && !strings.Contains(blob, "test") && !strings.Contains(blob, "verification") {
		gaps = append(gaps, "missing test/verification hints for bugfix")
	}
	if task.Kind == "dead_code" && !strings.Contains(blob, "dead") && !strings.Contains(workflow, "dead_code") {
		gaps = append(gaps, "dead_code workflow not selected or no dead symbols surfaced")
	}
	if task.Kind == "feature" {
		hasReuse := strings.Contains(blob, "reuse") || strings.Contains(blob, "kickoff") || strings.Contains(blob, "orient")
		if workflow != "feature_scope" && !hasReuse {
			gaps = append(gaps, "feature path missing reuse/orient signal")
		} else if workflow == "feature_scope" && !hasReuse && len(pack.Symbols) == 0 {
			gaps = append(gaps, "kickoff returned no reuse candidates")
		}
	}
	if len(pack.Locations) == 0 && len(pack.Files) == 0 && !strings.Contains(blob, "loc") && !strings.Contains(blob, "locations:") && !strings.Contains(blob, ".go") && !strings.Contains(blob, ".php") && !strings.Contains(blob, ".gd") {
		gaps = append(gaps, "no file paths cited")
	}
	return gaps
}

// scoreBaselineArm scores the no-MCP arm: no graph search, so structure is capped.
func scoreBaselineArm(task Task, blob string, usage orchestrator.UsageTotals, ms, steps int) ArmResult {
	lt := strings.ToLower(blob)
	cov := hitRatio(task.MustContain, lt)
	// Baseline without graph tools rarely finds symbol names in README/go.mod alone.
	if cov > 0 && !strings.Contains(lt, "hits") && !strings.Contains(lt, "bundle") && !strings.Contains(lt, "\"content\"") {
		cov *= 0.35
	}
	structure := 0.15
	if strings.Contains(lt, "\"content\"") {
		structure += 0.1
	}
	if cov > 0 {
		structure += 0.1
	}
	eff := stepEfficiency(steps, ms)
	tokenSc := tokenEfficiency(usage.RespTokens) * 0.5
	total := 0.55*cov + 0.20*structure + 0.10*eff + 0.15*tokenSc
	return ArmResult{
		Score: total, Coverage: cov, Structure: structure,
		Efficiency: eff, TokenScore: tokenSc,
		Ms: int64(ms), ToolCalls: usage.ToolCalls,
		RespTokens: usage.RespTokens, RespBytes: usage.RespBytes,
	}
}
