package graph

import "testing"

func TestRankGroupQueryHits_PathAndFixturePreference(t *testing.T) {
	t.Parallel()
	hits := []GroupQueryHit{
		{Repo: "nest", Name: "CatsService", Path: "sample/07-sequelize/src/cats.service.ts", Kind: "class", ID: "a"},
		{Repo: "nest", Name: "CatsService", Path: "sample/01-cats-app/src/cats.service.ts", Kind: "class", ID: "b"},
		{Repo: "api", Name: "CatsService", Path: "src/cats/cats.service.ts", Kind: "class", ID: "c"},
		{Repo: "web", Name: "Other", Path: "lib/other.ts", Kind: "function", ID: "d"},
	}
	got := RankGroupQueryHits(hits, "cats.service", 10)
	if len(got) != 3 {
		t.Fatalf("path filter: got %d %#v", len(got), got)
	}
	if got[0].Repo != "api" || got[0].Path != "src/cats/cats.service.ts" {
		t.Fatalf("production should rank first: %#v", got[0])
	}
	got = RankGroupQueryHits(hits, "sample/01-cats", 5)
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("path=sample/01-cats: %#v", got)
	}
}

func TestGroupQueryAmbiguous(t *testing.T) {
	t.Parallel()
	if GroupQueryAmbiguous([]GroupQueryHit{{Name: "A", Repo: "r", Path: "a.ts"}}) {
		t.Fatal("single hit not ambiguous")
	}
	if !GroupQueryAmbiguous([]GroupQueryHit{
		{Name: "CatsService", Repo: "nest", Path: "sample/01/cats.service.ts"},
		{Name: "CatsService", Repo: "nest", Path: "sample/07/cats.service.ts"},
	}) {
		t.Fatal("same name two paths should be ambiguous")
	}
	if !GroupQueryAmbiguous([]GroupQueryHit{
		{Name: "UserService", Repo: "api", Path: "src/user.go"},
		{Name: "UserService", Repo: "web", Path: "src/client.go"},
	}) {
		t.Fatal("same name across repos should be ambiguous")
	}
}
