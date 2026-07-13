package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
)

// enrichPackFromKickoff parses a kickoff JSON payload into the context pack so
// feature_scope answers surface orient, reuse, steps, and decisions — not just
// a one-line summary.
func enrichPackFromKickoff(raw string, pack *ContextPack) string {
	var k map[string]any
	if json.Unmarshal([]byte(raw), &k) != nil {
		return ""
	}
	topSymbol := ""
	if o, ok := k["orient"].(map[string]any); ok {
		pt, _ := o["project_type"].(string)
		sum, _ := o["summary"].(string)
		langs, _ := o["languages"].([]any)
		var langStr string
		if len(langs) > 0 {
			if s, ok := langs[0].(string); ok {
				langStr = s
			}
		}
		line := fmt.Sprintf("orient: project_type=%s", pt)
		if langStr != "" {
			line += " language=" + langStr
		}
		if sum != "" {
			line += " — " + truncate(sum, 140)
		}
		pack.OrientLine = line
		pack.Snippets = append(pack.Snippets, line)
	}
	if v, ok := k["reuse_candidates"].([]any); ok {
		for _, item := range v {
			m, _ := item.(map[string]any)
			n, _ := m["name"].(string)
			if n == "" {
				continue
			}
			pack.Symbols = uniqueAppend(pack.Symbols, n)
			if topSymbol == "" {
				topSymbol = n
			}
			if loc, _ := m["loc"].(string); loc != "" {
				pack.Locations = uniqueAppend(pack.Locations, loc)
				pack.Files = uniqueAppend(pack.Files, strings.Split(loc, ":")[0])
			}
		}
		if len(v) > 0 {
			pack.Snippets = append(pack.Snippets, fmt.Sprintf("reuse: %d kickoff candidates ranked", len(v)))
		}
	}
	if steps, ok := k["steps"].([]any); ok {
		for _, s := range steps {
			if str, ok := s.(string); ok && str != "" {
				pack.Steps = append(pack.Steps, str)
			}
		}
		if len(pack.Steps) > 0 {
			show := pack.Steps
			if len(show) > 4 {
				show = show[:4]
			}
			pack.Snippets = append(pack.Snippets, "steps: "+strings.Join(show, " | "))
		}
	}
	if dps, ok := k["decision_points"].([]any); ok {
		for i, d := range dps {
			if i >= 4 {
				break
			}
			if str, ok := d.(string); ok && str != "" {
				pack.Decisions = append(pack.Decisions, str)
				pack.Risks = uniqueAppend(pack.Risks, "decision: "+truncate(str, 120))
			}
		}
	}
	if pl, ok := k["placement"].([]any); ok {
		for i, p := range pl {
			if i >= 3 {
				break
			}
			if str, ok := p.(string); ok && str != "" {
				pack.Snippets = append(pack.Snippets, "placement: "+str)
			}
		}
	}
	if dup, ok := k["duplication_risk"].([]any); ok {
		for i, d := range dup {
			if i >= 2 {
				break
			}
			if str, ok := d.(string); ok && str != "" {
				pack.Risks = uniqueAppend(pack.Risks, "duplication: "+truncate(str, 100))
			}
		}
	}
	if imp, ok := k["impact_of_top"].(map[string]any); ok {
		if tier, _ := imp["risk_tier"].(string); tier != "" {
			pack.Risks = uniqueAppend(pack.Risks, "impact risk="+tier)
		}
		if target, _ := imp["target"].(string); target != "" {
			topSymbol = target
			pack.Symbols = uniqueAppend(pack.Symbols, target)
		}
	}
	if v, ok := k["verification"].([]any); ok {
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				pack.Verification = uniqueAppend(pack.Verification, s)
			}
		}
	}
	if note, _ := k["note"].(string); note != "" {
		pack.Snippets = append(pack.Snippets, "kickoff: "+truncate(note, 180))
	}
	return topSymbol
}

// enrichPackFromKickoffText is a fallback when kickoff returns TOON/text instead of JSON.
func enrichPackFromKickoffText(raw string, pack *ContextPack) string {
	topSymbol := ""
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- name:") || strings.HasPrefix(line, "name:") {
			n := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "- name:"), "name:"))
			if n != "" && isSymbolName(n) {
				pack.Symbols = uniqueAppend(pack.Symbols, n)
				if topSymbol == "" {
					topSymbol = n
				}
			}
		}
		if strings.Contains(line, "loc:") {
			if i := strings.Index(line, `"`); i >= 0 {
				rest := line[i+1:]
				if j := strings.Index(rest, `"`); j > 0 {
					loc := rest[:j]
					pack.Locations = uniqueAppend(pack.Locations, loc)
					if path := strings.Split(loc, ":")[0]; path != "" {
						pack.Files = uniqueAppend(pack.Files, path)
					}
				}
			}
		}
		if strings.HasPrefix(strings.ToLower(line), "orient:") || strings.Contains(strings.ToLower(line), "project_type:") {
			pack.OrientLine = "orient: " + truncate(line, 160)
			pack.Snippets = append(pack.Snippets, pack.OrientLine)
		}
	}
	if strings.Contains(strings.ToLower(raw), "reuse_candidates") {
		pack.Snippets = append(pack.Snippets, "reuse: kickoff candidates ranked")
	}
	return topSymbol
}

func isSymbolName(s string) bool {
	if len(s) < 2 || len(s) > 64 {
		return false
	}
	return !strings.ContainsAny(s, `"'[]{}`)
}
