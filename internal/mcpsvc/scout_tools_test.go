package mcpsvc

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestApplySparseWhatNextCaution_JuniorPrefix(t *testing.T) {
	base := "Extend `Foo` via change_kit target=Foo."
	got := applySparseWhatNextCaution(base, "LOW — sparse call graph (0 call edges)")
	if !strings.HasPrefix(got, "Sparse call graph") {
		t.Fatalf("expected short Sparse call graph prefix, got %q", got)
	}
	if strings.Contains(got, "call_graph_confidence=LOW") || strings.Contains(got, "do NOT treat") {
		t.Fatalf("prefix still uses heavy agent jargon: %q", got)
	}
	if !strings.Contains(got, "empty blast_radius") || !strings.Contains(got, base) {
		t.Fatalf("must keep honesty + original what_next: %q", got)
	}
	// Idempotent when already cautioned.
	if again := applySparseWhatNextCaution(got, "LOW — sparse"); again != got {
		t.Fatalf("expected idempotent sparse caution, got %q", again)
	}
	if plain := applySparseWhatNextCaution(base, "HIGH — dense"); plain != base {
		t.Fatalf("HIGH must not prefix: %q", plain)
	}
}

func TestApplyMediumWhatNextCaution_JuniorPrefix(t *testing.T) {
	base := "Extend `Bar` via change_kit target=Bar."
	got := applyMediumWhatNextCaution(base, "MEDIUM — moderate call density")
	if !strings.HasPrefix(got, "Thinner call graph") {
		t.Fatalf("expected short Thinner call graph prefix, got %q", got)
	}
	if strings.Contains(got, "MEDIUM CALL GRAPH:") || strings.Contains(got, "textual search") {
		t.Fatalf("prefix still uses old heavy copy: %q", got)
	}
	if !strings.Contains(got, "empty fanout") || !strings.Contains(got, base) {
		t.Fatalf("must keep honesty + original what_next: %q", got)
	}
	if again := applyMediumWhatNextCaution(got, "MEDIUM — moderate"); again != got {
		t.Fatalf("expected idempotent medium caution, got %q", again)
	}
}

// scout should surface a real call site of the top reuse candidate so the agent
// can copy the calling convention.
func TestScoutHandler_UsageExample(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"store.go":  "package x\n\n// AcquireLock takes the global lock.\nfunc AcquireLock() error { return nil }\n",
		"caller.go": "package x\n\nfunc bootstrap() error {\n\tif err := AcquireLock(); err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n",
	})

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"repo":   repo.Name,
		"task":   "acquire the global lock",
		"format": "json",
	}
	res, err := scoutHandler(reg)(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	out := decodeStructured[scoutResponse](t, res)
	if len(out.ReuseCandidates) == 0 {
		t.Fatal("expected reuse candidates")
	}
	if out.UsageOfTop == nil {
		t.Fatalf("expected a usage example for the top candidate; got none (candidates=%+v)", out.ReuseCandidates)
	}
	if out.UsageOfTop.Caller != "bootstrap" {
		t.Errorf("expected caller 'bootstrap', got %q", out.UsageOfTop.Caller)
	}
	if got := out.UsageOfTop.Code; got == "" || !strings.Contains(got, "AcquireLock()") {
		t.Errorf("usage code should show the call site, got %q", got)
	}
	if out.UsageOfTop.Loc == "" {
		t.Error("usage example missing location")
	}
}
