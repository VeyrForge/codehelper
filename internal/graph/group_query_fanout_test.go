package graph

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestFanOutGroupQuery_MembersPathAmbiguity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	openMember := func(repo, symName, symPath string) GroupMemberDB {
		st, err := Open(filepath.Join(dir, repo+".db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if err := st.UpsertSymbol(ctx, types.Symbol{
			ID: "s:" + repo + ":" + symPath, RepoID: repo, Name: symName,
			Kind: types.SymbolKindClass, Path: symPath, Language: "typescript",
		}); err != nil {
			t.Fatal(err)
		}
		return GroupMemberDB{Repo: repo, Store: st}
	}

	members := []GroupMemberDB{
		openMember("api", "UserService", "src/user.service.ts"),
		openMember("web", "UserService", "src/user.service.ts"),
		openMember("nest", "UserService", "sample/01-cats/src/user.service.ts"),
	}

	got := FanOutGroupQuery(ctx, "platform", "UserService", "", 10, members)
	if got.Count != 3 {
		t.Fatalf("expected 3 hits, got %#v", got)
	}
	if got.Hits[0].Repo != "api" {
		t.Fatalf("production api should rank before fixture nest: %#v", got.Hits[0])
	}
	if !got.Ambiguous {
		t.Fatal("expected ambiguous for same name across paths")
	}

	filtered := FanOutGroupQuery(ctx, "platform", "UserService", "sample/01", 10, members)
	if filtered.Count != 1 || filtered.Hits[0].Repo != "nest" {
		t.Fatalf("path filter: %#v", filtered.Hits)
	}
}

func TestSearchMemberSymbols_PathFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "api"
	if err := st.UpsertSymbol(ctx, types.Symbol{
		ID: "s:1", RepoID: repo, Name: "UserService", Kind: types.SymbolKindClass,
		Path: "src/user.service.ts", Language: "typescript",
	}); err != nil {
		t.Fatal(err)
	}
	syms, err := SearchMemberSymbols(ctx, st, repo, "UserService", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 1 || syms[0].Name != "UserService" {
		t.Fatalf("got %#v", syms)
	}
}

func TestPreferExactSymbolNameHits_DropsDocOnly(t *testing.T) {
	t.Parallel()
	syms := []types.Symbol{
		{Name: "CheckoutService", Path: "checkout.go"},
		{Name: "InventoryClient", Path: "client.go"},
	}
	got := preferExactSymbolNameHits(syms, "InventoryClient")
	if len(got) != 1 || got[0].Name != "InventoryClient" {
		t.Fatalf("exact prefer: %#v", got)
	}
	onlyDoc := []types.Symbol{{Name: "CheckoutService", Path: "checkout.go"}}
	if got := preferExactSymbolNameHits(onlyDoc, "InventoryClient"); len(got) != 0 {
		t.Fatalf("doc-only FTS should drop: %#v", got)
	}
}

func TestGroupQueryAmbiguousNames(t *testing.T) {
	t.Parallel()
	names := GroupQueryAmbiguousNames([]GroupQueryHit{
		{Name: "UserService", Repo: "api", Path: "a.ts"},
		{Name: "UserService", Repo: "web", Path: "b.ts"},
		{Name: "Other", Repo: "api", Path: "c.ts"},
	})
	if len(names) != 1 || names[0] != "UserService" {
		t.Fatalf("got %#v", names)
	}
}
