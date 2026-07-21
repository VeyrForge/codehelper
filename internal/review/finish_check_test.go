package review

import "testing"

func TestBuildFinishCheck_VerifyRequired(t *testing.T) {
	rr := BuildReleaseReadiness(
		&ReviewResult{Findings: nil, Risk: "low"},
		&ContractGuardResult{},
		&TestGapResult{},
		"low",
	)
	out := BuildFinishCheck(FinishCheckInput{
		Release:   rr,
		VerifyRan: false,
	})
	if out.CanClaimDone {
		t.Fatal("expected blocked without verify")
	}
	if out.Note == "" {
		t.Fatal("expected guidance note when blocked")
	}
}

func TestBuildFinishCheckAbstain(t *testing.T) {
	out := BuildFinishCheckAbstain("HEAD~1", "no parent commit")
	if out.CanClaimDone {
		t.Fatal("abstain must not claim done")
	}
	if out.CompletionState != "abstain" {
		t.Fatalf("want abstain, got %q", out.CompletionState)
	}
}
