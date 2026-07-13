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
}
