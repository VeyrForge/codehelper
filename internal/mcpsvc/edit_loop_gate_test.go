package mcpsvc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestAnnotateVerifyWhatNext_Branches(t *testing.T) {
	abstain := &verifyToolResponse{Abstain: true}
	annotateVerifyWhatNext(abstain)
	if abstain.WhatNext == "" || !strings.Contains(strings.Join(abstain.RecommendedNextTools, ","), "finish_check") {
		t.Fatalf("abstain RNT: %+v", abstain)
	}

	ok := &verifyToolResponse{Accepted: true}
	annotateVerifyWhatNext(ok)
	if !strings.Contains(ok.WhatNext, "finish_check") || !strings.Contains(strings.Join(ok.RecommendedNextTools, ","), "finish_check") {
		t.Fatalf("accepted RNT: %+v", ok)
	}

	fail := &verifyToolResponse{Accepted: false, Reasons: []string{"tests failed"}}
	annotateVerifyWhatNext(fail)
	if !strings.Contains(fail.WhatNext, "verify") || !strings.Contains(strings.Join(fail.RecommendedNextTools, ","), "diagnostics") {
		t.Fatalf("fail RNT: %+v", fail)
	}
}

func TestAnnotateReviewDiffWhatNext_Branches(t *testing.T) {
	clean := &reviewDiffToolResponse{Summary: "ok", Risk: "low"}
	annotateReviewDiffWhatNext(clean)
	if !strings.Contains(clean.WhatNext, "verify") {
		t.Fatalf("clean: %+v", clean)
	}

	block := &reviewDiffToolResponse{
		Findings: []review.Finding{{Severity: review.SeverityHigh, Message: "secret"}},
	}
	annotateReviewDiffWhatNext(block)
	if !strings.Contains(block.WhatNext, "findings") {
		t.Fatalf("blocking: %+v", block)
	}
}

func TestVerifyToolResponse_TOONRoundTrip(t *testing.T) {
	out := verifyToolResponse{
		Accepted: true, Confidence: 1, Reasons: []string{},
		WhatNext: "next", RecommendedNextTools: []string{"finish_check"},
	}
	res, err := toolResultFormatted(out, "toon")
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(res)
	if strings.HasPrefix(strings.TrimSpace(text), "{") {
		t.Fatalf("expected TOON text, got JSON: %s", text)
	}
	if !strings.Contains(text, "what_next") || !strings.Contains(text, "finish_check") {
		t.Fatalf("TOON missing fields: %s", text)
	}
	resJSON, err := toolResultFormatted(out, "json")
	if err != nil {
		t.Fatal(err)
	}
	var parsed verifyToolResponse
	if err := json.Unmarshal([]byte(resultText(resJSON)), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.WhatNext != "next" {
		t.Fatalf("%+v", parsed)
	}
}

func TestFinishCheckOutput_HasRNT(t *testing.T) {
	rr := review.BuildReleaseReadiness(
		&review.ReviewResult{Findings: nil, Risk: "low"},
		&review.ContractGuardResult{},
		&review.TestGapResult{},
		"low",
	)
	blocked := review.BuildFinishCheck(review.FinishCheckInput{Release: rr, VerifyRan: false})
	if blocked.WhatNext == "" || !strings.Contains(strings.Join(blocked.RecommendedNextTools, ","), "verify") {
		t.Fatalf("blocked finish_check RNT: %+v", blocked)
	}
	abstain := review.BuildFinishCheckAbstain("HEAD~1", "shallow")
	if abstain.WhatNext == "" {
		t.Fatal("abstain missing what_next")
	}
}

// Integration path (skipped on Windows via buildIndexedRepo stub).
func TestVerify_RepoAliasAndWhatNextTOON(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"go.mod":  "module example.com/demo\n\ngo 1.22\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":     repo.Name,
		"test_cmd": "go test ./...",
		"format":   "toon",
	}
	res, err := verifyHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(res))
	}
	text := resultText(res)
	if strings.TrimSpace(text) == "" || text[0] == '{' {
		t.Fatalf("expected TOON (not JSON object) by default/toon: %q", truncateSmoke(text, 200))
	}
	if !strings.Contains(text, "what_next") || !strings.Contains(text, "recommended_next_tools") {
		t.Fatalf("expected what_next + RNT in TOON: %s", truncateSmoke(text, 600))
	}
	if !strings.Contains(text, "finish_check") {
		t.Fatalf("expected finish_check in RNT: %s", truncateSmoke(text, 600))
	}
}

func TestVerify_RepoOnlyNoRepoRoot(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"go.mod":  "module example.com/demo\n\ngo 1.22\n",
		"main.go": "package main\n\nfunc Helper() int { return 1 }\n",
	})
	req := mcp.CallToolRequest{}
	// Agents often omit repo_root and pass only repo= — must not fail validation.
	req.Params.Arguments = map[string]any{
		"repo":   repo.Name,
		"format": "json",
	}
	res, err := verifyHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("repo= alone must resolve: %s", resultText(res))
	}
	var out verifyToolResponse
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, resultText(res))
	}
	if out.WhatNext == "" || len(out.RecommendedNextTools) == 0 {
		t.Fatalf("expected RNT/what_next: %+v", out)
	}
	// Either accepted, failed with reasons, or honest abstain — never silent empty.
	if !out.Accepted && !out.Abstain && len(out.Reasons) == 0 {
		t.Fatalf("silent fail: %+v", out)
	}
}

