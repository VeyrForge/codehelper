package registry

import "testing"

func TestRemove(t *testing.T) {
	r := &Registry{Entries: map[string]Entry{
		"keep": {Name: "keep"},
		"gone": {Name: "gone"},
	}}
	r.Remove("gone")
	if _, ok := r.Get("gone"); ok {
		t.Fatal("expected 'gone' to be removed")
	}
	if _, ok := r.Get("keep"); !ok {
		t.Fatal("'keep' should remain")
	}
	r.Remove("absent") // must not panic on a missing key
}
