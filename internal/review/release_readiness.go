package review

import "strings"

func BuildReleaseReadiness(review *ReviewResult, contract *ContractGuardResult, tests *TestGapResult, risk string) ReleaseReadiness {
	blocking := make([]Finding, 0, 8)
	required := make([]string, 0, 8)
	for _, f := range review.Findings {
		if f.Severity == SeverityHigh || f.Severity == SeverityCritical {
			blocking = append(blocking, f)
		}
	}
	for _, f := range contract.BreakingChanges {
		blocking = append(blocking, f)
	}
	if len(tests.MissingTests) > 0 {
		required = append(required, "Add regression tests for changed symbols.")
	}
	required = append(required, review.RequiredActions...)
	comp := Completion{
		CompletionState: StateReviewed,
		CanClaimDone:    len(blocking) == 0 && strings.ToLower(risk) != "critical",
	}
	if !comp.CanClaimDone {
		comp.MissingBeforeDone = append(comp.MissingBeforeDone, "Resolve blocking review findings")
		comp.MissingBeforeDone = append(comp.MissingBeforeDone, required...)
	}
	summary := "Strict review: SHIP"
	if !comp.CanClaimDone {
		summary = "Strict review: DO NOT SHIP"
	}
	return ReleaseReadiness{
		Ship:               comp.CanClaimDone,
		Summary:            summary,
		Risk:               risk,
		BlockingFindings:   blocking,
		RequiredBeforeDone: required,
		Completion:         comp,
	}
}
