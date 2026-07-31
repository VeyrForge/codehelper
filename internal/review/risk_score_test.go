package review

import "testing"

func TestRiskScore(t *testing.T) {
	got := RiskScore([]Finding{
		{Severity: SeverityMedium},
		{Severity: SeverityHigh},
	})
	if got != "high" {
		t.Fatalf("expected high risk, got %s", got)
	}
}

func TestBuildReleaseReadiness_DoNotShipOnBlocking(t *testing.T) {
	r := &ReviewResult{
		Findings: []Finding{{Severity: SeverityHigh, Category: "contract", Message: "break"}},
	}
	c := &ContractGuardResult{}
	g := &TestGapResult{}
	out := BuildReleaseReadiness(r, c, g, "high")
	if out.Ship {
		t.Fatalf("expected not shippable with blocking findings")
	}
	if out.Completion.CanClaimDone {
		t.Fatalf("expected can_claim_done=false")
	}
}
