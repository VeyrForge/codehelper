package mcpsvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestToolResultRecoveryError_JSONDecisionTree(t *testing.T) {
	res := toolResultRecoveryError(agentRecovery{
		ErrorCategory: ErrCategoryValidation,
		IsRetryable:   true,
		RecoveryHint:  RecoveryCheckInput,
		Message:       "name is required",
		Expected:      "{\"name\":\"Symbol\"}",
		Example:       map[string]any{"name": "Foo"},
		WhatNext:      "Retry with name=",
	})
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError result")
	}
	text := resultText(res)
	var parsed agentRecovery
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("error body must be JSON: %v\n%s", err, text)
	}
	if parsed.ErrorCategory != ErrCategoryValidation || !parsed.IsRetryable {
		t.Fatalf("bad recovery parse: %+v", parsed)
	}
	if parsed.RecoveryHint != RecoveryCheckInput {
		t.Fatalf("hint: %q", parsed.RecoveryHint)
	}
	if !strings.Contains(parsed.Message, "name is required") {
		t.Fatalf("message: %q", parsed.Message)
	}
}

func TestWithRecoveryFields(t *testing.T) {
	out := withRecoveryFields(map[string]any{"found": false}, ErrCategoryNotFound, RecoveryTryAlternative, true)
	if out["error_category"] != ErrCategoryNotFound {
		t.Fatalf("%#v", out)
	}
	if out["recovery_hint"] != RecoveryTryAlternative {
		t.Fatalf("%#v", out)
	}
	if out["is_retryable"] != true {
		t.Fatalf("%#v", out)
	}
}

func TestContext_EmptyNameRecoveryJSON(t *testing.T) {
	reg, _, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"format": "json"}
	res, err := contextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error")
	}
	text := resultText(res)
	var parsed agentRecovery
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("expected JSON recovery error: %v\n%s", err, text)
	}
	if parsed.RecoveryHint != RecoveryCheckInput {
		t.Fatalf("hint=%q", parsed.RecoveryHint)
	}
	if parsed.Example == nil || parsed.Example["name"] == nil {
		t.Fatalf("expected example.name: %+v", parsed)
	}
}

func TestChangeKit_EmptyTargetRecoveryJSON(t *testing.T) {
	reg, _, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"format": "json"}
	res, err := changeKitHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error")
	}
	var parsed agentRecovery
	if err := json.Unmarshal([]byte(resultText(res)), &parsed); err != nil {
		t.Fatalf("expected JSON: %v\n%s", err, resultText(res))
	}
	if parsed.ErrorCategory != ErrCategoryValidation {
		t.Fatalf("%+v", parsed)
	}
}

func TestChangeKit_EditHintAndApplyPatchRNT(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "target": "Helper", "format": "json"}
	res, err := changeKitHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "apply_patch_workspace_file") {
		t.Fatalf("expected apply_patch in RNT/edit_hint: %s", text)
	}
	if !strings.Contains(text, "dry_run") {
		t.Fatalf("expected dry_run guidance: %s", text)
	}
	if !strings.Contains(text, "edit_hint") {
		t.Fatalf("expected edit_hint: %s", text)
	}
}

func TestSymbolMissGuidance_RecoveryFields(t *testing.T) {
	dir := t.TempDir()
	res := symbolMissGuidance(context.Background(), nil, dir, "x", "impact", "health", "json")
	text := resultText(res)
	if !strings.Contains(text, "recovery_hint") || !strings.Contains(text, "TRY_ALTERNATIVE") {
		t.Fatalf("expected recovery fields: %s", text)
	}
	if !strings.Contains(text, "error_category") {
		t.Fatalf("expected error_category: %s", text)
	}
}

func TestApplyPatch_MissingHunksRecoveryJSON(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "path": "a.go"}
	res, err := applyPatchWorkspaceFileHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error")
	}
	var parsed agentRecovery
	if err := json.Unmarshal([]byte(resultText(res)), &parsed); err != nil {
		t.Fatalf("expected JSON recovery: %v\n%s", err, resultText(res))
	}
	if parsed.RecoveryHint != RecoveryCheckInput {
		t.Fatalf("%+v", parsed)
	}
}
