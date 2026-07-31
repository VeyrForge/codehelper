package review

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/rules"
)

func TestParseAddedLines_SingleFile(t *testing.T) {
	diff := `diff --git a/x.php b/x.php
index 111..222 100644
--- a/x.php
+++ b/x.php
@@ -1,2 +1,3 @@
 a
-b
+c
+echo $_GET['x'];
`
	got := parseAddedLines(diff)
	if len(got) < 2 {
		t.Fatalf("expected 2 added lines, got %d", len(got))
	}
	// After context line " a", first insertion (+c) lands at new line 2.
	if got[0].Line != 2 || !strings.Contains(got[0].Text, "c") {
		t.Fatalf("first added line unexpected: %#v", got[0])
	}
}

func TestPatchCritic_Pattern(t *testing.T) {
	// Exercises matching path without calling git: pass empty diff by mocking is hard; use pattern helper only.
	p := rules.RiskPattern{ID: "t", Match: "badword", Requires: "safe", Severity: "high"}
	if !matchedPattern("has badword here", p) {
		t.Fatal("expected match")
	}
	if hunkSatisfiesRequires("context safe", p) {
		_ = hunkSatisfiesRequires("badword", p) // no requires in hunk
	}
	if !hunkSatisfiesRequires("safe badword", p) {
		t.Fatal("expected requires satisfied")
	}
}
