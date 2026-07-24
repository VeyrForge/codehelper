package mcpsvc

import (
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
)

// Recovery hint codes for LLM self-correction (SERF / MCP production patterns).
// Keep the set small so agents learn a stable decision tree.
const (
	RecoveryCheckInput       = "CHECK_INPUT"        // fix arg key/value and retry
	RecoveryTryAlternative   = "TRY_ALTERNATIVE"    // different tool or search term
	RecoveryRefreshIndex     = "REFRESH_INDEX"      // codehelper analyze --force
	RecoveryDisambiguate     = "DISAMBIGUATE"       // path= or sym: id
	RecoveryPreviewThenApply = "PREVIEW_THEN_APPLY" // dry_run then write
	RecoveryReportToUser     = "REPORT_TO_USER"     // unrecoverable / abstain honestly
	RecoveryCheckWorkspace   = "CHECK_WORKSPACE"    // wrong repo / cwd vs open project
)

// Error categories — machine-stable for agent branching.
const (
	ErrCategoryValidation = "VALIDATION_ERROR"
	ErrCategoryNotFound   = "NOT_FOUND"
	ErrCategoryAmbiguous  = "AMBIGUOUS"
	ErrCategoryStaleIndex = "STALE_INDEX"
	ErrCategoryConflict   = "CONFLICT" // patch hunk mismatch, etc.
)

// agentRecovery is the structured recovery envelope agents should parse from
// tool errors (and soft-miss payloads). Message stays human-readable; Hint /
// Category / Retryable are the decision tree.
type agentRecovery struct {
	ErrorCategory        string         `json:"error_category"`
	IsRetryable          bool           `json:"is_retryable"`
	RecoveryHint         string         `json:"recovery_hint"`
	Message              string         `json:"message"`
	Expected             string         `json:"expected,omitempty"`
	Example              map[string]any `json:"example,omitempty"`
	WhatNext             string         `json:"what_next,omitempty"`
	RecommendedNextTools []string       `json:"recommended_next_tools,omitempty"`
	NextQueries          []string       `json:"next_queries,omitempty"`
}

// toolResultRecoveryError returns an MCP error whose text is JSON with
// recovery fields. Agents that json.loads the error body get a decision tree;
// agents that only read prose still see Message first in pretty-printed JSON.
func toolResultRecoveryError(rec agentRecovery) *mcp.CallToolResult {
	if rec.ErrorCategory == "" {
		rec.ErrorCategory = ErrCategoryValidation
	}
	if rec.RecoveryHint == "" {
		rec.RecoveryHint = RecoveryCheckInput
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(rec.Message)
	}
	return mcp.NewToolResultError(string(b))
}

// withRecoveryFields merges recovery keys into an existing soft-miss / guidance map.
func withRecoveryFields(out map[string]any, category, hint string, retryable bool) map[string]any {
	if out == nil {
		out = map[string]any{}
	}
	out["error_category"] = category
	out["recovery_hint"] = hint
	out["is_retryable"] = retryable
	return out
}
