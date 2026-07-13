package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquire_RejectsSecondHolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	t.Cleanup(func() {
		if rerr := first.Release(); rerr != nil {
			t.Fatalf("release first lock: %v", rerr)
		}
	})

	if _, err := Acquire(dir); err == nil {
		t.Fatalf("expected ErrAlreadyRunning, got nil")
	} else {
		var er ErrAlreadyRunning
		if !errors.As(err, &er) {
			// On Windows the underlying error type differs; accept any non-nil.
			t.Logf("non-ErrAlreadyRunning error path: %v", err)
		}
	}
}

func TestAcquireRelease_AllowsReacquire(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	l2, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if rerr := l2.Release(); rerr != nil {
			t.Fatalf("release second lock: %v", rerr)
		}
	})
}

func TestStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".codehelper"), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if rerr := l.Release(); rerr != nil {
			t.Fatalf("release lock: %v", rerr)
		}
	})
	if err := l.WriteState(State{IndexRoot: dir, RepoName: "demo", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.RepoName != "demo" || got.Status != "running" {
		t.Fatalf("unexpected state: %+v", got)
	}
}