func TestReviewDiff_WhatNextTOON(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "format": "toon"}
	res, err := reviewDiffHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "what_next") || !strings.Contains(text, "verify") {
		t.Fatalf("expected what_next→verify: %s", truncateSmoke(text, 500))
	}
}

func TestFinishCheck_WhatNextAndTOON(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Helper() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "format": "toon"}
	res, err := finishCheckHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, "what_next") || !strings.Contains(text, "can_claim_done") {
		t.Fatalf("expected finish_check fields: %s", truncateSmoke(text, 500))
	}
	if !strings.Contains(text, "verify") {
		t.Fatalf("blocked finish_check should steer to verify: %s", truncateSmoke(text, 500))
	}

	req2 := mcp.CallToolRequest{}
	req2.Params.Arguments = map[string]any{
		"repo": repo.Name, "verify_ran": true, "format": "json",
	}
	res2, err := finishCheckHandler(reg)(ctx, req2)
	if err != nil {
		t.Fatal(err)
	}
	text2 := resultText(res2)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text2), &parsed); err != nil {
		t.Fatalf("json: %v\n%s", err, text2)
	}
	if _, ok := parsed["what_next"]; !ok {
		t.Fatalf("missing what_next: %s", text2)
	}
}

func TestDiagnostics_WhatNextAfterClean(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"go.mod":  "module example.com/demo\n\ngo 1.22\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo": repo.Name, "command": "go vet ./...", "format": "json",
	}
	res, err := diagnosticsHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s", resultText(res))
	}
	var out diagnosticsResponse
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, resultText(res))
	}
	if out.WhatNext == "" || len(out.RecommendedNextTools) == 0 {
		t.Fatalf("expected RNT: %+v", out)
	}
	joined := strings.Join(out.RecommendedNextTools, ",")
	if !strings.Contains(joined, "review_diff") && !strings.Contains(joined, "verify") {
		t.Fatalf("RNT should continue edit loop: %v", out.RecommendedNextTools)
	}
}

func TestEditLoop_ChangeKitToFinishCheckChain(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"go.mod":    "module example.com/demo\n\ngo 1.22\n",
		"helper.go": "package demo\n\nfunc Helper() int {\n\treturn 1\n}\n",
	})
	kitReq := mcp.CallToolRequest{}
	kitReq.Params.Arguments = map[string]any{"repo": repo.Name, "target": "Helper", "format": "json"}
	kit, err := changeKitHandler(reg)(ctx, kitReq)
	if err != nil || kit.IsError {
		t.Fatalf("change_kit: %v %s", err, resultText(kit))
	}
	if !strings.Contains(resultText(kit), "apply_patch_workspace_file") {
		t.Fatalf("change_kit RNT missing apply_patch")
	}

	dryReq := mcp.CallToolRequest{}
	dryReq.Params.Arguments = map[string]any{
		"repo": repo.Name, "path": "helper.go", "dry_run": true,
		"hunks":  []any{map[string]any{"old_string": "\treturn 1\n", "new_string": "\treturn 2\n"}},
		"format": "json",
	}
	dry, err := applyPatchWorkspaceFileHandler(reg)(ctx, dryReq)
	if err != nil || dry.IsError {
		t.Fatalf("dry_run: %v %s", err, resultText(dry))
	}
	dryText := resultText(dry)
	if !strings.Contains(dryText, "what_next") || !strings.Contains(dryText, "dry_run") {
		t.Fatalf("dry_run what_next: %s", truncateSmoke(dryText, 400))
	}

	applyReq := mcp.CallToolRequest{}
	applyReq.Params.Arguments = map[string]any{
		"repo": repo.Name, "path": "helper.go",
		"hunks":  []any{map[string]any{"old_string": "\treturn 1\n", "new_string": "\treturn 2\n"}},
		"format": "json",
	}
	applied, err := applyPatchWorkspaceFileHandler(reg)(ctx, applyReq)
	if err != nil || applied.IsError {
		t.Fatalf("apply: %v %s", err, resultText(applied))
	}
	if !strings.Contains(resultText(applied), "diagnostics") {
		t.Fatalf("apply RNT missing diagnostics: %s", resultText(applied))
	}

	finReq := mcp.CallToolRequest{}
	finReq.Params.Arguments = map[string]any{
		"repo": repo.Name, "verify_ran": true, "format": "json",
	}
	fin, err := finishCheckHandler(reg)(ctx, finReq)
	if err != nil || fin.IsError {
		t.Fatalf("finish_check: %v %s", err, resultText(fin))
	}
	if !strings.Contains(resultText(fin), "can_claim_done") {
		t.Fatalf("finish_check missing can_claim_done")
	}
}
