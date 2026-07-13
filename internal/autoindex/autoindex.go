// Package autoindex orchestrates a Watcher with the indexer pipeline so that
// edits trigger incremental analyze runs without manual intervention. It is the
// engine behind `codehelper watch`.
package autoindex

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/watcher"
)

// Options configures the auto-indexer engine.
type Options struct {
	// Path is the directory to watch and index. Defaults to cwd when empty.
	Path string
	// RepoName is the registry name override.
	RepoName string
	// ShardName is the logical name when watching a subdirectory shard.
	ShardName string
	// IndexSubdir mirrors `analyze --path` semantics: a subdirectory under
	// the git root to treat as its own workspace.
	IndexSubdir string
	// Invalidation expansion mode for incremental runs.
	Invalidation indexer.InvalidationMode
	// Cooldown is the minimum gap between two successive indexer.Run calls
	// to avoid hammering CPU/disk during long burst sessions.
	Cooldown time.Duration
	// Debounce overrides watcher.DefaultDebounce when > 0.
	Debounce time.Duration
	// MaxWait overrides watcher.DefaultMaxWait when > 0.
	MaxWait time.Duration
	// InitialAnalyze runs a synchronous incremental analyze before starting
	// the watcher. Default true so first start gets a fresh index.
	InitialAnalyze bool
	// WatchProfile tunes watcher/cooldown defaults.
	// Allowed: auto|small|large. Empty defaults to auto.
	WatchProfile string
}

