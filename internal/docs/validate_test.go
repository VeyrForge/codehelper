package docs

import (
	"context"
	"testing"
)

// mockValidator returns a preset status per URL; unknown URLs are treated live.
type mockValidator struct{ dead map[string]bool }

func (m mockValidator) Validate(_ context.Context, url string) LinkStatus {
	if m.dead[url] {
		return LinkStatus{URL: url, Code: 404, Dead: true}
	}
	return LinkStatus{URL: url, Code: 200, OK: true}
}

func TestValidateLinksDropsDead(t *testing.T) {
	t.Parallel()
	links := []LLMSLink{
		{Title: "Good", URL: "https://x.dev/a"},
		{Title: "Broken", URL: "https://x.dev/404"},
		{Title: "AlsoGood", URL: "https://x.dev/b"},
	}
	v := mockValidator{dead: map[string]bool{"https://x.dev/404": true}}
	kept, checks := validateLinks(context.Background(), v, links)

	if len(kept) != 2 {
		t.Fatalf("kept=%d want 2 (dead link should be dropped)", len(kept))
	}
	for _, l := range kept {
		if l.URL == "https://x.dev/404" {
			t.Errorf("dead link survived: %s", l.URL)
		}
	}
	if len(checks) != 3 {
		t.Errorf("checks=%d want 3 (one per input link)", len(checks))
	}
	var deadSeen bool
	for _, c := range checks {
		if c.URL == "https://x.dev/404" && c.Dead {
			deadSeen = true
		}
	}
	if !deadSeen {
		t.Error("expected the 404 to be reported Dead in checks")
	}
}

func TestValidateLinksNilValidatorPassthrough(t *testing.T) {
	t.Parallel()
	links := []LLMSLink{{Title: "A", URL: "https://x.dev/a"}}
	kept, checks := validateLinks(context.Background(), nil, links)
	if len(kept) != 1 || checks != nil {
		t.Errorf("nil validator should pass links through unchanged: kept=%d checks=%v", len(kept), checks)
	}
}
