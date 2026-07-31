package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ossPinManifest mirrors scripts/prepare-oss-testbeds.sh → oss-testbed-pins.json
// (also copied into bench-comparison-scaffold.json as oss_testbeds).
type ossPinManifest struct {
	GeneratedAt   string `json:"generated_at"`
	SchemaVersion int    `json:"schema_version"`
	Harness       string `json:"harness"`
	Beds          []struct {
		Bed         string `json:"bed"`
		URL         string `json:"url"`
		PinnedSHA   string `json:"pinned_sha"`
		CommitSHA   string `json:"commit_sha"`
		PinMatch    string `json:"pin_match"`
		Source      string `json:"source"`
		ColdIndexMs *int   `json:"cold_index_ms"`
		WarmIndexMs *int   `json:"warm_index_ms"`
	} `json:"beds"`
}

func TestOSSTestbedPinsExampleSchema(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(root, "testdata", "oss-testbed-pins", "example.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read example pins: %v", err)
	}
	var doc ossPinManifest
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.SchemaVersion != 1 {
		t.Fatalf("schema_version=%d want 1", doc.SchemaVersion)
	}
	if len(doc.Beds) < 1 || doc.Beds[0].Bed == "" || doc.Beds[0].CommitSHA == "" {
		t.Fatalf("invalid beds: %+v", doc.Beds)
	}
	if doc.Beds[0].PinnedSHA == "" || doc.Beds[0].PinMatch == "" {
		t.Fatalf("missing pin fields: %+v", doc.Beds[0])
	}
}

func TestDefaultMultiBedCoverage(t *testing.T) {
	cov := DefaultMultiBedCoverage()
	if len(cov) < 18 {
		t.Fatalf("expected ≥18 beds (12 lite + extensions), got %d", len(cov))
	}
	tiers := map[BedTier]int{}
	seen := map[string]bool{}
	stubs := 0
	for _, b := range cov {
		if b.Bed == "" || len(b.Kinds) == 0 {
			t.Fatalf("invalid probe: %+v", b)
		}
		if seen[b.Bed] {
			t.Fatalf("duplicate bed %s", b.Bed)
		}
		seen[b.Bed] = true
		tiers[b.Tier]++
		if b.Source == BedSourceStub {
			stubs++
		}
	}
	if tiers[BedTierStrong] < 2 || tiers[BedTierMedium] < 2 || tiers[BedTierWeak] < 1 {
		t.Fatalf("tier balance weak: %+v", tiers)
	}
	for _, name := range []string{"csharp", "unity", "godot", "unreal", "cpp", "swift", "swiftui", "elixir", "phoenix", "dart", "flutter", "react-native", "capacitor", "zig", "solidity", "clojure", "erlang", "fsharp", "r", "perl", "ocaml", "haskell", "shaders", "terraform", "protobuf", "prisma", "typeorm", "drizzle", "devops", "kubernetes", "ansible", "powershell", "lua", "scala", "kotlin", "angular", "nextjs", "nuxt", "deno", "bun", "cloudflare-worker", "multi-repo-a", "multi-repo-b", "laravel", "symfony", "wordpress", "sinatra", "rails", "nest", "express", "fastify", "hono", "svelte", "vue", "astro", "mdx", "spring", "hibernate", "fastapi", "flask", "djangorest", "echo", "chi", "beego"} {
		if !seen[name] {
			t.Fatalf("missing extended/stub bed %q", name)
		}
	}
	if stubs < 59 {
		t.Fatalf("expected ≥59 stub-source beds, got %d", stubs)
	}
	ci := CIMinimalBedNames()
	if len(ci) != 3 || ci[0] != "gin" || ci[1] != "nest" || ci[2] != "express" {
		t.Fatalf("CI minimal beds drifted: %v", ci)
	}
	stubNames := StubBedNames()
	if len(stubNames) != stubs {
		t.Fatalf("StubBedNames=%d vs stub count=%d", len(stubNames), stubs)
	}
}
