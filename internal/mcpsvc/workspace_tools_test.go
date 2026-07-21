package mcpsvc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: unifiedDiffWithCap used to panic with index-out-of-range when
// the "before" side was empty (creating a brand-new file via write_workspace_file).
func TestUnifiedDiffWithCap_NewFileDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unifiedDiffWithCap panicked on new-file write: %v", r)
		}
	}()
	diff, elided := unifiedDiffWithCap("notes.txt", "", "first line\nsecond line\n", 8192)
	if diff == "" {
		t.Fatalf("expected non-empty diff for new file write")
	}
	if elided {
		t.Fatalf("did not expect elision for tiny diff")
	}
	if !strings.Contains(diff, "--- a/notes.txt") || !strings.Contains(diff, "+++ b/notes.txt") {
		t.Fatalf("diff missing file headers: %q", diff)
	}
	for _, want := range []string{"+first line", "+second line"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff missing %q:\n%s", want, diff)
		}
	}
}

func TestUnifiedDiffWithCap_DeletedFileDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unifiedDiffWithCap panicked when reducing to empty: %v", r)
		}
	}()
	diff, _ := unifiedDiffWithCap("notes.txt", "only line\n", "", 8192)
	if !strings.Contains(diff, "-only line") {
		t.Fatalf("diff missing delete line: %q", diff)
	}
}

func TestUnifiedDiffWithCap_IdenticalIsEmpty(t *testing.T) {
	diff, elided := unifiedDiffWithCap("notes.txt", "abc\n", "abc\n", 8192)
	if diff != "" || elided {
		t.Fatalf("expected empty diff for identical content; got %q elided=%v", diff, elided)
	}
}

