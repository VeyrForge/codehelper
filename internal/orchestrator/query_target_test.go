package orchestrator

import "testing"

func TestBestEntityQuery(t *testing.T) {
	got := bestEntityQuery([]string{"fix", "bug", "Run", "fails"})
	if got != "Run" {
		t.Fatalf("got %q want Run", got)
	}
	got = bestEntityQuery([]string{"godot_log", "work"})
	if got != "godot_log" {
		t.Fatalf("got %q want godot_log", got)
	}
}
