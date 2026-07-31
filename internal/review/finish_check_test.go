package review

import (
	"strings"
	"testing"
)

func TestBuildFinishCheck_VerifyRequired(t *testing.T) {
	rr := BuildReleaseReadiness(
		&ReviewResult{Findings: nil, Risk: "low"},
		&ContractGuardResult{},
		&TestGapResult{},
		"low",
	)
	out := BuildFinishCheck(FinishCheckInput{
		Release:      rr,
		VerifyRan:    false,
		HasEditProof: true,
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

func TestBuildFinishCheck_VerifyAbstainedFailClosed(t *testing.T) {
	rr := BuildReleaseReadiness(
		&ReviewResult{Findings: nil, Risk: "low"},
		&ContractGuardResult{},
		&TestGapResult{},
		"low",
	)
	blocked := BuildFinishCheck(FinishCheckInput{
		Release:         rr,
		VerifyAbstained: true,
		VerifyReason:    "ephemeral bed",
		HasEditProof:    true,
	})
	if blocked.CanClaimDone {
		t.Fatal("verify_abstained without allow must not claim done")
	}
	if !strings.Contains(strings.Join(blocked.MissingBeforeDone, "|"), "allow_claim_without_verify") {
		t.Fatalf("expected allow missing: %+v", blocked.MissingBeforeDone)
	}
	allowed := BuildFinishCheck(FinishCheckInput{
		Release:                 rr,
		VerifyAbstained:         true,
		VerifyReason:            "ephemeral bed",
		AllowClaimWithoutVerify: true,
		HasEditProof:            true,
	})
	if !allowed.CanClaimDone {
		t.Fatalf("explicit allow should claim done: %+v", allowed)
	}
}

func TestBuildFinishCheck_NoEditOrDiagnosticsProof(t *testing.T) {
	rr := BuildReleaseReadiness(
		&ReviewResult{Findings: nil, Risk: "low"},
		&ContractGuardResult{},
		&TestGapResult{},
		"low",
	)
	out := BuildFinishCheck(FinishCheckInput{
		Release:   rr,
		VerifyRan: true,
	})
	if out.CanClaimDone {
		t.Fatal("no edit/diagnostics proof must block")
	}
	if !strings.Contains(strings.Join(out.MissingBeforeDone, "|"), "no edit or diagnostics proof") {
		t.Fatalf("expected proof missing: %+v", out.MissingBeforeDone)
	}
	withDiag := BuildFinishCheck(FinishCheckInput{
		Release:        rr,
		VerifyRan:      true,
		DiagnosticsRan: true,
	})
	if !withDiag.CanClaimDone {
		t.Fatalf("diagnostics_ran should satisfy proof: %+v", withDiag)
	}
}
