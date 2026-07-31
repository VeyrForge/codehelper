package orchestrator

import "testing"

func TestClassifyTierFastExplain(t *testing.T) {
	plan := ClassifyTask("how does ResolveSymrefs work", Constraints{}, nil)
	if got := ClassifyTier(plan, "how does ResolveSymrefs work"); got != TierFast {
		t.Fatalf("tier=%s want fast", got)
	}
}

func TestClassifyTierDeepLowConfidence(t *testing.T) {
	plan := Plan{Intent: IntentFeature, Confidence: 0.65, Entities: []string{"a", "b", "c"}}
	if got := ClassifyTier(plan, "add something vague"); got != TierDeep {
		t.Fatalf("tier=%s want deep", got)
	}
}

func TestWorkflowStepsForTierFeatureFastSkipsScout(t *testing.T) {
	steps := WorkflowStepsForTier(WorkflowFeatureScope, TierFast)
	if len(steps) != 1 || steps[0].Tool != "kickoff" {
		t.Fatalf("fast feature steps=%v", steps)
	}
}

func TestWorkflowStepsForTierBugfixFastIsTwoTools(t *testing.T) {
	steps := WorkflowStepsForTier(WorkflowBugfixTriage, TierFast)
	if len(steps) != 2 {
		t.Fatalf("want 2 steps got %d", len(steps))
	}
}

func TestShouldSkipScoutWhenReuseRich(t *testing.T) {
	pack := ContextPack{Symbols: []string{"a", "b", "c"}}
	if !shouldSkipStep("scout", pack) {
		t.Fatal("expected scout skip")
	}
	if shouldSkipStep("query", pack) {
		t.Fatal("query should not skip")
	}
}

func TestShouldCompressBriefFastSkipsShort(t *testing.T) {
	if shouldCompressBrief(TierFast, 500) {
		t.Fatal("fast tier short brief should not compress")
	}
	if !shouldCompressBrief(TierDeep, 500) {
		t.Fatal("deep tier should compress")
	}
}
