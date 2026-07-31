package review

import "strings"

// FinishCheckInput aggregates signals for the done gate.
type FinishCheckInput struct {
	BaseRef                 string
	VerifyRan               bool
	VerifyAbstained         bool
	VerifyReason            string
	AllowClaimWithoutVerify bool // explicit allow when verify_abstained
	HasEditProof            bool // git diff vs base has files
	DiagnosticsRan          bool // agent reported diagnostics ran
	Release                 ReleaseReadiness
}

// FinishCheckOutput matches MCP finish_check tool.
type FinishCheckOutput struct {
	CompletionState      string   `json:"completion_state"`
	CanClaimDone         bool     `json:"can_claim_done"`
	MissingBeforeDone    []string `json:"missing_before_done"`
	Note                 string   `json:"note,omitempty"`
	WhatNext             string   `json:"what_next,omitempty"`
	RecommendedNextTools []string `json:"recommended_next_tools,omitempty"`
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
	if in.VerifyAbstained {
		if strings.TrimSpace(in.VerifyReason) == "" {
			missing = append(missing, "verify abstained without reason")
		}
		if !in.AllowClaimWithoutVerify {
			missing = append(missing, "verify abstained without allow_claim_without_verify")
		}
	}
	if !in.HasEditProof && !in.DiagnosticsRan {
		missing = append(missing, "no edit or diagnostics proof")
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
		out.WhatNext = "Claim done — can_claim_done=true. Summarize the change; do not invent extra work."
		out.RecommendedNextTools = nil
	} else if in.VerifyAbstained && !in.AllowClaimWithoutVerify {
		out.Note = "Verify abstained — do not claim done as green without allow_claim_without_verify=true."
		out.WhatNext = "Pass allow_claim_without_verify=true only when verify truly cannot run, or re-run verify then finish_check verify_ran=true."
		out.RecommendedNextTools = []string{"verify", "project_context", "diagnostics"}
	} else if in.VerifyAbstained {
		out.Note = "Verify abstained — do not claim done as green; report the reason and remaining missing_before_done."
		out.WhatNext = "Report abstain reason from missing_before_done; do not set can_claim_done=true. Re-run verify when cmds exist."
		out.RecommendedNextTools = []string{"verify", "project_context", "diagnostics"}
	} else if !in.VerifyRan {
		out.Note = "Gate blocked — run verify (or set verify_abstained+verify_reason+allow_claim_without_verify), then finish_check again. Do not invent can_claim_done=true."
		out.WhatNext = "Run verify (argv lint_cmd/build_cmd/test_cmd, or repo= so cmds auto-fill), then finish_check verify_ran=true"
		out.RecommendedNextTools = []string{"verify", "diagnostics", "review_diff"}
	} else if !in.HasEditProof && !in.DiagnosticsRan {
		out.Note = "Gate blocked — no edit or diagnostics proof. Apply a change or pass diagnostics_ran=true after diagnostics."
		out.WhatNext = "Edit via apply_patch → diagnostics, or pass diagnostics_ran=true for read-only QA, then finish_check again"
		out.RecommendedNextTools = []string{"diagnostics", "apply_patch_workspace_file", "review_diff"}
	} else {
		out.Note = "Gate blocked — address missing_before_done (review/release), then finish_check again. Do not invent can_claim_done=true."
		out.WhatNext = "Fix blocking findings / required actions, then re-run review_diff → finish_check"
		out.RecommendedNextTools = []string{"review_diff", "diagnostics", "verify"}
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
		CompletionState:      "abstain",
		CanClaimDone:         false,
		MissingBeforeDone:    []string{reason, "re-run on a real git history or pass verify_abstained=true with verify_reason"},
		Note:                 "Abstain (not error): gate unavailable on this worktree. Do not claim done; do not treat finish_check as broken.",
		WhatNext:             "Do not claim done. Prefer verify_abstained=true + verify_reason on shallow/ephemeral beds, or re-run on a real git history.",
		RecommendedNextTools: []string{"verify", "project_context"},
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
