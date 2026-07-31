package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/parser"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestDetectImportAliases_TSConfigWithCommentsAndExtends(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "tsconfig.base.json", `{
  "compilerOptions": {
    // shared across the monorepo
    "baseUrl": ".",
    "paths": {
      "@shared/*": ["packages/shared/src/*"],
    }
  }
}`)
	writeFile(t, root, "tsconfig.json", `{
  /* app config */
  "extends": "./tsconfig.base.json",
  "compilerOptions": {
    "baseUrl": ".",
    "paths": {
      "@/*": ["src/*"],
      "@app": ["src/app/index.ts"]
    }
  }
}`)
	aliases := DetectImportAliases(root)
	if aliases == nil {
		t.Fatal("expected aliases from tsconfig")
	}
	for _, tc := range []struct{ spec, want string }{
		{"@/lib/db", "src/lib/db"},
		{"@/components/Button", "src/components/Button"},
		{"@shared/types", "packages/shared/src/types"},
		{"@app", "src/app/index.ts"},
	} {
		got := ResolveAliasSpecifier(tc.spec, aliases)
		found := false
		for _, g := range got {
			if g == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("ResolveAliasSpecifier(%q) = %v, want to contain %q", tc.spec, got, tc.want)
		}
	}
	// Bare packages and relative paths are never rewritten.
	for _, spec := range []string{"react", "@scope/pkg", "./local", "../up"} {
		if got := ResolveAliasSpecifier(spec, aliases); len(got) != 0 {
			t.Errorf("ResolveAliasSpecifier(%q) should be empty; got %v", spec, got)
		}
	}
}

func TestDetectImportAliases_ViteAndPackageImports(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "vite.config.ts", `import { defineConfig } from "vite";
export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./resources/js"),
      "~~": path.resolve(__dirname, "./resources"),
    },
  },
});`)
	writeFile(t, root, "package.json", `{
  "name": "app",
  "imports": { "#cache/*": "./src/infra/cache/*" }
}`)
	aliases := DetectImportAliases(root)
	for _, tc := range []struct{ spec, want string }{
		{"@/pages/Home", "resources/js/pages/Home"},
		{"#cache/redis", "src/infra/cache/redis"},
	} {
		got := ResolveAliasSpecifier(tc.spec, aliases)
		found := false
		for _, g := range got {
			if g == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("ResolveAliasSpecifier(%q) = %v, want to contain %q", tc.spec, got, tc.want)
		}
	}
}

func TestDetectImportAliases_ConventionalSrcRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "src/lib/db.ts", "export const db = 1;\n")
	aliases := DetectImportAliases(root)
	got := ResolveAliasSpecifier("~/lib/db", aliases)
	if len(got) == 0 || got[0] != "src/lib/db" {
		t.Errorf("conventional ~/ alias not applied: %v", got)
	}
}

func TestResolveAliasSpecifier_MultiRootProbesFilesystem(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Both conventional roots exist; only resources/js has the imported module.
	writeFile(t, root, "src/.keep", "")
	writeFile(t, root, "resources/js/pages/Home.tsx", "export default function Home() { return null }\n")
	aliases := DetectImportAliases(root)
	got := ResolveAliasSpecifier("@/pages/Home", aliases)
	if len(got) != 1 || got[0] != "resources/js/pages/Home" {
		t.Fatalf("expected sole FS hit resources/js/pages/Home, got %v (aliases=%v)", got, aliases["@/"])
	}

	// Flip: only src has the module → pick src even though resources/js is listed.
	root2 := t.TempDir()
	writeFile(t, root2, "src/pages/Home.ts", "export {}\n")
	writeFile(t, root2, "resources/js/.keep", "")
	aliases2 := DetectImportAliases(root2)
	got2 := ResolveAliasSpecifier("@/pages/Home", aliases2)
	if len(got2) != 1 || got2[0] != "src/pages/Home" {
		t.Fatalf("expected sole FS hit src/pages/Home, got %v", got2)
	}
}

func TestResolveAliasSpecifier_MultiRootFallbackWhenNothingExists(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "src/.keep", "")
	writeFile(t, root, "resources/js/.keep", "")
	aliases := DetectImportAliases(root)
	got := ResolveAliasSpecifier("@/missing/mod", aliases)
	if len(got) < 2 {
		t.Fatalf("when no candidate exists, keep all fallbacks; got %v", got)
	}
	want := map[string]bool{"src/missing/mod": true, "resources/js/missing/mod": true}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected candidate %q in %v", g, got)
		}
	}
}

func TestResolveAliasSpecifier_TSConfigFallbackProbesFirstHit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "tsconfig.json", `{
  "compilerOptions": {
    "baseUrl": ".",
    "paths": { "@/*": ["src/*", "legacy/*"] }
  }
}`)
	writeFile(t, root, "legacy/util.ts", "export const util = 1;\n")
	// src/ exists but does not contain util — first listed path misses, second hits.
	writeFile(t, root, "src/.keep", "")
	aliases := DetectImportAliases(root)
	got := ResolveAliasSpecifier("@/util", aliases)
	if len(got) != 1 || got[0] != "legacy/util" {
		t.Fatalf("expected first existing fallback legacy/util, got %v", got)
	}
}

func TestExpandAliasImportEdges_MultiRootLinksOnlyExisting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "src/.keep", "")
	writeFile(t, root, "resources/js/lib/db.ts", "export const db = 1;\n")
	aliases := DetectImportAliases(root)
	repoID, rel := "repo", "resources/js/pages/users.ts"
	raw := parser.ModuleNodeID(repoID, "@/lib/db")
	edges := []types.Reference{{
		ID:       parser.EdgeID(repoID, parser.FileNodeID(repoID, rel), raw, "imports"),
		RepoID:   repoID,
		Kind:     types.RefKindImports,
		SourceID: parser.FileNodeID(repoID, rel),
		TargetID: raw, Confidence: 0.9,
	}}
	out := ExpandAliasImportEdges(repoID, rel, edges, aliases)
	targets := map[string]bool{}
	for _, e := range out {
		targets[e.TargetID] = true
	}
	if !targets[raw] {
		t.Error("original alias import edge must be preserved")
	}
	want := parser.ModuleNodeID(repoID, "resources/js/lib/db")
	if !targets[want] {
		t.Errorf("missing expanded edge %q; got %#v", want, targets)
	}
	bogus := parser.ModuleNodeID(repoID, "src/lib/db")
	if targets[bogus] {
		t.Errorf("must not link non-existing multi-root candidate %q", bogus)
	}
	if len(out) != 2 {
		t.Fatalf("expected original + one expanded edge, got %d: %#v", len(out), out)
	}
}

func TestExpandAliasImportEdges_AddsResolvedTargetKeepsOriginal(t *testing.T) {
	t.Parallel()
	aliases := map[string][]string{"@/": {"src/"}}
	repoID, rel := "repo", "src/pages/users.ts"
	raw := parser.ModuleNodeID(repoID, "@/lib/db")
	edges := []types.Reference{{
		ID:       parser.EdgeID(repoID, parser.FileNodeID(repoID, rel), raw, "imports"),
		RepoID:   repoID,
		Kind:     types.RefKindImports,
		SourceID: parser.FileNodeID(repoID, rel),
		TargetID: raw, Confidence: 0.9,
	}}
	out := ExpandAliasImportEdges(repoID, rel, edges, aliases)
	if len(out) != 2 {
		t.Fatalf("expected original + expanded edge, got %d: %#v", len(out), out)
	}
	targets := map[string]bool{}
	for _, e := range out {
		targets[e.TargetID] = true
	}
	if !targets[raw] {
		t.Error("original alias import edge must be preserved")
	}
	if want := parser.ModuleNodeID(repoID, "src/lib/db"); !targets[want] {
		t.Errorf("missing expanded edge %q; got %#v", want, targets)
	}
	// Idempotent: a second pass adds nothing.
	if again := ExpandAliasImportEdges(repoID, rel, out, aliases); len(again) != len(out) {
		t.Errorf("expansion is not idempotent: %d → %d", len(out), len(again))
	}
}

func TestExpandAliasImportEdges_NoAliasesIsNoop(t *testing.T) {
	t.Parallel()
	edges := []types.Reference{{Kind: types.RefKindImports, TargetID: parser.ModuleNodeID("r", "@/x")}}
	if got := ExpandAliasImportEdges("r", "a.ts", edges, nil); len(got) != 1 {
		t.Errorf("no aliases should be a no-op; got %#v", got)
	}
}

func TestRelaxedJSON(t *testing.T) {
	t.Parallel()
	in := `{
  // line comment with "quotes"
  "a": "keep // this",
  /* block
     comment */
  "b": [1, 2,],
}`
	out := relaxedJSON(in)
	if want := `"keep // this"`; !contains(out, want) {
		t.Errorf("string contents must survive: %q", out)
	}
	if contains(out, "line comment") || contains(out, "block") {
		t.Errorf("comments not stripped: %q", out)
	}
	if contains(out, ",}") || contains(out, ",]") {
		t.Errorf("trailing commas not stripped: %q", out)
	}
}

func TestLanguageFromExtBlade(t *testing.T) {
	t.Parallel()
	if got := languageFromExt("resources/views/home.blade.php"); got != "blade" {
		t.Errorf("languageFromExt(blade) = %q want blade", got)
	}
	if got := languageFromExt("app/Models/User.php"); got != "php" {
		t.Errorf("languageFromExt(php) = %q want php", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
