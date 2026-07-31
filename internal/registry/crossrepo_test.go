package registry

import "testing"

func TestResolveCrossRepoEdges_SameGroup(t *testing.T) {
	t.Parallel()
	r := &Registry{
		Entries: map[string]Entry{
			"api": {Name: "api", RootPath: "/tmp/api", ImportRoots: []string{"github.com/acme/api"}, GroupIDs: []string{"g1"}},
			"web": {Name: "web", RootPath: "/tmp/web", ImportRoots: []string{"@acme/web"}, GroupIDs: []string{"g1"}},
			"lib": {Name: "lib", RootPath: "/tmp/lib", ImportRoots: []string{"github.com/acme/lib"}},
		},
		Groups: map[string]WorkspaceGroup{
			"g1": {ID: "g1", Name: "g1", Members: []string{"api", "web"}},
		},
	}
	edges := r.ResolveCrossRepoEdges("web", []string{
		"github.com/acme/api/handlers",
		"github.com/acme/lib/util",
		"@acme/web/internal",
	})
	if len(edges) != 2 {
		t.Fatalf("edges=%#v", edges)
	}
	byOwner := map[string]CrossRepoEdge{}
	for _, e := range edges {
		byOwner[e.OwnerName] = e
	}
	if !byOwner["api"].SameGroup {
		t.Fatalf("api should be same group: %#v", byOwner["api"])
	}
	if byOwner["lib"].SameGroup {
		t.Fatalf("lib should not be same group: %#v", byOwner["lib"])
	}
}
