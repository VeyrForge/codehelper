package registry

import "testing"

func TestUpsertGroup_AndSiblings(t *testing.T) {
	t.Parallel()
	r := &Registry{
		Entries: map[string]Entry{
			"api":  {Name: "api", RootPath: "/tmp/api", ImportRoots: []string{"github.com/acme/api"}},
			"web":  {Name: "web", RootPath: "/tmp/web", ImportRoots: []string{"@acme/web"}},
			"docs": {Name: "docs", RootPath: "/tmp/docs"},
		},
		Groups: map[string]WorkspaceGroup{},
	}
	if err := r.UpsertGroup(WorkspaceGroup{
		ID:          "platform",
		Name:        "Platform",
		Members:     []string{"api", "web"},
		Description: "backend + frontend",
	}); err != nil {
		t.Fatal(err)
	}
	api, _ := r.Get("api")
	if len(api.GroupIDs) != 1 || api.GroupIDs[0] != "platform" {
		t.Fatalf("api GroupIDs=%v", api.GroupIDs)
	}
	sibs := r.SiblingEntries("api")
	if len(sibs) != 1 || sibs[0].Name != "web" {
		t.Fatalf("siblings=%#v", sibs)
	}
	docsSibs := r.SiblingEntries("docs")
	if len(docsSibs) != 0 {
		t.Fatalf("docs should have no siblings: %#v", docsSibs)
	}
	r.RemoveGroup("platform")
	api, _ = r.Get("api")
	if len(api.GroupIDs) != 0 {
		t.Fatalf("expected cleared GroupIDs, got %v", api.GroupIDs)
	}
	if _, ok := r.GetGroup("platform"); ok {
		t.Fatal("group should be gone")
	}
}

func TestSharesWorkspaceGroup(t *testing.T) {
	t.Parallel()
	r := &Registry{
		Entries: map[string]Entry{
			"api":  {Name: "api"},
			"web":  {Name: "web"},
			"docs": {Name: "docs"},
		},
		Groups: map[string]WorkspaceGroup{},
	}
	if err := r.UpsertGroup(WorkspaceGroup{ID: "platform", Members: []string{"api", "web"}}); err != nil {
		t.Fatal(err)
	}
	if !r.SharesWorkspaceGroup("api", "web") {
		t.Fatal("api+web should share platform")
	}
	if r.SharesWorkspaceGroup("api", "docs") {
		t.Fatal("api+docs should not share a group")
	}
}
