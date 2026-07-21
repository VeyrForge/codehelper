// Package watcher provides a debounced, recursive filesystem watcher for
// triggering incremental indexing on source-file changes.
//
// Design notes:
//   - Uses fsnotify with manual recursive registration (Linux/macOS/Windows portable).
//   - Coalesces rapid bursts of write/create/rename/remove events into a single
//     batch using a debounce timer plus a max-wait ceiling so an unending stream
//     of events still produces forward progress (e.g. during git checkout or
//     code-generation runs).
//   - The fsnotify event loop never blocks: events are pushed onto a bounded
//     channel. If the consumer is slow (the out channel is full) the batch is
//     re-queued and retried on a backoff rather than dropped, so a file written
//     once and never touched again is still guaranteed to be re-indexed.
//   - Symlinks and ignored directories (.git, node_modules, build, ...) are
//     never registered to keep memory bounded and avoid loops.
package watcher

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Event describes a coalesced batch of filesystem changes.
type Event struct {
	// ChangedPaths are repo-relative POSIX paths that have changed since the
	// last batch. They are sorted and deduplicated for deterministic batches.
	ChangedPaths []string
	// FlushReason is "debounce" when emitted after the quiet window or
	// "max_wait" when the burst-ceiling forced a flush.
	FlushReason string
	// At is the flush timestamp (UTC).
	At time.Time
}

// Options configures the watcher. Defaults are chosen to be sensible for
// build/index loops on dev machines.
type Options struct {
	// Root is the absolute directory to watch (recursively).
	Root string
	// Ignore receives a relative POSIX path; when true the path is dropped.
	// May be nil to use only the built-in ignore set.
	Ignore func(relPath string) bool
	// Include receives a relative POSIX path; when set, only matching files
	// are forwarded. May be nil to allow all source-like files.
	Include func(relPath string) bool
	// Debounce is the quiet period after the last event before flushing.
	Debounce time.Duration
	// MinDebounce is the lower debounce bound for tiny edit batches. When
	// pending paths are very small, watcher can flush sooner than Debounce.
	MinDebounce time.Duration
	// MaxWait caps how long a batch can keep getting deferred by new events
	// during a sustained burst (e.g. `git checkout`).
	MaxWait time.Duration
	// MaxPendingFlush forces an immediate flush when pending paths reach this
	// count, preventing unbounded in-memory growth during massive bursts.
	MaxPendingFlush int
	// QueueSize bounds the buffered batch channel.
	QueueSize int
}

// DefaultDebounce is the quiet-window default; long enough to coalesce
// editor save bursts, short enough that human edits feel snappy.
const DefaultDebounce = 350 * time.Millisecond

// DefaultMaxWait protects against indefinitely deferred batches.
const DefaultMaxWait = 3 * time.Second

// DefaultMinDebounce keeps single-file edits snappy without dropping burst
// coalescing for larger saves.
const DefaultMinDebounce = 90 * time.Millisecond

// DefaultMaxPendingFlush bounds pending-path growth in huge repos/bursts.
const DefaultMaxPendingFlush = 512

// builtInSkipDirs mirrors indexer.defaultSkipDirs to keep the watcher cheap.
var builtInSkipDirs = map[string]struct{}{
	"node_modules": {}, "vendor": {}, ".vendor": {}, ".git": {}, "dist": {}, "build": {},
	"out": {}, "tmp": {},
	".codehelper": {}, "target": {}, "obj": {}, "__pycache__": {}, ".venv": {}, "venv": {},
	".idea": {}, ".vscode": {}, "coverage": {}, ".next": {}, ".nuxt": {},
	".mypy_cache": {}, ".pytest_cache": {}, ".cache": {},
	".turbo": {}, ".parcel-cache": {}, ".output": {}, ".svelte-kit": {},
	"storybook-static": {}, ".angular": {}, ".vercel": {}, ".netlify": {},
	".dart_tool": {}, ".gradle": {}, ".tox": {}, ".nyc_output": {},
	"site-packages": {},
}

// Watcher coordinates an fsnotify backend with a debounce/coalesce loop.
type Watcher struct {
	opt       Options
	w         *fsnotify.Watcher
	out       chan Event
	mu        sync.Mutex
	pending   map[string]struct{}
	firstSeen time.Time
	timer     *time.Timer
	outClosed bool
	closed    bool
	closeMu   sync.Mutex
}

