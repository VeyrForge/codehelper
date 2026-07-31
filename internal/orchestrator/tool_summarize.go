package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
)

// toolSummary holds fields extracted from one MCP tool response for the
// orchestrator agent brief / next-step planner.
type toolSummary struct {
	summary, topSymbol            string
	symbols, files, risks, verify []string
}

// toolOutputSummarizer turns a parsed tool payload into a compact summary.
// tool is passed so shared handlers (e.g. diagnostics) can echo the tool name.
type toolOutputSummarizer func(tool string, parsed map[string]any, task string, entities []string) toolSummary

// toolOutputSummarizers is the data-driven dispatch table for summarizeToolOutput.
// Adding a tool: register a summarizer here instead of extending a switch.
var toolOutputSummarizers = map[string]toolOutputSummarizer{
	"query":           summarizeQueryToolOutput,
	"context":         summarizeContextToolOutput,
	"impact":          summarizeImpactToolOutput,
	"test_impact":     summarizeTestImpactToolOutput,
	"kickoff":         summarizeKickoffToolOutput,
	"investigate":     summarizeInvestigateToolOutput,
	"project_context": summarizeProjectContextToolOutput,
	"scout":           summarizeScoutToolOutput,
	"dead_code":       summarizeDeadCodeToolOutput,
	"detect_changes":  summarizeToolCompleteOutput,
	"review_diff":     summarizeToolCompleteOutput,
	"diagnostics":     summarizeToolCompleteOutput,
}

func summarizeToolOutput(tool, raw string, callErr error, task string, entities []string) (summary, topSymbol string, symbols, files, risks, verify []string) {
	if callErr != nil {
		return callErr.Error(), "", nil, nil, nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return truncate(raw, 300), "", nil, nil, nil, nil
	}
	fn, ok := toolOutputSummarizers[tool]
	if !ok {
		return tool + " done", "", nil, nil, nil, nil
	}
	out := fn(tool, parsed, task, entities)
	return out.summary, out.topSymbol, out.symbols, out.files, out.risks, out.verify
}

func summarizeToolCompleteOutput(tool string, _ map[string]any, _ string, _ []string) toolSummary {
	return toolSummary{summary: tool + " complete"}
}

func summarizeScoutToolOutput(_ string, _ map[string]any, _ string, _ []string) toolSummary {
	return toolSummary{summary: "Reuse candidates ranked"}
}

func summarizeQueryToolOutput(_ string, parsed map[string]any, task string, entities []string) toolSummary {
	var summary, topSymbol string
	var symbols, files, risks, verify []string

	if hits, ok := parsed["hits"].([]any); ok {
		ranked := parseRankedQueryHits(hits)
		if primary := pickPrimaryQueryHit(task, entities, ranked); primary.Name != "" {
			topSymbol = hitIdentity(primary)
		}
		names := make([]string, 0, len(ranked))
		for i, h := range ranked {
			if i >= 5 {
				break
			}
			names = append(names, h.Name)
			symbols = append(symbols, h.Name)
			if h.Loc != "" {
				files = append(files, strings.Split(h.Loc, ":")[0])
			}
		}
		// Surface the primary first in the symbol list for agent_brief.
		if topSymbol != "" {
			primName := topSymbol
			if strings.HasPrefix(primName, "sym:") {
				parts := strings.Split(primName, ":")
				primName = parts[len(parts)-1]
			}
			symbols = uniqueAppend([]string{primName}, symbols...)
		}
		if len(names) > 0 {
			summary = fmt.Sprintf("Found %d symbols: %s", len(hits), strings.Join(names, ", "))
		} else {
			summary = fmt.Sprintf("Found %d symbols", len(hits))
		}
	}
	return toolSummary{summary: summary, topSymbol: topSymbol, symbols: symbols, files: files, risks: risks, verify: verify}
}

func summarizeContextToolOutput(_ string, parsed map[string]any, task string, entities []string) toolSummary {
	var summary, topSymbol string
	var symbols, files, risks, verify []string

	if bundle, ok := parsed["bundle"].(map[string]any); ok {
		if sym, ok := bundle["symbol"].(map[string]any); ok {
			topSymbol, _ = sym["name"].(string)
			if topSymbol != "" {
				symbols = append(symbols, topSymbol)
			}
			if loc, _ := sym["loc"].(string); loc != "" {
				files = append(files, strings.Split(loc, ":")[0])
			}
		}
		nCallers := 0
		if callers, ok := bundle["callers"].([]any); ok {
			nCallers = len(callers)
			for i, c := range callers {
				if i >= 5 {
					break
				}
				if m, ok := c.(map[string]any); ok {
					n, _ := m["name"].(string)
					loc, _ := m["loc"].(string)
					if n != "" && (strings.HasPrefix(n, "Test") || strings.HasPrefix(n, "test")) {
						verify = append(verify, n)
					}
					if isTestishPath(loc) {
						p := strings.Split(loc, ":")[0]
						files = append(files, p)
						verify = append(verify, "go test "+p)
					}
				}
			}
		}
		if topSymbol != "" {
			summary = fmt.Sprintf("Context %s: %d callers", topSymbol, nCallers)
		} else {
			summary = fmt.Sprintf("Loaded symbol context (%d callers)", nCallers)
		}
	}
	if br, ok := parsed["blast_radius"].(map[string]any); ok {
		if tier, _ := br["risk_tier"].(string); tier != "" {
			risks = append(risks, "risk_tier="+tier)
		}
		if dep, ok := br["dependents"].(float64); ok && dep > 0 {
			risks = append(risks, fmt.Sprintf("dependents=%.0f", dep))
		}
	}
	return toolSummary{summary: summary, topSymbol: topSymbol, symbols: symbols, files: files, risks: risks, verify: verify}
}

