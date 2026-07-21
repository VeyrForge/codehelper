package review

import (
	"os"
	"path/filepath"
	"strings"
)

// IsCodeSourceFile reports whether path points at first-party source code
// that meaningfully benefits from behavioral regression tests or contract
// scrutiny. The strict review tools use it to drop noise like lockfiles,
// dotfiles, config files, compiled outputs, and static assets so reports
// stay actionable instead of being dominated by "add tests for tsconfig".
func IsCodeSourceFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	p := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(p)

	for _, dir := range []string{
		"vendor/", "node_modules/", "/vendor/", "/node_modules/",
		"/out/", "/dist/", "/build/", "/target/", "/bin/",
		"/.next/", "/.nuxt/", "/.svelte-kit/", "/coverage/",
		"/.cache/", "/.parcel-cache/",
	} {
		if strings.Contains(p, dir) {
			return false
		}
	}
	for _, prefix := range []string{"out/", "dist/", "build/", "target/", "bin/", "coverage/"} {
		if strings.HasPrefix(p, prefix) {
			return false
		}
	}

	if strings.HasPrefix(base, ".") {
		return false
	}
	switch base {
	case "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "shrinkwrap.yaml",
		"composer.lock", "gemfile.lock", "poetry.lock", "cargo.lock", "go.sum",
		"package.json", "tsconfig.json", "jsconfig.json", "tsconfig.base.json":
		return false
	}
	if strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, ".lockb") {
		return false
	}

	switch strings.ToLower(filepath.Ext(p)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".py", ".rb", ".php", ".java", ".kt", ".kts", ".swift", ".scala",
		".rs", ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh",
		".cs", ".m", ".mm", ".dart", ".ex", ".exs", ".erl", ".hs",
		".ml", ".mli", ".fs", ".fsx", ".clj", ".cljs", ".lua",
		".sh", ".bash", ".zsh", ".ps1":
		return true
	}
	return false
}

// IsTestPath reports whether path looks like a test file rather than
// production code. Tests must never be flagged as public contracts or as
// "missing tests for themselves".
func IsTestPath(path string) bool {
	p := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(p)

	if strings.HasSuffix(p, "_test.go") {
		return true
	}
	for _, suf := range []string{
		".test.ts", ".test.tsx", ".test.js", ".test.jsx", ".test.mjs",
		".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx", ".spec.mjs",
	} {
		if strings.HasSuffix(p, suf) {
			return true
		}
	}
	if strings.HasSuffix(p, "_test.py") || strings.HasPrefix(base, "test_") {
		return true
	}
	for _, seg := range []string{
		"/test/", "/tests/", "/__tests__/", "/spec/", "/specs/",
	} {
		if strings.Contains(p, seg) {
			return true
		}
	}
	if strings.HasPrefix(p, "test/") || strings.HasPrefix(p, "tests/") {
		return true
	}
	return false
}

// IsSecondaryNoisePath reports demo/tutorial/fixture/acceptance trees that
// drown hubs, kickoff reuse, and centrality for library monorepos. Files under
// these paths remain indexed and searchable; orientation tools should demote them.
func IsSecondaryNoisePath(path string) bool {
	p := strings.ToLower(filepath.ToSlash(path))
	for _, seg := range []string{
		"/docs_src/", "/sample/", "/samples/", "/examples/", "/example/",
		"/integration/", "/fixtures/", "/fixture/", "/testdata/",
		"/_expected/", "/benchmarking/", "/playground/", "/playgrounds/",
		"/test/acceptance/", "/acceptance/", "/.github/",
	} {
		if strings.Contains(p, seg) {
			return true
		}
	}
	for _, prefix := range []string{
		"docs_src/", "sample/", "samples/", "examples/", "example/",
		"integration/", "fixtures/", "fixture/", "testdata/",
		"benchmarking/", "playground/", "playgrounds/", "test/acceptance/",
		".github/",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	base := filepath.Base(p)
	if strings.HasPrefix(base, "expected.") || strings.Contains(base, "_expected") {
		return true
	}
	return false
}

// HasSiblingTestFile reports whether a source file already has a colocated
// test file. We use it to avoid emitting noisy "add tests for X" actions
// when tests already exist next to X (e.g., perf_guard.go + perf_guard_test.go
// or perf_guard.go + guards_diff_test.go in the same directory).
func HasSiblingTestFile(repoRoot, path string) bool {
	if strings.TrimSpace(repoRoot) == "" || strings.TrimSpace(path) == "" {
		return false
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(repoRoot, filepath.FromSlash(path))
	}
	dir := filepath.Dir(abs)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	// 1) Direct sibling match: foo.go ↔ foo_test.go, foo.ts ↔ foo.test.ts, etc.
	directCandidates := []string{}
	switch ext {
	case ".go":
		directCandidates = append(directCandidates, base+"_test.go")
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		directCandidates = append(directCandidates,
			base+".test"+ext, base+".spec"+ext,
		)
	case ".py":
		directCandidates = append(directCandidates, "test_"+base+".py", base+"_test.py")
	case ".rb":
		directCandidates = append(directCandidates, base+"_spec.rb", base+"_test.rb")
	case ".php":
		directCandidates = append(directCandidates, base+"Test.php")
	}
	for _, e := range entries {
		name := e.Name()
		for _, want := range directCandidates {
			if strings.EqualFold(name, want) {
				return true
			}
		}
	}

	// 2) Package-level: for Go, any *_test.go in the same directory provides
	// coverage at the package level; same for Python tests/ or __tests__/.
	if ext == ".go" {
		for _, e := range entries {
			if strings.HasSuffix(strings.ToLower(e.Name()), "_test.go") {
				return true
			}
		}
	}
	// 3) Sibling __tests__/ folder mirroring this file (JS/TS conventions).
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n := strings.ToLower(e.Name())
		if n == "__tests__" || n == "tests" || n == "test" {
			sub := filepath.Join(dir, e.Name())
			subEntries, err := os.ReadDir(sub)
			if err != nil {
				continue
			}
			for _, s := range subEntries {
				sn := strings.ToLower(s.Name())
				if strings.Contains(sn, strings.ToLower(base)) {
					return true
				}
			}
		}
	}
	return false
}

// IsBehavioralSymbolKind reports whether a SymbolKind string represents
// code with behavior worth covering with tests. Structs, type aliases,
// enums and namespaces are data/declarative — flagging them as needing
// tests is noise.
func IsBehavioralSymbolKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "function", "method":
		return true
	}
	return false
}
