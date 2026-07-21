package mcpsvc

import (
	"strings"
	"testing"
)

func TestAgentToolNames_CoversFeatureLifecycle(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range AgentToolNames {
		if seen[n] {
			t.Errorf("duplicate AgentToolName %q", n)
		}
		seen[n] = true
	}
	for _, n := range []string{
		"kickoff", "plan", "change_kit", "impact", "test_impact",
		"apply_patch_workspace_file", "review_diff", "finish_check",
		"dead_code", "hotspots", "rename_symbol", "insert_at_symbol",
	} {
		if !seen[n] {
			t.Errorf("AgentToolNames missing lifecycle tool %q (serve 10-tool subset regression)", n)
		}
	}
	if len(AgentToolNames) < 20 {
		t.Errorf("AgentToolNames too small (%d); expected expanded lifecycle set", len(AgentToolNames))
	}
	if len(AgentToolNames) >= 40 {
		t.Errorf("AgentToolNames=%d; keep under the 40-tool accuracy cliff", len(AgentToolNames))
	}
	catalog := map[string]bool{}
	for _, n := range AllMCPToolNames() {
		catalog[n] = true
	}
	for _, n := range AgentToolNames {
		if !catalog[n] {
			t.Errorf("AgentToolNames has unknown tool %q", n)
		}
	}
}

func TestFeatureLifecycleRecipes(t *testing.T) {
	recipes := FeatureLifecycleRecipes()
	wantIDs := map[string]bool{
		"add_feature": false, "remove_feature": false, "review_changes": false,
		"security_review": false, "dead_code": false, "performance": false,
		"architecture_qa": false, "locate_symbol": false, "vibe_fix": false,
		"vibe_ui": false, "programmer_ui": false, "browser_qa": false,
	}
	for _, r := range recipes {
		if _, ok := wantIDs[r.ID]; !ok {
			t.Errorf("unexpected recipe id %q", r.ID)
			continue
		}
		wantIDs[r.ID] = true
		if len(r.Tools) < 3 {
			t.Errorf("recipe %q has too few tools: %v", r.ID, r.Tools)
		}
	}
	for id, ok := range wantIDs {
		if !ok {
			t.Errorf("missing recipe %q", id)
		}
	}
	if !strings.Contains(VerifyFinishGateText, "finish_check") {
		t.Errorf("VerifyFinishGateText must mention finish_check")
	}
}