func summarizeImpactToolOutput(_ string, parsed map[string]any, task string, entities []string) toolSummary {
	var summary, topSymbol string
	var symbols, files, risks, verify []string

	imp, ok := parsed["impact"].(map[string]any)
	if !ok {
		imp, _ = parsed["blast_radius"].(map[string]any)
	}
	if imp != nil {
		tier, _ := imp["risk_tier"].(string)
		if tier != "" {
			risks = append(risks, "impact risk="+tier)
		}
		if dep, ok := imp["dependents"].(float64); ok {
			if tier != "" {
				summary = fmt.Sprintf("Impact: risk=%s dependents=%.0f", tier, dep)
			} else {
				summary = fmt.Sprintf("Impact: %.0f dependents", dep)
			}
		} else if summary == "" {
			summary = "Computed blast radius"
		}
	}
	return toolSummary{summary: summary, topSymbol: topSymbol, symbols: symbols, files: files, risks: risks, verify: verify}
}

func summarizeTestImpactToolOutput(_ string, parsed map[string]any, task string, entities []string) toolSummary {
	var summary, topSymbol string
	var symbols, files, risks, verify []string

	summary = "Mapped test impact"
	if tests, ok := parsed["tests"].([]any); ok && len(tests) > 0 {
		summary = fmt.Sprintf("Tests to run: %d", len(tests))
		for i, t := range tests {
			if i >= 8 {
				break
			}
			if m, ok := t.(map[string]any); ok {
				if n, _ := m["name"].(string); n != "" {
					verify = append(verify, n)
				}
				if loc, _ := m["loc"].(string); loc != "" && isTestishPath(loc) {
					files = append(files, strings.Split(loc, ":")[0])
				}
			}
		}
	}
	if tfs, ok := parsed["test_files"].([]any); ok {
		for i, f := range tfs {
			if i >= 8 {
				break
			}
			if s, ok := f.(string); ok && s != "" {
				files = append(files, s)
				verify = append(verify, "run tests in "+s)
			}
		}
	}
	if len(verify) == 0 {
		if note, _ := parsed["note"].(string); note != "" {
			summary = "test impact: " + truncate(note, 120)
		}
	}
	return toolSummary{summary: summary, topSymbol: topSymbol, symbols: symbols, files: files, risks: risks, verify: verify}
}

func summarizeKickoffToolOutput(_ string, parsed map[string]any, task string, entities []string) toolSummary {
	var summary, topSymbol string
	var symbols, files, risks, verify []string

	summary = "Kickoff task starter pack ready"
	if v, ok := parsed["reuse_candidates"].([]any); ok && len(v) > 0 {
		summary = fmt.Sprintf("Kickoff reuse: %d candidates", len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if n, _ := m["name"].(string); n != "" {
					symbols = append(symbols, n)
					if topSymbol == "" {
						topSymbol = n
					}
					if loc, _ := m["loc"].(string); loc != "" {
						files = append(files, strings.Split(loc, ":")[0])
					}
				}
			}
		}
	}
	if findings, ok := parsed["findings"].([]any); ok {
		for i, item := range findings {
			if i >= 8 {
				break
			}
			if m, ok := item.(map[string]any); ok {
				if f, _ := m["file"].(string); f != "" {
					files = append(files, f)
				}
				if rule, _ := m["rule"].(string); rule != "" {
					risks = append(risks, rule)
				}
			}
		}
		if len(findings) > 0 {
			summary = fmt.Sprintf("Kickoff findings: %d grounded candidate(s)", len(findings))
		}
	}
	if v, ok := parsed["verification"].([]any); ok {
		for _, x := range v {
			if s, ok := x.(string); ok {
				verify = append(verify, s)
			}
		}
	}
	return toolSummary{summary: summary, topSymbol: topSymbol, symbols: symbols, files: files, risks: risks, verify: verify}
}

