package mcpsvc

import (
	"context"
	"net/url"
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
