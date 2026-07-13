package review

import "strings"

// FinishCheckInput aggregates signals for the done gate.
type FinishCheckInput struct {
	BaseRef         string
	VerifyRan       bool
	VerifyAbstained bool
	VerifyReason    string
	Release         ReleaseReadiness
}

// FinishCheckOutput matches MCP finish_check tool.
type FinishCheckOutput struct {
	CompletionState   string   `json:"completion_state"`
	CanClaimDone      bool     `json:"can_claim_done"`
	MissingBeforeDone []string `json:"missing_before_done"`
}

// BuildFinishCheck merges release_readiness with verify hygiene.
func BuildFinishCheck(in FinishCheckInput) FinishCheckOutput {
	var missing []string
	base := strings.TrimSpace(in.BaseRef)
	if base == "" {
		base = "HEAD~1"
	}

	if !in.VerifyRan && !in.VerifyAbstained {
		missing = append(missing, "verify was not run")
	}
	if in.VerifyAbstained && strings.TrimSpace(in.VerifyReason) == "" {
		missing = append(missing, "verify abstained without reason")
	}

	for _, m := range in.Release.Completion.MissingBeforeDone {
		if strings.TrimSpace(m) != "" {
			missing = append(missing, m)
		}
	}
	if !in.Release.Completion.CanClaimDone {
		for _, r := range in.Release.RequiredBeforeDone {
			missing = append(missing, r)
		}
	}

	state := "blocked"
	if len(missing) == 0 && in.Release.Completion.CanClaimDone {
		state = "ready"
	}

	return FinishCheckOutput{
		CompletionState:   state,
		CanClaimDone:      len(missing) == 0 && in.Release.Completion.CanClaimDone,
		MissingBeforeDone: dedupeFinishMissing(missing),
	}
}

func dedupeFinishMissing(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
