package freshness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/daemon"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/parser"
)

func TestInspect_ParserVersionMismatch_IsStale(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := parser.Version - 1
	if old < 1 {
		old = 1
		if parser.Version == 1 {
			t.Skip("need parser.Version > 1")
		}
	}
	if err := meta.Write(dir, &meta.Data{
		RepoName:      "x",
		ParserVersion: old,
	}); err != nil {
		t.Fatal(err)
	}
	// Keep source mtime behind IndexedAt so only the version gate fires.
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(src, past, past); err != nil {
		t.Fatal(err)
	}

	r := Inspect(dir)
	if !r.Stale {
		t.Fatalf("parser version mismatch should be stale, got %#v", r)
	}
	if !versionMismatchReason(r.StaleReason) {
		t.Fatalf("stale_reason=%q want parser/schema mismatch", r.StaleReason)
	}
	if r.ActionRequired == nil || len(r.ActionRequired.Commands) != 1 {
		t.Fatalf("version mismatch must keep analyze command: %#v", r.ActionRequired)
	}
}

func TestInspect_ParserVersionMismatch_KeepsAnalyzeWhenWatchRunning(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := parser.Version - 1
	if old < 1 {
		old = 1
	}
	if err := meta.Write(dir, &meta.Data{RepoName: "x", ParserVersion: old}); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(src, past, past); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"pid": %d, "index_root": %q}`, os.Getpid(), dir)
	if err := os.WriteFile(daemon.StatePath(dir), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Inspect(dir)
	if !r.WatchRunning || !r.Stale {
		t.Fatalf("want watch+stale version mismatch, got %#v", r)
	}
	if r.ActionRequired == nil || len(r.ActionRequired.Commands) == 0 {
		t.Fatalf("watch must NOT suppress analyze on parser mismatch: %#v", r.ActionRequired)
	}
	if strings.Contains(r.SuggestedFix, "converge automatically") {
		t.Fatalf("must not claim watch will converge on parser bump: %q", r.SuggestedFix)
	}
}
