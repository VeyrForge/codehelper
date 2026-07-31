package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/connections"
)

func TestReadLog_TailsLocalFile(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "app.log")
	lines := []string{"line1", "line2", "line3", "line4", "line5"}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := connections.Config{
		LogSources: []connections.LogSource{{Name: "app", Kind: "app", Path: "app.log"}},
	}
	if err := connections.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	out, err := ReadLog(context.Background(), root, "app", 3)
	if err != nil {
		t.Fatal(err)
	}
	if out.Lines != 3 {
		t.Fatalf("lines=%d want 3", out.Lines)
	}
	if !strings.Contains(out.Output, "line3") || !strings.Contains(out.Output, "line5") {
		t.Fatalf("unexpected tail output: %q", out.Output)
	}
}

// TestReadLog_TailsLastNOfLargeFile exercises the multi-chunk read loop (file
// bigger than the 8 KiB chunk) and confirms it returns exactly the last N lines
// in order — guarding the O(n) newline-count rewrite of tailFile.
func TestReadLog_TailsLastNOfLargeFile(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	const total = 5000
	for i := 0; i < total; i++ {
		fmt.Fprintf(&sb, "log line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "big.log"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := connections.Config{
		LogSources: []connections.LogSource{{Name: "big", Kind: "app", Path: "big.log"}},
	}
	if err := connections.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	out, err := ReadLog(context.Background(), root, "big", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(out.Output, "\n"), "\n")
	if len(got) != 10 {
		t.Fatalf("got %d lines, want 10: %q", len(got), out.Output)
	}
	if got[0] != "log line 4990" || got[9] != "log line 4999" {
		t.Fatalf("wrong tail window: first=%q last=%q", got[0], got[9])
	}
}

func TestReadLog_RequiresConfiguredSource(t *testing.T) {
	_, err := ReadLog(context.Background(), t.TempDir(), "missing", 10)
	if err == nil {
		t.Fatal("expected error for missing source")
	}
}
