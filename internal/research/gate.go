package research

import (
	"strings"

	"github.com/VeyrForge/codehelper/internal/patterns"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/taskstore"
)

var triggerTopics = []string{
	"deprecated", "migration", "oauth", "auth", "payment", "schema", "upload",
	"cache", "webhook", "api version", "breaking", "security", "csrf", "nonce",
	"woocommerce", "laravel", "wordpress", "mcp", "graphql",
}

// ShouldResearch heuristically decides if official docs research is warranted.
func ShouldResearch(request string, prof *profile.ProjectProfile, ex patterns.ExpandOutput) bool {
	lq := strings.ToLower(strings.TrimSpace(request))
	for _, t := range triggerTopics {
		if strings.Contains(lq, t) {
			return true
		}
	}
	for _, r := range ex.RiskChecks {
		lr := strings.ToLower(r)
		for _, t := range triggerTopics {
			if strings.Contains(lr, t) {
				return true
			}
		}
	}
	if ex.AskUser && strings.TrimSpace(ex.AskReason) != "" {
		return true
	}
	if prof != nil && prof.ProjectType != "" && prof.ProjectType != "unknown" {
		if strings.Contains(lq, prof.ProjectType) {
			return true
		}
	}
	return false
}

// BuildSummary creates a structured research summary for plans when research has not run yet.
func BuildSummary(request string, prof *profile.ProjectProfile) *taskstore.ResearchSummary {
	needed := "Task may depend on current framework/library behavior."
	sources := []string{"official documentation", "project code via query/context"}
	rec := "Prefer patterns already in this repo; confirm against official docs before introducing new APIs."
	if prof != nil && prof.ProjectType != "" {
		rec = "Confirm " + prof.ProjectType + " APIs against official docs; reuse existing project patterns first."
	}
	return &taskstore.ResearchSummary{
		Needed:         needed,
		Sources:        sources,
		Recommendation: rec,
		Avoid:          []string{"blind copy-paste from community examples without validation"},
		ProjectImpact:  "Research pending — run agent_research or POST /v1/research when network is enabled.",
	}
}
