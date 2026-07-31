package mcpsvc

// WorkflowRecipe is a recommended tool sequence for a common agent task.
// Surfaced in project_context so agents pick the right loop instead of
// stopping after bootstrap or inventing ad-hoc chains.
type WorkflowRecipe struct {
	ID    string   `json:"id"`
	When  string   `json:"when"`
	Tools []string `json:"tools"`
	Note  string   `json:"note,omitempty"`
}

// VerifyFinishGateText is the hard done-gate guidance surfaced on every
// project_context bootstrap. Agents that skip verify → finish_check learn to
// claim done without evidence; keep this explicit and short.
const VerifyFinishGateText = "After ANY edit: diagnostics → review_diff → verify (argv-mode; set verify_ran=true) → finish_check. Claim done ONLY when finish_check.can_claim_done=true. If verify cannot run (no cmds / ephemeral bed), call finish_check with verify_abstained=true and verify_reason=… — never invent a green gate. UI changes: browser outline→assert→fix→retest (recipes vibe_ui|programmer_ui|browser_qa); attach passing browser assert/screenshot before finish_check."

// FeatureLifecycleRecipes are the default sequences for add / remove / review
// and related analysis tasks. Keep tool names exact MCP names.
func FeatureLifecycleRecipes() []WorkflowRecipe {
	return []WorkflowRecipe{
		{
			ID:   "add_feature",
			When: "adding a feature or extending behavior",
			Tools: []string{
				"kickoff", "plan", "scout", "context", "change_kit",
				"apply_patch_workspace_file", "insert_at_symbol",
				"diagnostics", "test_impact", "review_diff", "verify", "finish_check",
			},
			Note: "Prefer kickoff(task=…) over project_context→query→context. Reuse before adding; pass change_kit target= from query/scout. Gate: verify → finish_check (can_claim_done).",
		},
		{
			ID:   "remove_feature",
			When: "removing or deleting a symbol/feature",
			Tools: []string{
				"query", "impact", "test_impact", "change_kit", "dead_code",
				"apply_patch_workspace_file", "diagnostics", "review_diff", "verify", "finish_check",
			},
			Note: "Never delete on dead_code alone — confirm with impact (upstream) + a textual search. Update every caller from change_kit first. Gate: verify → finish_check.",
		},
		{
			ID:   "review_changes",
			When: "reviewing uncommitted or branch changes before claiming done",
			Tools: []string{
				"detect_changes", "since", "review_diff", "review", "diagnostics", "verify", "finish_check",
			},
			Note: "review_diff is line-level; review is symbol blast-radius. Pair both with diagnostics → verify → finish_check before claiming done.",
		},
		{
			ID:   "security_review",
			When: "security review or hardening a trust boundary",
			Tools: []string{
				"kickoff", "plan", "query", "context", "impact", "investigate",
				"review_diff", "review", "diagnostics", "verify", "finish_check",
			},
			Note: "kickoff/plan with role=security, or investigate recipe=security. review/review_diff scan SQL concat, eval, secrets, shell injection. Gate: verify → finish_check.",
		},
		{
			ID:   "dead_code",
			When: "finding unused symbols or cleaning dead code",
			Tools: []string{
				"dead_code", "impact", "change_kit", "query", "investigate",
				"apply_patch_workspace_file", "review_diff", "verify", "finish_check",
			},
			Note: "dead_code returns path/symbol/reason/confidence (high first). Over-approximates — verify each with impact + textual search. Or investigate recipe=dead_code. Gate: verify → finish_check.",
		},
		{
			ID:   "performance",
			When: "performance investigation or hot-path changes",
			Tools: []string{
				"hotspots", "kickoff", "plan", "query", "context", "impact", "investigate",
				"test_impact", "change_kit", "diagnostics", "review_diff", "verify", "finish_check",
			},
			Note: "kickoff/plan with role=performance, or investigate recipe=perf. hotspots = churn × centrality. Confirm with impact + test_impact before editing. Gate: verify → finish_check.",
		},
		{
			ID:   "architecture_qa",
			When: "architecture Q&A / design — how does X reach Y, where should this live (no edits yet)",
			Tools: []string{
				"kickoff", "plan", "investigate", "query", "trace", "context", "impact",
			},
			Note: "kickoff/plan with role=architect, or investigate recipe=architecture. Answer with cited symbols/paths; do NOT edit until the user accepts the plan. Prefer method/fn targets over leaf type hubs; if impact is self-only, retry direction=upstream or a method.",
		},
		{
			ID:   "locate_symbol",
			When: "find where a symbol/behavior lives (search / locate)",
			Tools: []string{
				"query", "scout", "context", "trace", "impact",
			},
			Note: "Prefer query for a name; scout when asking 'what already does X?'. Production defs rank above sample/test/fixture. Ambiguous? pass path= on context/impact. Class hubs: impact defaults upstream (who uses this).",
		},
		{
			ID:   "vibe_fix",
			When: "underspecified Slack-style fix/feature — orient fast, reuse, then gate",
			Tools: []string{
				"kickoff", "scout", "context", "change_kit",
				"apply_patch_workspace_file", "diagnostics", "review_diff", "verify", "finish_check",
			},
			Note: "kickoff(task=…) first (sections=orient,reuse if cheap). Extend high-caller reuse over new files. After ANY edit: diagnostics → review_diff → verify → finish_check (can_claim_done). Never invent a green gate. UI? switch to vibe_ui.",
		},
		{
			ID:   "vibe_ui",
			When: "underspecified UI/CMS fix — vibe coding with a real-page proof",
			Tools: []string{
				"kickoff", "scout", "context", "change_kit",
				"apply_patch_workspace_file", "browser", "diagnostics", "review_diff", "verify", "finish_check",
			},
			Note: "Same as vibe_fix PLUS browser: propose setup_suggestions if no site=/URL yet → outline once → actions+assert → on fail fix → retest same assert. CMS: recipe=wp_*|laravel_login|django_admin|drupal_login|magento_login|spa_hydrate + site=. Claim done only with green browser assert AND finish_check.can_claim_done.",
		},
		{
			ID:   "programmer_ui",
			When: "intentional UI feature/fix — implement, browser-test, debug, retest",
			Tools: []string{
				"kickoff", "plan", "query", "context", "change_kit",
				"apply_patch_workspace_file", "browser", "diagnostics", "review_diff", "verify", "finish_check",
			},
			Note: "Loop: propose setup if needed → implement → browser(outline|recipe|actions|assert) → read diagnostics → query console/assert failures → patch → retest. Optional metrics/audit/devices. Gate: browser assert + verify → finish_check.",
		},
		{
			ID:   "browser_qa",
			When: "verify a running page / admin flow without (or after) code edits",
			Tools: []string{
				"browser", "web", "finish_check",
			},
			Note: "HTTP-only → web. Visual/JS/e2e → browser outline then assert. Propose setup_suggestions first if site= missing. CMS: recipe+site+session. Remote: SSH -L to 127.0.0.1 then browse (GuardURL). Attach screenshot/assert evidence before finish_check; verify_abstained only if no live URL.",
		},
	}
}