// Status is a snapshot of the running auto-indexer for status/MCP callers.
type Status struct {
	Running           bool      `json:"running"`
	IndexRoot         string    `json:"index_root"`
	RepoName          string    `json:"repo_name"`
	LastTriggerReason string    `json:"last_trigger_reason,omitempty"`
	LastRunAt         time.Time `json:"last_run_at,omitempty"`
	LastDuration      string    `json:"last_duration,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
	PendingPaths      int       `json:"pending_paths"`
	TotalRuns         int64     `json:"total_runs"`
	WatchProfile      string    `json:"watch_profile"`
}

// Engine wraps a watcher + indexer loop with cooldown and pending-batch state.
type Engine struct {
	opt       Options
	gitRoot   string
	indexRoot string
	repoName  string

	mu         sync.Mutex
	pending    map[string]struct{}
	lastReason string
	lastRunAt  time.Time
	lastDur    time.Duration
	lastErr    string
	runs       atomic.Int64
	running    atomic.Bool
	indexing   atomic.Bool
}

// New constructs an Engine. It does not start any goroutines; call Run.
func New(opt Options) (*Engine, error) {
	if strings.TrimSpace(opt.Path) == "" {
		return nil, errors.New("autoindex: Path is required")
	}
	gitRoot, indexRoot, err := indexer.ResolveIndexPaths(opt.Path, opt.IndexSubdir)
	if err != nil {
		return nil, err
	}
	opt = applyScaleDefaults(opt, metaOrNil(indexRoot))
	if opt.Cooldown <= 0 {
		opt.Cooldown = 750 * time.Millisecond
	}
	if opt.Invalidation == "" {
		opt.Invalidation = indexer.InvalidationEager
	}
	repoName := strings.TrimSpace(opt.RepoName)
	if repoName == "" {
		repoName = strings.TrimSpace(opt.ShardName)
	}
	if repoName == "" {
		repoName = filepath.Base(indexRoot)
	}
	return &Engine{
		opt:       opt,
		gitRoot:   gitRoot,
		indexRoot: indexRoot,
		repoName:  repoName,
		pending:   map[string]struct{}{},
	}, nil
}

func metaOrNil(indexRoot string) *meta.Data {
	m, _ := meta.Read(indexRoot)
	return m
}

func applyScaleDefaults(opt Options, m *meta.Data) Options {
	profile := normalizeProfile(opt.WatchProfile)
	opt.WatchProfile = profile
	if profile == "small" {
		return applySmallDefaults(opt)
	}
	if profile == "large" {
		return applyLargeDefaults(opt)
	}
	// auto profile: infer from existing index metadata.
	if m == nil {
		return applySmallDefaults(opt)
	}
	largeRepo := m.FileCount >= 8000 || m.SymbolCount >= 120000
	if largeRepo {
		return applyLargeDefaults(opt)
	}
	return applySmallDefaults(opt)
}

func normalizeProfile(profile string) string {
	p := strings.ToLower(strings.TrimSpace(profile))
	switch p {
	case "", "auto":
		return "auto"
	case "small":
		return "small"
	case "large":
		return "large"
	default:
		return "auto"
	}
}

func applySmallDefaults(opt Options) Options {
	if opt.Cooldown <= 0 {
		opt.Cooldown = 750 * time.Millisecond
	}
	if opt.Debounce <= 0 {
		opt.Debounce = watcher.DefaultDebounce
	}
	if opt.MaxWait <= 0 {
		opt.MaxWait = watcher.DefaultMaxWait
	}
	if opt.Invalidation == "" {
		opt.Invalidation = indexer.InvalidationEager
	}
	return opt
}

func applyLargeDefaults(opt Options) Options {
	if opt.Cooldown <= 0 {
		opt.Cooldown = 1500 * time.Millisecond
	}
	if opt.Debounce <= 0 {
		opt.Debounce = 650 * time.Millisecond
	}
	if opt.MaxWait <= 0 {
		opt.MaxWait = 5 * time.Second
	}
	if opt.Invalidation == "" {
		opt.Invalidation = indexer.InvalidationLazy
	}
	return opt
}

// IndexRoot returns the directory being indexed.
func (e *Engine) IndexRoot() string { return e.indexRoot }

// RepoName returns the resolved repository name used for registry entries.
func (e *Engine) RepoName() string { return e.repoName }

// Status returns a snapshot suitable for status/MCP callers.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := Status{
		Running:           e.running.Load(),
		IndexRoot:         e.indexRoot,
		RepoName:          e.repoName,
		LastTriggerReason: e.lastReason,
		LastRunAt:         e.lastRunAt,
		LastError:         e.lastErr,
		PendingPaths:      len(e.pending),
		TotalRuns:         e.runs.Load(),
		WatchProfile:      normalizeProfile(e.opt.WatchProfile),
	}
	if e.lastDur > 0 {
		st.LastDuration = e.lastDur.String()
	}
	return st
}

// Run starts the watcher loop and blocks until ctx is canceled or the
// watcher channel closes.
func (e *Engine) Run(ctx context.Context) error {
	if e.running.Swap(true) {
		return errors.New("autoindex: engine already running")
	}
	defer e.running.Store(false)

	if e.opt.InitialAnalyze {
		if err := e.runIndex(ctx, "initial"); err != nil {
			slog.Warn("initial analyze failed", "err", err)
		} else {
			e.upsertRegistry(ctx)
		}
	}

	// Prune gitignored directories from the recursive watch — the same trees the
	// indexer skips. Otherwise the watcher registers an inotify watch per dir
	// across vendored fixture trees (e.g. .testbeds/), which is slow and can
	// exhaust the per-user inotify watch limit.
	var ignore func(string) bool
	if gi, gerr := indexer.LoadLayeredGitIgnoreMatcher(e.gitRoot); gerr == nil && gi != nil {
		if skip := indexer.GitIgnoreSkipFunc(e.gitRoot, e.indexRoot, gi); skip != nil {
			ignore = func(rel string) bool { return skip(rel) || skip(rel+"/") }
		}
	}
	w, err := watcher.New(ctx, watcher.Options{
		Root:     e.indexRoot,
		Include:  isSourceFile,
		Ignore:   ignore,
		Debounce: e.opt.Debounce,
		MaxWait:  e.opt.MaxWait,
	})
	if err != nil {
		return err
	}
	defer w.Close()

	cooldown := time.NewTicker(e.opt.Cooldown)
	defer cooldown.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case batch, ok := <-w.Events():
			if !ok {
				return nil
			}
			e.queue(batch)
		case <-cooldown.C:
			if e.hasPending() && !e.indexing.Load() {
				if err := e.drain(ctx); err != nil {
					slog.Warn("auto-index run failed", "err", err)
				}
			}
		}
	}
}

func (e *Engine) queue(b watcher.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range b.ChangedPaths {
		e.pending[p] = struct{}{}
	}
	e.lastReason = "watch:" + b.FlushReason
}

func (e *Engine) hasPending() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pending) > 0
}

func (e *Engine) drain(ctx context.Context) error {
	e.mu.Lock()
	if len(e.pending) == 0 {
		e.mu.Unlock()
		return nil
	}
	count := len(e.pending)
	e.pending = map[string]struct{}{}
	reason := e.lastReason
	e.mu.Unlock()
	slog.Info("auto-index run", "paths", count, "reason", reason, "root", e.indexRoot)
	if err := e.runIndex(ctx, reason); err != nil {
		return err
	}
	e.upsertRegistry(ctx)
	return nil
}

func (e *Engine) runIndex(ctx context.Context, reason string) error {
	if e.indexing.Swap(true) {
		return errors.New("autoindex: run already in progress")
	}
	defer e.indexing.Store(false)

	t0 := time.Now()
	err := indexer.Run(ctx, e.opt.Path, indexer.Options{
		RepoName:     e.opt.RepoName,
		ShardName:    e.opt.ShardName,
		IndexSubdir:  e.opt.IndexSubdir,
		Invalidation: e.opt.Invalidation,
	})
	dur := time.Since(t0)

	e.mu.Lock()
	e.lastRunAt = time.Now().UTC()
	e.lastDur = dur
	if err != nil {
		e.lastErr = err.Error()
	} else {
		e.lastErr = ""
	}
	e.lastReason = reason
	e.mu.Unlock()
	e.runs.Add(1)
	return err
}

func (e *Engine) upsertRegistry(ctx context.Context) {
	_ = ctx
	reg, err := registry.Load()
	if err != nil {
		return
	}
	m, _ := meta.Read(e.indexRoot)
	commit := ""
	if m != nil {
		commit = m.LastCommit
	}
	if err := reg.Upsert(e.repoName, e.indexRoot, commit, meta.SchemaVersion); err == nil {
		_ = reg.Save()
	}
}

func isSourceFile(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	if ext == "" {
		return false
	}
	_, ok := indexer.SourceExtensions[ext]
	return ok
}
