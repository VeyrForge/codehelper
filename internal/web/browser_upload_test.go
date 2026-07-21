package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitUploadPaths(t *testing.T) {
	got := SplitUploadPaths("/a.zip||/b.zip\n")
	if len(got) != 2 || got[0] != "/a.zip" || got[1] != "/b.zip" {
		t.Fatalf("got %#v", got)
	}
	if len(SplitUploadPaths("  /one  ")) != 1 {
		t.Fatal("single path")
	}
}

func TestResolveUploadPathsSandbox(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "ok.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "nope.txt")
	if err := os.WriteFile(outside, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths, err := ResolveUploadPaths(inside, root, nil)
	if err != nil || len(paths) != 1 {
		t.Fatalf("inside: %v %#v", err, paths)
	}
	_, err = ResolveUploadPaths(outside, root, nil)
	if err == nil || !strings.Contains(err.Error(), "outside workspace") {
		t.Fatalf("expected sandbox reject, got %v", err)
	}
	_, err = ResolveUploadPaths(inside+"||"+outside, root, nil)
	if err == nil {
		t.Fatal("multi-file with outside path must fail")
	}
	// Empty workspace without env → fail closed
	t.Setenv("CODEHELPER_BROWSER_UPLOAD_ALLOW", "")
	_, err = ResolveUploadPaths(inside, "", nil)
	if err == nil || !strings.Contains(err.Error(), "upload sandbox") {
		t.Fatalf("want sandbox config error, got %v", err)
	}
}

func TestBuildFailureDebugPack(t *testing.T) {
	res := &BrowserResult{
		FinalURL: "http://example.test/x",
		Title:    "T",
		Console:  []ConsoleMessage{{Level: "error", Text: "boom"}, {Level: "log", Text: "ok"}},
		Failed:   []FailedRequest{{URL: "http://example.test/a", Status: 500}},
		ActionLog: []string{
			"step 1 fill — ok",
			"step 2 assert — FAILED: text missing",
		},
		Outline:  []OutlineElement{{Selector: "#x", Role: "button", Name: "Go"}},
		Snapshot: "button \"Go\"",
	}
	pack := BuildFailureDebugPack(res)
	if pack == nil || !pack.Failed {
		t.Fatal("expected failed pack")
	}
	if len(pack.ConsoleErrors) != 1 || pack.ConsoleErrors[0].Text != "boom" {
		t.Fatalf("console errors: %#v", pack.ConsoleErrors)
	}
	if pack.Snapshot == "" || len(pack.Outline) != 1 || len(pack.FailedRequests) != 1 {
		t.Fatalf("incomplete pack: %#v", pack)
	}
}

func TestWriteFailureDebugPack(t *testing.T) {
	dir := t.TempDir()
	res := &BrowserResult{
		Format:      FormatWebP,
		Image:       []byte("RIFF....WEBPXXXX"), // not valid enough for magic, but write still happens
		FinalURL:    "http://localhost/",
		ActionLog:   []string{"step 1 click — FAILED: missing"},
		FailureShot: true,
		ActionPreviews: []ActionPreview{{
			Step: 1, Label: "click — FAILED",
			Image: []byte{0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0, 0x57, 0x45, 0x42, 0x50},
		}},
	}
	pack, err := WriteFailureDebugPack(dir, res)
	if err != nil {
		t.Fatal(err)
	}
	if pack.ReportPath == "" || pack.ScreenshotPath == "" {
		t.Fatalf("paths missing: %#v", pack)
	}
	if _, err := os.Stat(pack.ReportPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pack.ScreenshotPath); err != nil {
		t.Fatal(err)
	}
}
