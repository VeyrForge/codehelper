package registry

import "testing"

func TestSummarizeEntry_uninitialized(t *testing.T) {
	dir := t.TempDir()
	s := SummarizeEntry(Entry{Name: "x", RootPath: dir})
	if s.Initialized || s.IndexStatus != "missing" {
		t.Fatalf("summary = %+v", s)
	}
}

func TestListProjectSummaries_sorted(t *testing.T) {
	r := &Registry{
		Entries: map[string]Entry{
			"z": {Name: "z", RootPath: t.TempDir()},
			"a": {Name: "a", RootPath: t.TempDir()},
		},
	}
	list := r.ListProjectSummaries()
	if len(list) != 2 || list[0].Name != "a" || list[1].Name != "z" {
		t.Fatalf("list = %+v", list)
	}
}
