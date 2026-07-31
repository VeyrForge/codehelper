package memory

import (
	"testing"
)

func TestStore_Search(t *testing.T) {
	s := Open(t.TempDir())
	if err := s.AddFixPattern(FixPattern{ID: "a", Problem: "checkout reset", Solution: "persist state"}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("checkout state", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hit")
	}
}
