package registry

import "testing"

func TestEntryForWorkspace_exactAndParent(t *testing.T) {
	t.Parallel()
	r := &Registry{
		Entries: map[string]Entry{
			"codehelper": {Name: "codehelper", RootPath: "/home/user/codehelper"},
			"docs":       {Name: "docs", RootPath: "/home/user/codehelper/docs"},
			"other":      {Name: "other", RootPath: "/tmp/other"},
		},
	}
	if e, ok := r.EntryForWorkspace("/home/user/codehelper"); !ok || e.Name != "codehelper" {
		t.Fatalf("exact workspace: got %#v ok=%v", e, ok)
	}
	if e, ok := r.EntryForWorkspace("/home/user/codehelper/docs/deep"); !ok || e.Name != "docs" {
		t.Fatalf("parent shard: got %#v ok=%v", e, ok)
	}
	if _, ok := r.EntryForWorkspace("/tmp/nope"); ok {
		t.Fatal("expected no entry for unknown workspace")
	}
}

func TestResolveNameInWorkspace_prefersWorkspace(t *testing.T) {
	t.Parallel()
	r := &Registry{
		Entries: map[string]Entry{
			"codehelper": {Name: "codehelper", RootPath: "/home/user/codehelper"},
			"other":      {Name: "other", RootPath: "/tmp/other"},
		},
	}
	name, err := r.ResolveNameInWorkspace("", "/home/user/codehelper")
	if err != nil || name != "codehelper" {
		t.Fatalf("ResolveNameInWorkspace: name=%q err=%v", name, err)
	}
}