// New starts a recursive watcher rooted at opt.Root and returns a Watcher
// whose Events() channel emits coalesced batches. Cancel ctx to shut down.
func New(ctx context.Context, opt Options) (*Watcher, error) {
	if strings.TrimSpace(opt.Root) == "" {
		return nil, errors.New("watcher: Root is required")
	}
	abs, err := filepath.Abs(opt.Root)
	if err != nil {
		return nil, err
	}
	opt.Root = abs
	if opt.Debounce <= 0 {
		opt.Debounce = DefaultDebounce
	}
	if opt.MaxWait <= 0 {
		opt.MaxWait = DefaultMaxWait
	}
	if opt.MinDebounce <= 0 {
		opt.MinDebounce = DefaultMinDebounce
	}
	if opt.MinDebounce > opt.Debounce {
		opt.MinDebounce = opt.Debounce
	}
	if opt.MaxPendingFlush <= 0 {
		opt.MaxPendingFlush = DefaultMaxPendingFlush
	}
	if opt.QueueSize <= 0 {
		opt.QueueSize = 64
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		opt:     opt,
		w:       fw,
		out:     make(chan Event, opt.QueueSize),
		pending: map[string]struct{}{},
	}
	if err := w.addRecursive(opt.Root); err != nil {
		_ = fw.Close()
		return nil, err
	}
	go w.run(ctx)
	return w, nil
}

// Events returns the channel of coalesced batches. The channel closes when
// the watcher exits (after the supplied context is canceled or Close is
// called).
func (w *Watcher) Events() <-chan Event {
	return w.out
}

// Close stops the watcher; safe to call multiple times.
func (w *Watcher) Close() error {
	w.closeMu.Lock()
	defer w.closeMu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.w.Close()
}

// IsIgnoredDir returns true when the directory base name is part of the
// built-in skip set.
func IsIgnoredDir(base string) bool {
	_, ok := builtInSkipDirs[base]
	return ok
}

func (w *Watcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if path != root && IsIgnoredDir(base) {
			return filepath.SkipDir
		}
		// Prune ignored (e.g. gitignored) directories from fsnotify registration.
		// Without this, a gitignored fixture tree (a vendored linux/kubernetes
		// checkout under .testbeds/, say) registers an inotify watch per directory —
		// slow to set up and able to exhaust the PER-USER inotify watch limit,
		// which starves every other watcher on the machine.
		if path != root && w.opt.Ignore != nil {
			if rel, rerr := filepath.Rel(root, path); rerr == nil && w.opt.Ignore(filepath.ToSlash(rel)) {
				return filepath.SkipDir
			}
		}
		if err := w.w.Add(path); err != nil {
			slog.Debug("watcher add failed", "path", path, "err", err)
		}
		return nil
	})
}

func (w *Watcher) run(ctx context.Context) {
	// Close the out channel under the mutex, with outClosed set first, so a
	// concurrent flush() timer goroutine never sends on a closed channel (it
	// observes outClosed and returns). Stop any pending timer so no flush fires
	// after shutdown.
	defer func() {
		w.mu.Lock()
		if w.timer != nil {
			w.timer.Stop()
		}
		w.outClosed = true
		close(w.out)
		w.mu.Unlock()
	}()
	// Self-heal: if the watched root is deleted (e.g. a throwaway clone removed),
	// exit instead of lingering forever watching a path that no longer exists —
	// the leaked-daemon class of bug.
	heal := time.NewTicker(30 * time.Second)
	defer heal.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = w.Close()
			return
		case <-heal.C:
			if st, err := os.Stat(w.opt.Root); err != nil || !st.IsDir() {
				slog.Info("watch root gone; stopping daemon", "root", w.opt.Root)
				_ = w.Close()
				return
			}
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			w.handleEvent(ev)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			if w.handleWatcherError(err) {
				continue
			}
			slog.Debug("watcher error", "err", err)
		}
	}
}

func (w *Watcher) handleWatcherError(err error) bool {
	if !errors.Is(err, fsnotify.ErrEventOverflow) {
		return false
	}
	// Queue a synthetic full-resync signal when backend buffers overflow.
	// This keeps index freshness converging even if individual file events drop.
	w.mu.Lock()
	if len(w.pending) == 0 {
		w.firstSeen = time.Now()
	}
	w.pending[".watcher_overflow"] = struct{}{}
	if w.timer == nil {
		w.timer = time.AfterFunc(0, w.flush)
	} else {
		w.timer.Reset(0)
	}
	w.mu.Unlock()
	if w.w != nil {
		_ = w.addRecursive(w.opt.Root)
	}
	slog.Warn("watcher overflow detected; forcing resync batch", "root", w.opt.Root)
	return true
}

func (w *Watcher) handleEvent(ev fsnotify.Event) {
	if ev.Name == "" {
		return
	}
	if !relevantOp(ev.Op) {
		return
	}

	rel, ok := w.relForRoot(ev.Name)
	if !ok {
		return
	}
	if w.shouldSkip(rel, ev.Op, ev.Name) {
		return
	}

	w.mu.Lock()
	if len(w.pending) == 0 {
		w.firstSeen = time.Now()
	}
	w.pending[rel] = struct{}{}
	w.scheduleFlushLocked()
	w.mu.Unlock()
}