func summarizeInvestigateToolOutput(_ string, parsed map[string]any, task string, entities []string) toolSummary {
	var summary, topSymbol string
	var symbols, files, risks, verify []string

	summary = "Investigation bundle ready"
	if t, _ := parsed["target"].(string); t != "" {
		topSymbol = t
		symbols = append(symbols, t)
	}
	if q, ok := parsed["query"].(map[string]any); ok {
		if hits, ok := q["hits"].([]any); ok {
			ranked := parseRankedQueryHits(hits)
			if primary := pickPrimaryQueryHit(task, entities, ranked); primary.Name != "" {
				if topSymbol == "" {
					topSymbol = hitIdentity(primary)
				}
				symbols = uniqueAppend(symbols, primary.Name)
				if primary.Loc != "" {
					files = append(files, strings.Split(primary.Loc, ":")[0])
				}
			}
		}
	}
	if ctxBundle, ok := parsed["context"].(map[string]any); ok {
		if bundle, ok := ctxBundle["bundle"].(map[string]any); ok {
			if sym, ok := bundle["symbol"].(map[string]any); ok {
				if n, _ := sym["name"].(string); n != "" {
					if topSymbol == "" {
						topSymbol = n
					}
					symbols = uniqueAppend(symbols, n)
				}
				if loc, _ := sym["loc"].(string); loc != "" {
					files = append(files, strings.Split(loc, ":")[0])
				}
			}
		}
	}
	// Prefer repo_sink_scan findings (security recipe) so security_review cites files.
	if scan, ok := parsed["repo_sink_scan"].(map[string]any); ok {
		if findings, ok := scan["findings"].([]any); ok {
			for i, item := range findings {
				if i >= 10 {
					break
				}
				if m, ok := item.(map[string]any); ok {
					if f, _ := m["file"].(string); f != "" {
						files = append(files, f)
					}
					if rule, _ := m["rule"].(string); rule != "" {
						risks = append(risks, rule)
					}
				}
			}
			summary = fmt.Sprintf("Security sink scan: %d candidate(s)", len(findings))
		}
	}
	// Nested steps may also carry findings / hits.
	if findings, ok := parsed["findings"].([]any); ok {
		for i, item := range findings {
			if i >= 8 {
				break
			}
			if m, ok := item.(map[string]any); ok {
				if f, _ := m["file"].(string); f != "" {
					files = append(files, f)
				}
			}
		}
	}
	if note, _ := parsed["note"].(string); note != "" && summary == "Investigation bundle ready" {
		summary = truncate(note, 160)
	}
	if topSymbol != "" && summary == "Investigation bundle ready" {
		summary = fmt.Sprintf("Investigated %s", topSymbol)
	}
	return toolSummary{summary: summary, topSymbol: topSymbol, symbols: symbols, files: files, risks: risks, verify: verify}
}

func summarizeProjectContextToolOutput(_ string, parsed map[string]any, task string, entities []string) toolSummary {
	var summary, topSymbol string
	var symbols, files, risks, verify []string

	if pt, _ := parsed["project_type"].(string); pt != "" {
		summary = "orient: project_type=" + pt
	}
	if cmds, ok := parsed["suggested_verify_commands"].([]any); ok {
		for _, x := range cmds {
			if s, ok := x.(string); ok {
				verify = append(verify, s)
			}
		}
	}
	if cmds, ok := parsed["verification"].([]any); ok {
		for _, x := range cmds {
			if s, ok := x.(string); ok {
				verify = append(verify, s)
			}
		}
	}
	return toolSummary{summary: summary, topSymbol: topSymbol, symbols: symbols, files: files, risks: risks, verify: verify}
}

func summarizeDeadCodeToolOutput(_ string, parsed map[string]any, task string, entities []string) toolSummary {
	var summary, topSymbol string
	var symbols, files, risks, verify []string

	summary = "Dead/unreferenced symbols listed"
	if items, ok := parsed["candidates"].([]any); ok {
		names := make([]string, 0, len(items))
		for i, item := range items {
			if i >= 5 {
				break
			}
			if m, ok := item.(map[string]any); ok {
				if n, _ := m["name"].(string); n != "" {
					names = append(names, n)
					symbols = append(symbols, n)
					if topSymbol == "" {
						topSymbol = n
					}
					if loc, _ := m["loc"].(string); loc != "" {
						files = append(files, strings.Split(loc, ":")[0])
					}
				}
			}
		}
		summary = fmt.Sprintf("Dead code: %d candidates", len(items))
		if len(names) > 0 {
			summary += ": " + strings.Join(names, ", ")
		}
	} else if items, ok := parsed["unreferenced"].([]any); ok {
		summary = fmt.Sprintf("Dead code: %d unreferenced", len(items))
	}
	return toolSummary{summary: summary, topSymbol: topSymbol, symbols: symbols, files: files, risks: risks, verify: verify}
}
