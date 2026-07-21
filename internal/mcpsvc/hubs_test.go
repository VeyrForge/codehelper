package mcpsvc

import (
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
)

// TestCompactProjectContextOmitsHubs verifies hubs are a DETAILED-only field: the
// short bootstrap must not carry them (they cost tokens on the once-per-session
// call that every session makes), so compactProjectContext drops them.
func TestCompactProjectContextOmitsHubs(t *testing.T) {
	full := projectContextMCPResponse{
		Repo:        "r",
		Hubs:        []string{"Marshal internal/toon/toon.go:52 ×60"},
		PackageHubs: []string{"internal/graph ×177 ←14 pkgs"},
	}
	got := compactProjectContext(full)
	if len(got.Hubs) != 0 {
		t.Errorf("short mode must omit hubs (detailed-only), got %v", got.Hubs)
	}
	if len(got.PackageHubs) != 0 {
		t.Errorf("short mode must omit package_hubs (detailed-only), got %v", got.PackageHubs)
	}
}

func TestTopPackageDirs(t *testing.T) {
	in := []graph.PackageHub{
		{Dir: "internal/graph", Callers: 180},
		{Dir: "internal/registry", Callers: 107},
		{Dir: "", Callers: 50}, // skipped
		{Dir: "internal/taskstore", Callers: 96},
		{Dir: "internal/usage", Callers: 74},
	}
	got := topPackageDirs(in, 3)
	want := []string{"internal/graph", "internal/registry", "internal/taskstore"}
	if len(got) != 3 {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestCompactProjectContextKeepsArchitecture verifies the top-3 architecture
// teaser survives into the SHORT bootstrap (unlike the full hubs, which are
// detailed-only) — it's a cheap, always-on orientation.
func TestCompactProjectContextKeepsArchitecture(t *testing.T) {
	full := projectContextMCPResponse{
		Repo:             "r",
		Architecture:     []string{"internal/graph", "internal/registry", "internal/taskstore"},
		Hubs:             []string{"Close internal/graph/store.go:53 ×88"},
		VerifyFinishGate: VerifyFinishGateText,
	}
	got := compactProjectContext(full)
	if len(got.Architecture) != 3 {
		t.Errorf("short mode must KEEP the architecture teaser, got %v", got.Architecture)
	}
	if len(got.Hubs) != 0 {
		t.Errorf("short mode must drop the full hubs, got %v", got.Hubs)
	}
	if got.VerifyFinishGate == "" {
		t.Errorf("short mode must KEEP verify_finish_gate")
	}
}

func TestNormalizeInvestigateRecipe(t *testing.T) {
	cases := map[string]string{
		"architecture": "architecture",
		"architect":    "architecture",
		"design":       "architecture",
		"dead_code":    "dead_code",
		"perf":         "perf",
		"security":     "security",
		"":             "",
		"nope":         "",
	}
	for in, want := range cases {
		if got := normalizeInvestigateRecipe(in); got != want {
			t.Errorf("normalizeInvestigateRecipe(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFormatPackageHubs(t *testing.T) {
	in := []graph.PackageHub{
		{Dir: "internal/graph", Callers: 177, FromPkgs: 14},
		{Dir: "", Callers: 5, FromPkgs: 2}, // empty dir dropped
	}
	got := formatPackageHubs(in)
	if len(got) != 1 {
		t.Fatalf("empty-dir hub should be dropped, got %v", got)
	}
	if got[0] != "internal/graph ×177 ←14 pkgs" {
		t.Errorf("unexpected package-hub format: %q", got[0])
	}
}

func TestFormatHubs(t *testing.T) {
	in := []graph.Hub{
		{Name: "Marshal", Path: "internal/toon/toon.go", Line: 52, Callers: 60},
		{Name: "", Path: "x.go", Line: 1, Callers: 3}, // nameless dropped
	}
	got := formatHubs(in)
	if len(got) != 1 {
		t.Fatalf("nameless hub should be dropped, got %v", got)
	}
	if got[0] != "Marshal internal/toon/toon.go:52 ×60" {
		t.Errorf("unexpected hub format: %q", got[0])
	}
}
