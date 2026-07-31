package mcpsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
)

// TestKickoffHealthSmoke_FastAPIGinVue exercises live beds for the residual
// Feature readability fixes: short what_next caution + no same-name double-seed
// on FastAPI; gin placement stays clean; vue still ABSTAINs (no invented /health).
func TestKickoffHealthSmoke_FastAPIGinVue(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	root := findRepoRoot(t)
	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	handlers := AllToolHandlers(reg)
	task := "add GET /health real quick"

	type bedExpect struct {
		name       string
		candidates []string // tried in order
		check      func(t *testing.T, blob string)
	}
	beds := []bedExpect{
		{
			name: "fastapi",
			candidates: []string{
				filepath.Join(root, ".eval-projects", "fastapi"),
				filepath.Join(root, ".testbeds", "active", "fastapi"),
				filepath.Join(root, ".testbeds", "real-oss", "fastapi"),
			},
			check: func(t *testing.T, blob string) {
				t.Helper()
				var out kickoffResponse
				if err := json.Unmarshal([]byte(blob), &out); err != nil {
					t.Fatalf("fastapi decode: %v\n%s", err, truncateSmoke(blob, 600))
				}
				names := map[string]int{}
				for _, c := range out.ReuseCandidates {
					names[c.Name]++
				}
				for n, ncount := range names {
					if (n == "placement_fastapi_example" || n == "route_health") && ncount > 1 {
						t.Fatalf("fastapi double-seeded %s x%d in %+v", n, ncount, out.ReuseCandidates)
					}
				}
				if len(out.ReuseCandidates) == 0 {
					t.Fatalf("fastapi expected placement/route reuse, got empty; abstain=%q", out.Abstain)
				}
				top := out.ReuseCandidates[0].Name
				if top != "placement_fastapi_example" && top != "route_health" &&
					!strings.Contains(strings.ToLower(top), "health") &&
					!strings.Contains(strings.ToLower(out.ReuseCandidates[0].Loc), "docs_src") {
					t.Fatalf("fastapi reuse#1 unexpected: %+v", out.ReuseCandidates[0])
				}
				wn := out.WhatNext
				if strings.Contains(wn, "SPARSE CALL GRAPH (call_graph_confidence=LOW)") ||
					strings.Contains(wn, "MEDIUM CALL GRAPH:") {
					t.Fatalf("fastapi what_next still uses heavy prefix jargon: %q", wn)
				}
				t.Logf("fastapi reuse#1=%s what_next=%q", top, wn)
			},
		},
		{
			name: "gin",
			candidates: []string{
				filepath.Join(root, ".testbeds", "active", "gin"),
				filepath.Join(root, ".eval-projects", "gin"),
			},
			check: func(t *testing.T, blob string) {
				t.Helper()
				var out kickoffResponse
				if err := json.Unmarshal([]byte(blob), &out); err != nil {
					t.Fatalf("gin decode: %v\n%s", err, truncateSmoke(blob, 600))
				}
				if out.Abstain != "" && len(out.ReuseCandidates) == 0 {
					t.Fatalf("gin must not abstain as non-HTTP: %q", out.Abstain)
				}
				if len(out.ReuseCandidates) == 0 {
					t.Fatal("gin expected health/placement reuse")
				}
				seen := map[string]int{}
				for _, c := range out.ReuseCandidates {
					seen[c.Name]++
				}
				for n, c := range seen {
					if c > 1 {
						t.Fatalf("gin double-seeded name %s x%d", n, c)
					}
				}
				top := out.ReuseCandidates[0]
				low := strings.ToLower(top.Name + " " + top.Loc)
				if !strings.Contains(low, "health") && !strings.Contains(low, "placement") &&
					!strings.Contains(low, "engine") && !strings.Contains(low, "utils") {
					t.Fatalf("gin reuse#1 unexpected: %+v", top)
				}
				wn := out.WhatNext
				if strings.Contains(wn, "SPARSE CALL GRAPH (call_graph_confidence=LOW)") ||
					strings.Contains(wn, "MEDIUM CALL GRAPH:") {
					t.Fatalf("gin what_next still uses heavy prefix jargon: %q", wn)
				}
				t.Logf("gin reuse#1=%s what_next=%q", top.Name, wn)
			},
		},
		{
			name: "vue",
			candidates: []string{
				filepath.Join(root, ".testbeds", "active", "vue"),
				filepath.Join(root, ".eval-projects", "vue"),
			},
			check: func(t *testing.T, blob string) {
				t.Helper()
				var out kickoffResponse
				if err := json.Unmarshal([]byte(blob), &out); err != nil {
					t.Fatalf("vue decode: %v\n%s", err, truncateSmoke(blob, 600))
				}
				if out.Abstain == "" {
					t.Fatalf("vue expected ABSTAIN, got reuse=%+v what_next=%q", out.ReuseCandidates, out.WhatNext)
				}
				wn := strings.ToLower(out.WhatNext)
				if !strings.Contains(wn, "abstain") || !strings.Contains(wn, "do not invent") {
					t.Fatalf("vue what_next must be honest ABSTAIN (do not invent), got %q", out.WhatNext)
				}
				if strings.Contains(wn, "change_kit target=<top reuse") {
					t.Fatalf("vue must not invent /health via change_kit, got %q", out.WhatNext)
				}
				for _, c := range out.ReuseCandidates {
					if strings.HasPrefix(strings.ToLower(c.Name), "route_health") ||
						strings.HasPrefix(strings.ToLower(c.Name), "placement_") {
						t.Fatalf("vue must not seed HTTP health placement, got %+v", c)
					}
				}
				t.Logf("vue abstain=%q what_next=%q", out.Abstain, out.WhatNext)
			},
		},
	}

	ran := 0
	for _, b := range beds {
		b := b
		var bedRoot string
		for _, cand := range b.candidates {
			cand = resolveBedRoot(cand)
			if _, err := os.Stat(filepath.Join(cand, ".codehelper")); err == nil {
				bedRoot = cand
				break
			}
		}
		if bedRoot == "" {
			t.Logf("skip %s: no indexed bed", b.name)
			continue
		}
		_ = reg.Upsert(b.name, bedRoot, "", 2)
		ctx := workspacectx.WithRoots(bedRoot)
		blob := pairedCall(ctx, handlers, "kickoff", map[string]any{
			"task": task, "format": "json", "role": "feature",
			"sections": "orient,reuse,findings", "repo": b.name,
		})
		if strings.TrimSpace(blob) == "" || strings.Contains(strings.ToLower(blob), "could not be indexed") {
			t.Fatalf("%s kickoff failed: %s", b.name, truncateSmoke(blob, 400))
		}
		ran++
		t.Run(b.name, func(t *testing.T) { b.check(t, blob) })
	}
	if ran == 0 {
		t.Skip("no fastapi/gin/vue indexed beds for smoke")
	}
}
