package usage

import "testing"

func TestMarkOnce(t *testing.T) {
	r := NewRecorder()

	// First call per (session,key) is true; subsequent are false.
	if !r.MarkOnce("s1", "note") {
		t.Fatal("first MarkOnce should be true")
	}
	if r.MarkOnce("s1", "note") {
		t.Fatal("second MarkOnce should be false")
	}
	// Different key is independent.
	if !r.MarkOnce("s1", "other") {
		t.Fatal("different key should be true the first time")
	}
	// Different session is independent.
	if !r.MarkOnce("s2", "note") {
		t.Fatal("different session should be true the first time")
	}

	// Empty session is always treated as never-seen (sessionless transport keeps
	// the guidance rather than dropping it).
	if !r.MarkOnce("", "note") || !r.MarkOnce("", "note") {
		t.Fatal("empty session must always return true")
	}

	// Forget resets the session's flags.
	r.Forget("s1")
	if !r.MarkOnce("s1", "note") {
		t.Fatal("after Forget, MarkOnce should be true again")
	}
	// Forget must not disturb other sessions.
	if r.MarkOnce("s2", "note") {
		t.Fatal("Forget(s1) must not reset s2")
	}
}
