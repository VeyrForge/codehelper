package watcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestWatcher_DebouncesBurst(t *testing.T) {
	dir := t.TempDir()
	// Pre-create the file before starting so the create event isn't part of
	// what we measure (we focus on burst write coalescing).
	target := filepath.Join(dir, "main.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := New(ctx, Options{
		Root:        dir,
		Debounce:    120 * time.Millisecond,
		MinDebounce: 120 * time.Millisecond,
		MaxWait:     800 * time.Millisecond,
		Include:     func(rel string) bool { return strings.HasSuffix(rel, ".go") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var batches int
	var totalPaths int
	done := make(chan struct{})
	var mu sync.Mutex
	go func() {
		for ev := range w.Events() {
			mu.Lock()
			batches++
			totalPaths += len(ev.ChangedPaths)
			mu.Unlock()
		}
		close(done)
	}()

	for i := 0; i < 10; i++ {
		if err := os.WriteFile(target, []byte("package main\n// "+time.Now().String()), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	time.Sleep(450 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if batches == 0 {
		t.Fatalf("expected at least one batch, got 0")
	}
	if batches > 4 {
		t.Fatalf("expected coalescing to bound batches; got %d", batches)
	}
}

func TestWatcher_IgnoresSkippedDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := New(ctx, Options{
		Root:     dir,
		Debounce: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var got int
	done := make(chan struct{})
	go func() {
		for range w.Events() {
			got++
		}
		close(done)
	}()

	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "x.js"), []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done
	if got != 0 {
		t.Fatalf("expected 0 batches from ignored dirs, got %d", got)
	}
}

func TestWatcher_ForceFlushWhenPendingLimitReached(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := New(ctx, Options{
		Root:            dir,
		Debounce:        2 * time.Second,
		MinDebounce:     2 * time.Second,
		MaxWait:         5 * time.Second,
		MaxPendingFlush: 1,
		Include:         func(rel string) bool { return strings.HasSuffix(rel, ".go") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := os.WriteFile(target, []byte("package main\n// change"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-w.Events():
		if len(ev.ChangedPaths) == 0 {
			t.Fatalf("expected changed paths in forced flush batch")
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatalf("expected forced flush before long debounce window")
	}
}

func TestWatcher_RelForRootRejectsPrefixCollision(t *testing.T) {
	rootParent := t.TempDir()
	root := filepath.Join(rootParent, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(rootParent, "repo-other", "x.go")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &Watcher{opt: Options{Root: root}}
	if _, ok := w.relForRoot(outside); ok {
		t.Fatalf("expected prefix-collision path to be rejected")
	}
}

func TestWatcher_HandleOverflowQueuesResyncBatch(t *testing.T) {
	w := &Watcher{
		opt: Options{
			Root:            t.TempDir(),
			Debounce:        time.Second,
			MinDebounce:     time.Second,
			MaxWait:         time.Second,
			MaxPendingFlush: 32,
		},
		out:     make(chan Event, 1),
		pending: map[string]struct{}{},
	}
	if !w.handleWatcherError(errors.Join(fsnotify.ErrEventOverflow)) {
		t.Fatalf("expected overflow error to be handled")
	}
	select {
	case ev := <-w.out:
		if len(ev.ChangedPaths) != 1 || ev.ChangedPaths[0] != ".watcher_overflow" {
			t.Fatalf("unexpected overflow batch: %+v", ev.ChangedPaths)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected overflow to force resync batch")
	}
}

// TestWatcher_RequeuesBatchWhenConsumerBehind guards the regression where a
// flush whose send blocked on a full out channel was silently dropped, leaving
// a file that was written once (and never touched again) permanently
// unindexed while freshness still reported clean.
func TestWatcher_RequeuesBatchWhenConsumerBehind(t *testing.T) {
	w := &Watcher{
		opt:     Options{Root: t.TempDir(), Debounce: 10 * time.Millisecond},
		out:     make(chan Event, 1),
		pending: map[string]struct{}{},
	}
	// Saturate the out channel so the next send cannot proceed.
	w.out <- Event{ChangedPaths: []string{"already_queued.go"}}

	// Stage a batch and flush it; the send must fail and re-queue, not drop.
	w.mu.Lock()
	w.pending["lost.go"] = struct{}{}
	w.firstSeen = time.Now()
	w.mu.Unlock()
	w.flush()

	w.mu.Lock()
	_, requeued := w.pending["lost.go"]
	timerSet := w.timer != nil
	w.mu.Unlock()
	if !requeued {
		t.Fatal("batch was dropped: lost.go not re-queued when consumer was behind")
	}
	if !timerSet {
		t.Fatal("expected a retry flush to be scheduled after re-queue")
	}

	// Free the channel; the scheduled retry must eventually deliver lost.go.
	<-w.out
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-w.out:
			for _, p := range ev.ChangedPaths {
				if p == "lost.go" {
					return
				}
			}
		case <-deadline:
			t.Fatal("re-queued batch was never re-delivered")
		}
	}
}
