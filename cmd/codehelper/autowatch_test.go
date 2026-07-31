package main

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/VeyrForge/codehelper/internal/paths"
)

func TestShouldAutoWatch_SkipsEphemeralPaths(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join("F:", "Projects", "codehelper"), true},
		{filepath.Join("F:", "Projects", "vireo"), true},
		{filepath.Join("C:", "UE_Projects", "SoulsLike"), true},
		{filepath.Join("F:", "x", ".testbeds", "active", "nest"), false},
		{filepath.Join("F:", "x", ".eval-projects", "express"), false},
		{filepath.Join("F:", "x", ".ci-testbeds", "foo"), false},
		{filepath.Join("F:", "x", ".ci-testbeds-extended", "foo"), false},
		{filepath.Join("F:", "x", ".stub-src", "dart"), false},
		{filepath.Join("F:", "x", "testdata", "workspace-groups"), false},
	}
	for _, tc := range cases {
		if got := shouldAutoWatch(tc.path); got != tc.want {
			t.Fatalf("shouldAutoWatch(%q)=%v want %v", tc.path, got, tc.want)
		}
	}
}

func TestCountLiveWatchDaemons_PathIdentity(t *testing.T) {
	// countLiveWatchDaemons uses paths.EqualIndexRoot so Windows slash/case
	// variants of the same workspace count as hasSelf (avoid duplicate daemons).
	a := filepath.FromSlash("F:/Projects/codehelper")
	b := filepath.FromSlash("F:/Projects/codehelper/")
	if !paths.EqualIndexRoot(a, b) {
		t.Fatalf("slash variants must match for watch identity")
	}
	if runtime.GOOS == "windows" {
		c := `f:\Projects\codehelper`
		if !paths.EqualIndexRoot(a, c) {
			t.Fatalf("Windows case variants must match for watch identity")
		}
	}
}

func TestMaxAutoWatchDaemons_DefaultsBounded(t *testing.T) {
	t.Setenv("CODEHELPER_MAX_WATCH_DAEMONS", "")
	n := maxAutoWatchDaemons()
	if n < 2 || n > 6 {
		t.Fatalf("default max=%d outside [2,6] (cpus=%d)", n, runtime.NumCPU())
	}
	t.Setenv("CODEHELPER_MAX_WATCH_DAEMONS", "3")
	if maxAutoWatchDaemons() != 3 {
		t.Fatalf("env override ignored: got %d", maxAutoWatchDaemons())
	}
	t.Setenv("CODEHELPER_MAX_WATCH_DAEMONS", "0")
	if maxAutoWatchDaemons() != 0 {
		t.Fatalf("want 0 (disable auto), got %d", maxAutoWatchDaemons())
	}
}

func TestAutoWatchDisabled(t *testing.T) {
	t.Setenv("CODEHELPER_AUTO_WATCH", "")
	t.Setenv("CODEHELPER_NO_AUTO_WATCH", "")
	if autoWatchDisabled() {
		t.Fatal("expected enabled by default")
	}
	t.Setenv("CODEHELPER_AUTO_WATCH", "0")
	if !autoWatchDisabled() {
		t.Fatal("CODEHELPER_AUTO_WATCH=0 should disable")
	}
	t.Setenv("CODEHELPER_AUTO_WATCH", "")
	t.Setenv("CODEHELPER_NO_AUTO_WATCH", "1")
	if !autoWatchDisabled() {
		t.Fatal("CODEHELPER_NO_AUTO_WATCH=1 should disable")
	}
}
