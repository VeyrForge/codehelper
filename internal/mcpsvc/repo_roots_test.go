package mcpsvc

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
	"github.com/mark3labs/mcp-go/mcp"
)

// fakeSessionSeq hands each fake session a unique ID, mirroring production where
// every client connection gets a distinct session — so the per-session roots
// cache never leaks roots from one test into the next.
var fakeSessionSeq atomic.Int64

type fakeRootsSession struct {
	roots []mcp.Root
	ch    chan mcp.JSONRPCNotification
	id    string
}

func (s *fakeRootsSession) Initialize() {}

func (s *fakeRootsSession) Initialized() bool { return true }

func (s *fakeRootsSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	if s.ch == nil {
		s.ch = make(chan mcp.JSONRPCNotification, 1)
	}
	return s.ch
}

func (s *fakeRootsSession) SessionID() string {
	if s.id == "" {
		s.id = "test-session-" + strconv.FormatInt(fakeSessionSeq.Add(1), 10)
	}
	return s.id
}

func (s *fakeRootsSession) ListRoots(ctx context.Context, req mcp.ListRootsRequest) (*mcp.ListRootsResult, error) {
	_ = ctx
	_ = req
	return &mcp.ListRootsResult{Roots: s.roots}, nil
}

func testRegistryWithRoots(repoRoot, otherRoot string) *registry.Registry {
	return &registry.Registry{Entries: map[string]registry.Entry{
		"target": {Name: "target", RootPath: repoRoot, SchemaVer: 2},
		"other":  {Name: "other", RootPath: otherRoot, SchemaVer: 2},
	}}
}

func fileURIForTestPath(p string) string {
	slash := filepath.ToSlash(filepath.Clean(p))
	if runtime.GOOS == "windows" && len(slash) >= 2 && slash[1] == ':' {
		slash = "/" + slash
	}
	return (&url.URL{Scheme: "file", Path: slash}).String()
}

func contextWithRoots(roots ...string) context.Context {
	return workspacectx.WithRoots(roots...)
}

func TestResolveRepoUsesMCPRootsWhenRepoOmitted(t *testing.T) {
	base := t.TempDir()
	repoRoot := filepath.Join(base, "repo")
	otherRoot := filepath.Join(base, "other")
	reg := testRegistryWithRoots(repoRoot, otherRoot)

	got, err := resolveRepo(contextWithRoots(repoRoot), reg, "")
	if err != nil {
		t.Fatalf("resolveRepo returned error: %v", err)
	}
	if got.Name != "target" {
		t.Fatalf("expected target repo from MCP roots, got %q", got.Name)
	}
}

