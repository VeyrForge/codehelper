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
	Note              string   `json:"note,omitempty"`
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

	out := FinishCheckOutput{
		CompletionState:   state,
		CanClaimDone:      len(missing) == 0 && in.Release.Completion.CanClaimDone,
		MissingBeforeDone: dedupeFinishMissing(missing),
	}
	if out.CanClaimDone {
		out.Note = "Gate green — safe to claim done."
	} else if in.VerifyAbstained {
		out.Note = "Verify abstained — do not claim done as green; report the reason and remaining missing_before_done."
	} else {
		out.Note = "Gate blocked — run verify (or set verify_abstained+verify_reason), then finish_check again. Do not invent can_claim_done=true."
	}
	return out
}

// BuildFinishCheckAbstain returns a structured non-error response when the gate
// cannot be computed (shallow clone, missing git, ephemeral fixture). Agents
// should treat this as abstain — not ignore the tool as "broken".
func BuildFinishCheckAbstain(baseRef, reason string) FinishCheckOutput {
	base := strings.TrimSpace(baseRef)
	if base == "" {
		base = "HEAD~1"
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "finish_check could not evaluate the diff/release gate"
	}
	return FinishCheckOutput{
		CompletionState:   "abstain",
		CanClaimDone:      false,
		MissingBeforeDone: []string{reason, "re-run on a real git history or pass verify_abstained=true with verify_reason"},
		Note:              "Abstain (not error): gate unavailable on this worktree. Do not claim done; do not treat finish_check as broken.",
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