func TestApplyHunks_ExactMatch(t *testing.T) {
	src := "alpha\nbeta\ngamma\n"
	got, n, _, err := applyHunks(src, []patchHunk{
		{OldString: "beta\n", NewString: "BETA!\n"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 hunk applied, got %d", n)
	}
	if got != "alpha\nBETA!\ngamma\n" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestApplyHunks_AmbiguousFails(t *testing.T) {
	src := "x\nx\n"
	if _, _, _, err := applyHunks(src, []patchHunk{{OldString: "x\n", NewString: "y\n"}}); err == nil {
		t.Fatalf("expected error for ambiguous match")
	}
}

func TestApplyHunks_ReplaceAll(t *testing.T) {
	src := "x\nx\n"
	got, _, _, err := applyHunks(src, []patchHunk{{OldString: "x\n", NewString: "y\n", ReplaceAll: true}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "y\ny\n" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestApplyHunks_NoMatchFails(t *testing.T) {
	if _, _, _, err := applyHunks("alpha\n", []patchHunk{{OldString: "missing", NewString: "x"}}); err == nil {
		t.Fatalf("expected error for no match")
	}
}

func TestApplyHunks_NoopRejected(t *testing.T) {
	if _, _, _, err := applyHunks("alpha\n", []patchHunk{{OldString: "alpha\n", NewString: "alpha\n"}}); err == nil {
		t.Fatalf("expected error for no-op hunk")
	}
}

func TestWindowLines_SmallFileWhole(t *testing.T) {
	src := "a\nb\nc\n" // 4 "lines" (trailing newline → empty 4th)
	got, ls, le, total := windowLines(src, 0, 0)
	if got != src || ls != 1 || le != total {
		t.Fatalf("small file should return whole: got=%q ls=%d le=%d total=%d", got, ls, le, total)
	}
}

func TestWindowLines_DefaultWindowPages(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 1200; i++ {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	got, ls, le, total := windowLines(b.String(), 0, 0)
	if ls != 1 || le != defaultReadLineWindow {
		t.Fatalf("expected window 1–%d, got %d–%d", defaultReadLineWindow, ls, le)
	}
	if total < 1200 {
		t.Fatalf("total lines wrong: %d", total)
	}
	if strings.Count(got, "\n") != defaultReadLineWindow-1 {
		t.Fatalf("expected %d lines returned, got %d", defaultReadLineWindow, strings.Count(got, "\n")+1)
	}
	// Next page via offset.
	_, ls2, le2, _ := windowLines(b.String(), defaultReadLineWindow+1, 0)
	if ls2 != defaultReadLineWindow+1 || le2 <= ls2 {
		t.Fatalf("next page wrong: %d–%d", ls2, le2)
	}
}

func TestWindowLines_ExplicitSlice(t *testing.T) {
	src := "1\n2\n3\n4\n5\n"
	got, ls, le, _ := windowLines(src, 2, 2)
	if got != "2\n3" || ls != 2 || le != 3 {
		t.Fatalf("explicit slice wrong: got=%q ls=%d le=%d", got, ls, le)
	}
}

func TestApplyHunks_WhitespaceTolerant_TabsVsSpaces(t *testing.T) {
	// File uses a tab to indent; the agent's old_string used 4 spaces (drift).
	src := "func f() {\n\treturn 1\n}\n"
	got, n, fuzzy, err := applyHunks(src, []patchHunk{
		{OldString: "    return 1\n", NewString: "    return 2\n"},
	})
	if err != nil {
		t.Fatalf("expected tolerant match to apply, got error: %v", err)
	}
	if n != 1 || fuzzy != 1 {
		t.Fatalf("expected 1 applied / 1 fuzzy, got %d / %d", n, fuzzy)
	}
	// new_string must be reindented to the file's TAB, not the agent's spaces.
	if got != "func f() {\n\treturn 2\n}\n" {
		t.Fatalf("expected tab-reindented result, got %q", got)
	}
}

func TestApplyHunks_WhitespaceTolerant_TrailingSpace(t *testing.T) {
	src := "alpha  \nbeta\n" // alpha has trailing spaces on disk
	got, _, fuzzy, err := applyHunks(src, []patchHunk{
		{OldString: "alpha\n", NewString: "ALPHA\n"},
	})
	if err != nil || fuzzy != 1 {
		t.Fatalf("expected tolerant trailing-space match, fuzzy=%d err=%v", fuzzy, err)
	}
	if got != "ALPHA\nbeta\n" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestApplyHunks_WhitespaceTolerant_AmbiguousRefused(t *testing.T) {
	// Two whitespace-equivalent spans → must NOT guess; falls back to error.
	src := "  x\n\tx\n"
	if _, _, _, err := applyHunks(src, []patchHunk{{OldString: "x\n", NewString: "y\n"}}); err == nil {
		t.Fatalf("expected refusal when whitespace match is ambiguous")
	}
}

func TestApplyHunks_SequentialHunks(t *testing.T) {
	src := "one\ntwo\nthree\n"
	got, n, _, err := applyHunks(src, []patchHunk{
		{OldString: "one\n", NewString: "uno\n"},
		{OldString: "three\n", NewString: "tres\n"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 hunks applied, got %d", n)
	}
	if got != "uno\ntwo\ntres\n" {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestTruncationGuard_EmptyContent(t *testing.T) {
	reason := truncationLooksWrong([]byte("hello\n"), []byte(""))
	if reason == "" {
		t.Fatalf("expected guard to reject empty content")
	}
}

func TestTruncationGuard_HalfBakedTail(t *testing.T) {
	// Mimics the real-world breakage: file ends mid-line at `*.`
	prev := []byte(strings.Repeat("# comment\n", 20))
	next := []byte("# comment\n*.")
	reason := truncationLooksWrong(prev, next)
	if reason == "" {
		t.Fatalf("expected guard to reject obvious truncation")
	}
}

func TestTruncationGuard_LegitShrinkAllowed(t *testing.T) {
	prev := []byte("aaaaaaaaaa\nbbbbbbbbbb\n")
	next := []byte("aaaaaaaaaa\n") // small shrink, still ends in NL
	if reason := truncationLooksWrong(prev, next); reason != "" {
		t.Fatalf("legitimate shrink should pass, got: %s", reason)
	}
}

func TestTruncationGuard_NewFileAllowed(t *testing.T) {
	if reason := truncationLooksWrong(nil, []byte("hello\n")); reason != "" {
		t.Fatalf("creating new file should pass, got: %s", reason)
	}
}

func TestUnifyIndentChars_SpacesToTabs(t *testing.T) {
	sample := "\treturn 1\n"
	got := unifyIndentChars("    return 2\n", sample)
	if got != "\treturn 2\n" {
		t.Fatalf("expected tab indent, got %q", got)
	}
}

func TestApplyHunks_ExactMatch_RestylesIndentChars(t *testing.T) {
	// File + old_string use tabs; agent new_string uses 4 spaces — must restyle to tabs.
	src := "func f() {\n\treturn 1\n}\n"
	got, n, fuzzy, err := applyHunks(src, []patchHunk{
		{OldString: "\treturn 1\n", NewString: "    return 2\n"},
	})
	if err != nil || n != 1 || fuzzy != 0 {
		t.Fatalf("expected exact apply, n=%d fuzzy=%d err=%v", n, fuzzy, err)
	}
	if got != "func f() {\n\treturn 2\n}\n" {
		t.Fatalf("expected tab-styled new_string, got %q", got)
	}
}

func TestApplyHunks_ExactFlag_SkipsTolerant(t *testing.T) {
	src := "func f() {\n\treturn 1\n}\n"
	_, _, _, err := applyHunks(src, []patchHunk{
		{OldString: "    return 1\n", NewString: "    return 2\n", Exact: true},
	})
	if err == nil {
		t.Fatalf("expected exact=true to refuse whitespace-only match")
	}
	if !strings.Contains(err.Error(), "exact=true") {
		t.Fatalf("error should mention exact=true, got %v", err)
	}
}

func TestApplyHunks_BannerCommentNotRestyled(t *testing.T) {
	// Regression: dry-run on express banner rewrote " * express" → "    * express".
	src := "/*!\n * express\n * Copyright\n */\n"
	got, n, fuzzy, err := applyHunks(src, []patchHunk{
		{OldString: " * express\n", NewString: " * express-patched\n"},
	})
	if err != nil || n != 1 || fuzzy != 0 {
		t.Fatalf("expected exact apply, n=%d fuzzy=%d err=%v", n, fuzzy, err)
	}
	if got != "/*!\n * express-patched\n * Copyright\n */\n" {
		t.Fatalf("banner indent corrupted: %q", got)
	}
}

func TestUnifyIndentChars_PreservesSingleSpaceBanner(t *testing.T) {
	sample := " * express\n"
	got := unifyIndentChars(" * express-patched\n", sample)
	if got != " * express-patched\n" {
		t.Fatalf("expected single-space preserved, got %q", got)
	}
}

func TestAtomicWrite_SizeCheck(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.txt")
	data := []byte("hello world\n")
	if err := atomicWrite(target, data); err != nil {
		t.Fatalf("atomic write: %v", err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != int64(len(data)) {
		t.Fatalf("size %d want %d", st.Size(), len(data))
	}
}

func TestResolveKickoffTask_QueryAlias(t *testing.T) {
	task, note := resolveKickoffTask(map[string]any{"query": "add oauth login"})
	if task != "add oauth login" {
		t.Fatalf("task=%q", task)
	}
	if !strings.Contains(note, "task=") {
		t.Fatalf("expected correction note, got %q", note)
	}
	task2, note2 := resolveKickoffTask(map[string]any{"task": "real task", "query": "ignored"})
	if task2 != "real task" || note2 != "" {
		t.Fatalf("task should win: task=%q note=%q", task2, note2)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rel := "deep/file.txt"
	orig := []byte("original content\n")

	token, err := snapshotPreEdit(dir, rel, orig)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !isPlausibleToken(token) {
		t.Fatalf("token %q failed plausibility", token)
	}

	// Confirm meta + content exist on disk.
	snapDir := filepath.Join(dir, ".codehelper", "edits", token)
	if _, err := os.Stat(filepath.Join(snapDir, "meta.json")); err != nil {
		t.Fatalf("meta missing: %v", err)
	}
	gotContent, err := os.ReadFile(filepath.Join(snapDir, "content.bin"))
	if err != nil {
		t.Fatalf("content missing: %v", err)
	}
	if string(gotContent) != string(orig) {
		t.Fatalf("snapshot content mismatch: %q", string(gotContent))
	}

	// Read meta JSON shape.
	raw, _ := os.ReadFile(filepath.Join(snapDir, "meta.json"))
	var meta snapshotMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("meta parse: %v", err)
	}
	if meta.RelPath != "deep/file.txt" || meta.RepoRoot != dir || meta.Token != token {
		t.Fatalf("meta wrong: %+v", meta)
	}
	if meta.Reverted {
		t.Fatalf("fresh snapshot should not be marked reverted")
	}
}

func TestAtomicWrite_ReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(target, []byte("v2 with more")); err != nil {
		t.Fatalf("atomic write: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "v2 with more" {
		t.Fatalf("got %q", got)
	}
	// No leftover *.tmp.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".codehelper-tmp-") {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
}

func TestUnifiedDiff_AddsLine(t *testing.T) {
	before := "alpha\nbeta\n"
	after := "alpha\nbeta\ngamma\n"
	diff, elided := unifiedDiffWithCap("f.txt", before, after, 4096)
	if elided {
		t.Fatalf("did not expect elision")
	}
	if !strings.Contains(diff, "+gamma") {
		t.Fatalf("diff missing add line:\n%s", diff)
	}
	if !strings.Contains(diff, "--- a/f.txt") || !strings.Contains(diff, "+++ b/f.txt") {
		t.Fatalf("diff missing headers:\n%s", diff)
	}
}

func TestUnifiedDiff_RemovesLine(t *testing.T) {
	before := "alpha\nbeta\ngamma\n"
	after := "alpha\ngamma\n"
	diff, _ := unifiedDiffWithCap("f.txt", before, after, 4096)
	if !strings.Contains(diff, "-beta") {
		t.Fatalf("diff missing del line:\n%s", diff)
	}
}

func TestUnifiedDiff_EmptyWhenIdentical(t *testing.T) {
	diff, _ := unifiedDiffWithCap("f.txt", "same\n", "same\n", 4096)
	if diff != "" {
		t.Fatalf("expected empty diff, got: %q", diff)
	}
}

func TestIsObviouslyTruncatedTail(t *testing.T) {
	cases := map[string]bool{
		"*.":           true,
		"**.":          true,
		`name = "open`: true,
		"name = `tpl":  true,
		"foo":          false,
		"":             false,
		"}":            false,
		"`closed`":     false,
	}
	for input, want := range cases {
		if got := isObviouslyTruncatedTail(input); got != want {
			t.Fatalf("isObviouslyTruncatedTail(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestRelativePathRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if _, err := relativePathUnderRepo(dir, "../etc/passwd"); err == nil {
		t.Fatalf("expected traversal rejection")
	}
	if _, err := relativePathUnderRepo(dir, "subdir/file.txt"); err != nil {
		t.Fatalf("legit relative should pass: %v", err)
	}
}

func TestResolvePatchHunks_AliasesAndUnifiedDiff(t *testing.T) {
	hunks, err := resolvePatchHunks(map[string]any{
		"edits": []any{
			map[string]any{"old": "a\n", "new": "b\n"},
		},
	})
	if err != nil || len(hunks) != 1 || hunks[0].OldString != "a\n" || hunks[0].NewString != "b\n" {
		t.Fatalf("edits alias: %#v err=%v", hunks, err)
	}
	hunks, err = resolvePatchHunks(map[string]any{
		"patch": "@@\n line-one\n-line-two\n+line-two-patched\n",
	})
	if err != nil {
		t.Fatalf("unified patch: %v", err)
	}
	if len(hunks) != 1 {
		t.Fatalf("want 1 hunk, got %#v", hunks)
	}
	if hunks[0].OldString != "line-one\nline-two\n" || hunks[0].NewString != "line-one\nline-two-patched\n" {
		t.Fatalf("unexpected unified hunk: old=%q new=%q", hunks[0].OldString, hunks[0].NewString)
	}
	_, err = resolvePatchHunks(map[string]any{})
	if err == nil {
		t.Fatal("expected error when hunks missing")
	}
}
