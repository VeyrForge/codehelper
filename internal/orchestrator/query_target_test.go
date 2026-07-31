package orchestrator

import (
	"strings"
	"testing"
)

func TestBestEntityQuery(t *testing.T) {
	got := bestEntityQuery([]string{"fix", "bug", "Run", "fails"})
	if got != "Run" {
		t.Fatalf("got %q want Run", got)
	}
	got = bestEntityQuery([]string{"godot_log", "work"})
	if got != "godot_log" {
		t.Fatalf("got %q want godot_log", got)
	}
	got = bestEntityQuery([]string{"fix", "requireOrchestrationEnabled", "Run"})
	if got != "requireOrchestrationEnabled" {
		t.Fatalf("got %q want requireOrchestrationEnabled (strong CamelCase over Run)", got)
	}
}

func TestPickPrimaryQueryHit_PrefersExactOverSibling(t *testing.T) {
	hits := []rankedQueryHit{
		{Name: "orchestrationDisabledResult", ID: "sym:r:a.go:10:orchestrationDisabledResult", Loc: "internal/mcpsvc/orchestration_tools.go:33"},
		{Name: "requireOrchestrationEnabled", ID: "sym:r:a.go:42:requireOrchestrationEnabled", Loc: "internal/mcpsvc/orchestration_tools.go:42"},
		{Name: "orchestrateHandler", ID: "sym:r:a.go:161:orchestrateHandler", Loc: "internal/mcpsvc/orchestration_tools.go:161"},
	}
	task := "how does requireOrchestrationEnabled gate orchestrate"
	got := pickPrimaryQueryHit(task, []string{"requireOrchestrationEnabled"}, hits)
	if got.Name != "requireOrchestrationEnabled" {
		t.Fatalf("got %q want requireOrchestrationEnabled (exact over sibling-first)", got.Name)
	}
}

func TestPickPrimaryQueryHit_PathQualified(t *testing.T) {
	hits := []rankedQueryHit{
		{Name: "show", ID: "sym:r:ex.js:1:show", Loc: "examples/mvc/controllers/user.js:10"},
		{Name: "show", ID: "sym:r:lib.js:1:show", Loc: "lib/application.js:40"},
	}
	task := "explain show in lib/application.js"
	got := pickPrimaryQueryHit(task, []string{"show"}, hits)
	if !strings.Contains(strings.ReplaceAll(got.Loc, "\\", "/"), "lib/application.js") {
		t.Fatalf("got loc %q want path-qualified lib/application.js", got.Loc)
	}
}

func TestPickPrimaryQueryHit_DemotesThirdPartyAndTestbeds(t *testing.T) {
	hits := []rankedQueryHit{
		{Name: "Run", ID: "sym:r:tp:Run", Loc: "third_party/green-engine/runner.go:10"},
		{Name: "Run", ID: "sym:r:tb:Run", Loc: ".testbeds/live-harness/run.go:5"},
		{Name: "Run", ID: "sym:r:prod:Run", Loc: "internal/orchestrator/orchestrator.go:100"},
	}
	got := pickPrimaryQueryHit("how does Run work", []string{"Run"}, hits)
	if !strings.Contains(strings.ReplaceAll(got.Loc, "\\", "/"), "internal/orchestrator/orchestrator.go") {
		t.Fatalf("got loc %q want production def over third_party/testbeds", got.Loc)
	}
}

func TestExactSymbolFromTask_StrongCamelCase(t *testing.T) {
	got := exactSymbolFromTask("explain requireOrchestrationEnabled gating", nil)
	if got != "requireOrchestrationEnabled" {
		t.Fatalf("got %q", got)
	}
	if got := exactSymbolFromTask("how does Run work", nil); got != "" {
		t.Fatalf("short Run should not short-circuit, got %q", got)
	}
	got = exactSymbolFromTask("explain requireOrchestrationEnabled in internal/mcpsvc/orchestration_tools.go", nil)
	if got != "requireOrchestrationEnabled" {
		t.Fatalf("path must not beat CamelCase, got %q", got)
	}
}

func TestShouldShortCircuitInvestigate(t *testing.T) {
	task := "explain requireOrchestrationEnabled in internal/mcpsvc/orchestration_tools.go"
	plan := ClassifyTask(task, Constraints{}, nil)
	sym, ok := shouldShortCircuitInvestigate(plan, task)
	if !ok || sym != "requireOrchestrationEnabled" {
		t.Fatalf("short-circuit sym=%q ok=%v plan.entities=%v workflow=%s", sym, ok, plan.Entities, plan.Workflow)
	}
}