func TestResolveRepoExplicitNameRejectedOutsideWorkspace(t *testing.T) {
	base := t.TempDir()
	repoRoot := filepath.Join(base, "repo")
	otherRoot := filepath.Join(base, "other")
	reg := testRegistryWithRoots(repoRoot, otherRoot)

	_, err := resolveRepo(contextWithRoots(repoRoot), reg, "other")
	if err == nil {
		t.Fatal("expected error when explicit repo is outside MCP workspace roots")
	}
	if !strings.Contains(err.Error(), "outside the current workspace") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveRepoWithoutMatchingRootsRemainsAmbiguous(t *testing.T) {
	base := t.TempDir()
	reg := testRegistryWithRoots(filepath.Join(base, "repo"), filepath.Join(base, "other"))

	_, err := resolveRepo(contextWithRoots(filepath.Join(base, "missing")), reg, "")
	if err == nil {
		t.Fatalf("expected ambiguous repo error when roots do not match")
	}
}

func TestResolveRepoParentRootWithMultipleReposRemainsAmbiguous(t *testing.T) {
	base := t.TempDir()
	reg := testRegistryWithRoots(filepath.Join(base, "repo"), filepath.Join(base, "other"))

	_, err := resolveRepo(contextWithRoots(base), reg, "")
	if err == nil {
		t.Fatalf("expected ambiguous repo error when one root contains multiple indexed repos")
	}
}

func TestResolveRepoWithoutMCPRootsRequiresExplicitRepoWhenAmbiguous(t *testing.T) {
	base := t.TempDir()
	reg := testRegistryWithRoots(filepath.Join(base, "repo"), filepath.Join(base, "other"))

	_, err := resolveRepo(context.Background(), reg, "")
	if err == nil {
		t.Fatal("expected error when MCP roots missing and multiple repos indexed")
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRepoNameForRootsPrefersDeepestNestedProject(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "codehelper")
	child := filepath.Join(parent, ".testbeds", "fiber")
	reg := &registry.Registry{Entries: map[string]registry.Entry{
		"codehelper": {Name: "codehelper", RootPath: parent, SchemaVer: 2},
		"fiber":      {Name: "fiber", RootPath: child, SchemaVer: 2},
	}}

	// CWD / MCP root inside the nested testbed must bind to fiber, not parent.
	name, reason, ok := repoNameForRoots(reg, []string{normalizeComparablePath(child)})
	if !ok || name != "fiber" {
		t.Fatalf("cwd=child: got (%q,%v) want fiber", name, ok)
	}
	if reason != "matched_mcp_roots" {
		t.Fatalf("reason=%q", reason)
	}

	// Mid-path under parent but outside child still binds to parent.
	mid := filepath.Join(parent, "internal")
	name, _, ok = repoNameForRoots(reg, []string{normalizeComparablePath(mid)})
	if !ok || name != "codehelper" {
		t.Fatalf("cwd=mid: got (%q,%v) want codehelper", name, ok)
	}

	if got := repoNameForRoot(reg, child); got != "fiber" {
		t.Fatalf("repoNameForRoot(child)=%q want fiber", got)
	}
}

func TestRepoNameForRootsSkipsParentWhenChildHasOwnIndex(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "codehelper")
	child := filepath.Join(parent, ".testbeds", "astro")
	if err := os.MkdirAll(filepath.Join(child, ".codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".codehelper", "meta.json"), []byte(`{"schema_version":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &registry.Registry{Entries: map[string]registry.Entry{
		"codehelper": {Name: "codehelper", RootPath: parent, SchemaVer: 2},
	}}

	// Unregistered nested bed with its own index must NOT bind to the parent —
	// resolveRepo should fall through to auto-register the open workspace.
	name, _, ok := repoNameForRoots(reg, []string{normalizeComparablePath(child)})
	if ok {
		t.Fatalf("expected no parent match when child has own index, got %q", name)
	}

	// Mid-path without its own index still binds to parent.
	mid := filepath.Join(parent, "internal")
	name, _, ok = repoNameForRoots(reg, []string{normalizeComparablePath(mid)})
	if !ok || name != "codehelper" {
		t.Fatalf("cwd=mid: got (%q,%v) want codehelper", name, ok)
	}
}

func TestPreferNestedIndexedWorkspace_RegistersBedUnderParent(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "codehelper")
	child := filepath.Join(parent, ".testbeds", "godot")
	if err := os.MkdirAll(filepath.Join(child, ".codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Minimal initialized marker (graph.db present).
	if err := os.WriteFile(filepath.Join(child, ".codehelper", "graph.db"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".codehelper", "meta.json"), []byte(`{"schema_version":2,"symbol_count":5,"edge_count":8}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &registry.Registry{
		Entries: map[string]registry.Entry{
			"codehelper": {Name: "codehelper", RootPath: parent, SchemaVer: 2},
		},
	}
	got, ok := preferNestedIndexedWorkspace(context.Background(), reg, []string{child}, "codehelper")
	if !ok || got != "godot" {
		t.Fatalf("preferNested=%q ok=%v want godot", got, ok)
	}
	e, ok := reg.Get("godot")
	if !ok || normalizeComparablePath(e.RootPath) != normalizeComparablePath(child) {
		t.Fatalf("godot entry missing/wrong: %+v", e)
	}
}

func TestPathsEquivalent_SameAbs(t *testing.T) {
	base := t.TempDir()
	if !pathsEquivalent(base, base) {
		t.Fatal("identical paths should be equivalent")
	}
	if pathsEquivalent(base, filepath.Join(base, "nope")) {
		t.Fatal("distinct paths must not be equivalent")
	}
}

func TestBindOpenIndexedWorkspace_CWDWithoutMCPRoots(t *testing.T) {
	base := t.TempDir()
	bed := filepath.Join(base, "rails")
	if err := os.MkdirAll(filepath.Join(bed, ".codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bed, ".codehelper", "meta.json"), []byte(`{"schema_version":2,"symbol_count":12,"edge_count":28}`), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "other-rails-copy")
	reg := &registry.Registry{
		Entries: map[string]registry.Entry{
			"rails": {Name: "rails", RootPath: other, SchemaVer: 2},
		},
	}
	// stdio harness: CWD only, no MCP ListRoots.
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(bed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	got, err := resolveRepo(context.Background(), reg, "")
	if err != nil {
		t.Fatalf("resolveRepo: %v", err)
	}
	if got.Name != "rails" {
		t.Fatalf("name=%q want rails", got.Name)
	}
	e, ok := reg.Get("rails")
	if !ok || normalizeComparablePath(e.RootPath) != normalizeComparablePath(bed) {
		t.Fatalf("canonical root=%v ok=%v want %s", e, ok, bed)
	}
}

func TestResolveRepo_BindsIndexedCWDWhenRegistryPathDiffers(t *testing.T) {
	base := t.TempDir()
	bed := filepath.Join(base, "nest")
	if err := os.MkdirAll(filepath.Join(bed, ".codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bed, ".codehelper", "meta.json"), []byte(`{"schema_version":2,"symbol_count":14,"edge_count":40}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &registry.Registry{Entries: map[string]registry.Entry{}}
	got, err := resolveRepo(workspacectx.WithRoots(bed), reg, "")
	if err != nil {
		t.Fatalf("resolveRepo: %v", err)
	}
	if got.Name != "nest" {
		t.Fatalf("got %q want nest", got.Name)
	}
	e, ok := reg.Get("nest")
	if !ok || normalizeComparablePath(e.RootPath) != normalizeComparablePath(bed) {
		t.Fatalf("registry nest root=%v ok=%v", e, ok)
	}
}
