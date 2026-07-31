package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildAgentBriefCapsLists(t *testing.T) {
	pack := ContextPack{
		Symbols:      []string{"A", "B", "C", "D", "E", "F", "G"},
		Files:        []string{"f1.go", "f2.go", "f3.go", "f4.go", "f5.go", "f6.go", "f7.go", "f8.go", "f9.go"},
		Verification: []string{"go test ./...", "make lint"},
	}
	brief := BuildAgentBrief("explain auth", Plan{Intent: IntentExplain, Workflow: WorkflowExplainCode, Confidence: 0.9}, pack, []CompactTrace{
		{Step: 1, Tool: "query", Result: "3 hits"},
	}, Constraints{}, TierFast)
	if strings.Contains(brief, "`G`") {
		t.Fatal("expected symbols capped")
	}
	if strings.Contains(brief, "f9.go") {
		t.Fatal("expected files capped")
	}
	if !strings.Contains(brief, "Verify:") {
		t.Fatal("expected verification line")
	}
}

func TestAgentPayloadOmitsHeavyFieldsByDefault(t *testing.T) {
	res := &Result{
		RunID: "run1", Status: "complete", Workflow: WorkflowExplainCode, Intent: IntentExplain,
		Confidence: 0.8, AgentBrief: "brief", AnswerMarkdown: "long markdown",
		ContextPack: ContextPack{
			Files:       []string{"a.go"},
			Steps:       []string{"Extend Foo via change_kit target=Foo", "next: context name=Foo", "next: impact target=Foo"},
			NextQueries: []string{"context name=Foo", "impact target=Foo"},
		},
	}
	payload, ok := res.AgentPayload(false).(SlimAgentPayload)
	if !ok {
		t.Fatalf("expected SlimAgentPayload, got %T", res.AgentPayload(false))
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatal(err)
	}
	if _, ok := asMap["answer_markdown"]; ok {
		t.Fatal("answer_markdown should be omitted in slim payload")
	}
	if _, ok := asMap["context_pack"]; ok {
		t.Fatal("context_pack should be omitted in slim payload")
	}
	if payload.AgentBrief != "brief" {
		t.Fatal("agent_brief missing")
	}
	if payload.MCPParamKeys != AgentMCPParamKeys {
		t.Fatalf("mcp_param_keys missing/wrong: %v", payload.MCPParamKeys)
	}
	if payload.WhatNext == "" {
		t.Fatal("expected what_next in slim payload")
	}
	if len(payload.NextQueries) < 1 {
		t.Fatalf("expected next_queries, got %v", payload.NextQueries)
	}
	if !strings.Contains(payload.Note, "detail=true") {
		t.Fatalf("note should say detail=true, got %q", payload.Note)
	}
	if payload.RunID != "run1" {
		t.Fatalf("run_id=%q", payload.RunID)
	}
	full := res.AgentPayload(true)
	if _, ok := full.(*Result); !ok {
		t.Fatal("full payload should be Result")
	}
}

func TestSlimAgentPayloadTOONStartsWithRunID(t *testing.T) {
	res := &Result{
		RunID: "run_abc", Status: "complete", Workflow: WorkflowExplainCode, Intent: IntentExplain,
		Confidence: 0.8, AgentBrief: "brief",
		ContextPack: ContextPack{Steps: []string{"Do the thing"}},
	}
	// Import cycle avoided: encode via json then check key order from struct tags
	b, err := json.Marshal(res.AgentPayload(false))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	idxRun := strings.Index(s, `"run_id"`)
	idxBrief := strings.Index(s, `"agent_brief"`)
	if idxRun < 0 || idxBrief < 0 || idxRun > idxBrief {
		t.Fatalf("run_id should precede agent_brief in JSON/TOON stream: %s", s)
	}
}

func TestBuildAgentBriefIncludesParamKeys(t *testing.T) {
	brief := BuildAgentBrief("add health", Plan{Intent: IntentFeature, Workflow: WorkflowFeatureScope, Confidence: 0.8},
		ContextPack{Symbols: []string{"Health"}, Steps: []string{"Extend Health", "next: change_kit target=Health"}},
		nil, Constraints{}, TierFast)
	if !strings.Contains(brief, "Param keys:") || !strings.Contains(brief, "change_kit→target") {
		t.Fatalf("expected param keys in brief: %s", brief)
	}
	if !strings.Contains(brief, "context name=") {
		t.Fatalf("expected context name= tip: %s", brief)
	}
}

func TestAgentFacingTokensUsesSlimPayload(t *testing.T) {
	res := &Result{
		RunID: "run1", Status: "complete", AgentBrief: strings.Repeat("x", 400),
		ContextPack:    ContextPack{Files: []string{"big.go"}},
		AnswerMarkdown: strings.Repeat("y", 8000),
	}
	slim := AgentFacingTokens(res)
	fullBytes, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if slim >= estimateTokens(len(fullBytes)) {
		t.Fatalf("slim=%d should be less than full=%d", slim, estimateTokens(len(fullBytes)))
	}
}