func (w *Watcher) scheduleFlushLocked() {
	delay := w.opt.Debounce
	if len(w.pending) <= 2 && w.opt.MinDebounce > 0 && w.opt.MinDebounce < delay {
		delay = w.opt.MinDebounce
	}
	if !w.firstSeen.IsZero() {
		elapsed := time.Since(w.firstSeen)
		if elapsed+delay > w.opt.MaxWait {
			remaining := w.opt.MaxWait - elapsed
			if remaining < 0 {
				remaining = 0
			}
			delay = remaining
		}
	}
	if len(w.pending) >= w.opt.MaxPendingFlush {
		if w.timer == nil {
			w.timer = time.AfterFunc(0, w.flush)
			return
		}
		w.timer.Reset(0)
		return
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(delay, w.flush)
		return
	}
	w.timer.Reset(delay)
}

func (w *Watcher) flush() {
	w.mu.Lock()
	if len(w.pending) == 0 {
		w.mu.Unlock()
		return
	}
	paths := make([]string, 0, len(w.pending))
	for p := range w.pending {
		paths = append(paths, p)
	}
	w.pending = map[string]struct{}{}
	reason := "debounce"
	if !w.firstSeen.IsZero() && time.Since(w.firstSeen) >= w.opt.MaxWait {
		reason = "max_wait"
	}
	w.firstSeen = time.Time{}
	w.mu.Unlock()
	sort.Strings(paths)
	ev := Event{ChangedPaths: paths, FlushReason: reason, At: time.Now().UTC()}

	// Send and close are serialized under mu (run()'s shutdown closes w.out with
	// outClosed set first) so this never sends on a closed channel.
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.outClosed {
		return
	}
	select {
	case w.out <- ev:
	default:
		// Consumer is behind (its bounded channel is full — e.g. a multi-second
		// index run is blocking the drain loop). Do NOT drop: a file written once
		// and never touched again would otherwise never be re-indexed, leaving the
		// graph silently stale while freshness still reports clean. Re-queue the
		// paths and retry on the next debounce tick so nothing is ever lost.
		w.requeueLocked(paths)
		slog.Debug("watcher backpressure: consumer behind, re-queued batch", "paths", len(paths))
	}
}

// requeueLocked merges undelivered paths back into the pending set and
// reschedules a flush, so a batch that couldn't be sent (full out channel) is
// retried instead of dropped. The retry uses the full debounce as a fixed
// backoff so a stalled consumer can't spin this hot. Callers must hold w.mu.
func (w *Watcher) requeueLocked(paths []string) {
	if w.firstSeen.IsZero() {
		w.firstSeen = time.Now()
	}
	for _, p := range paths {
		w.pending[p] = struct{}{}
	}
	delay := w.opt.Debounce
	if delay <= 0 {
		delay = DefaultDebounce
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(delay, w.flush)
	} else {
		w.timer.Reset(delay)
	}
}

func (w *Watcher) relForRoot(name string) (string, bool) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(w.opt.Root, abs)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func (w *Watcher) shouldSkip(rel string, op fsnotify.Op, abs string) bool {
	if rel == "" || rel == "." {
		return true
	}
	parts := strings.Split(rel, "/")
	for _, seg := range parts {
		if IsIgnoredDir(seg) {
			return true
		}
	}
	if isEditorTemp(filepath.Base(rel)) {
		return true
	}
	if w.opt.Ignore != nil && w.opt.Ignore(rel) {
		return true
	}
	// Try to dynamically register newly created directories so their files
	// are watched. Best-effort; ignore errors.
	if op&fsnotify.Create != 0 {
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			if !IsIgnoredDir(filepath.Base(abs)) {
				_ = w.w.Add(abs)
			}
			return true
		}
	}
	if w.opt.Include != nil && !w.opt.Include(rel) {
		return true
	}
	return false
}

func isEditorTemp(base string) bool {
	if base == "" {
		return true
	}
	if strings.HasPrefix(base, ".#") || strings.HasSuffix(base, "~") {
		return true
	}
	if strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, ".swo") {
		return true
	}
	if strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".temp") {
		return true
	}
	if base == "4913" { // vim atomic-save sentinel
		return true
	}
	return false
}

func relevantOp(op fsnotify.Op) bool {
	if op&fsnotify.Write != 0 {
		return true
	}
	if op&fsnotify.Create != 0 {
		return true
	}
	if op&fsnotify.Remove != 0 {
		return true
	}
	if op&fsnotify.Rename != 0 {
		return true
	}
	return false
}
