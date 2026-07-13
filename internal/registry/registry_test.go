package registry

import "testing"

func TestResolveImportOwners(t *testing.T) {
	r := &Registry{
		Entries: map[string]Entry{
			"core": {Name: "core", ImportRoots: []string{"github.com/acme/core"}},
			"web":  {Name: "web", ImportRoots: []string{"github.com/acme/web"}},
		},
	}
	got := r.ResolveImportOwners("github.com/acme/core/auth")
	if len(got) != 1 || got[0].Name != "core" {
		t.Fatalf("unexpected owners: %#v", got)
	}
}
