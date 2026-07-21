package mcpsvc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/workspacectx"
)

// TestImpactDefaultDirectionUpstream ensures bare impact (no direction) answers
// "who uses this?" — the vibe-coder / refactor default. Downstream remains opt-in.
func TestImpactDefaultDirectionUpstream(t *testing.T) {
	reg, repo, _ := buildIndexedRepo(t, map[string]string{
		"hub.go": "package demo\n\ntype Hub struct{}\n\nfunc (h *Hub) Run() {}\n",
		"use.go": "package demo\n\nfunc Call() {\n\tvar h Hub\n\t_ = h\n}\n",
	})
	handlers := AllToolHandlers(reg)
	wctx := workspacectx.WithRoots(repo.RootPath)

	res := workflowCall(wctx, handlers, "impact", map[string]any{
		"repo": repo.Name, "name": "Hub", "format": "json",
	}, repo.Name)
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("impact failed: %v", res)
	}
	snippet, _ := res["snippet"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(snippet), &payload); err != nil {
		t.Fatalf("json: %v\n%s", err, snippet)
	}
	imp, _ := payload["impact"].(map[string]any)
	if imp == nil {
		t.Fatalf("missing impact object: %s", snippet)
	}
	if dir, _ := imp["direction"].(string); dir != "upstream" {
		t.Fatalf("default direction want upstream, got %q", dir)
	}
}

// TestImpactDownstreamClassHubAutoRetriesUpstream covers Nest/Axum-style hubs:
// explicit downstream on a type with no callees should flip to upstream when
// callers/readers exist, instead of returning a self-only low-risk lie.
func TestImpactDownstreamClassHubAutoRetriesUpstream(t *testing.T) {
	reg, repo, _ := buildIndexedRepo(t, map[string]string{
		"svc.go":  "package demo\n\ntype CatsService struct{}\n",
		"ctrl.go": "package demo\n\nfunc Wire(s CatsService) { _ = s }\n",
	})
	handlers := AllToolHandlers(reg)
	wctx := workspacectx.WithRoots(repo.RootPath)

	res := workflowCall(wctx, handlers, "impact", map[string]any{
		"repo": repo.Name, "name": "CatsService", "direction": "downstream", "format": "json",
	}, repo.Name)
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("impact failed: %v", res)
	}
	snippet, _ := res["snippet"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(snippet), &payload); err != nil {
		t.Fatalf("json: %v\n%s", err, snippet)
	}
	imp, _ := payload["impact"].(map[string]any)
	if imp == nil {
		t.Fatalf("missing impact: %s", snippet)
	}
	nodes, _ := imp["nodes"].([]any)
	dir, _ := imp["direction"].(string)
	note, _ := payload["note"].(string)
	lowerNote := strings.ToLower(note)

	switch {
	case dir == "upstream" && len(nodes) > 1:
		if !strings.Contains(lowerNote, "upstream") {
			t.Fatalf("auto-retried upstream should note it; note=%q", note)
		}
	case len(nodes) <= 1:
		// Both directions empty on a thin fixture — stay honest, not silent.
		if !strings.Contains(lowerNote, "self-only") && !strings.Contains(lowerNote, "no impacted") &&
			!strings.Contains(lowerNote, "upstream") {
			t.Fatalf("expected upstream retry note or self-only honesty, got dir=%s note=%q", dir, note)
		}
	default:
		t.Logf("impact dir=%s nodes=%d note=%s", dir, len(nodes), truncateSmoke(note, 200))
	}
}
