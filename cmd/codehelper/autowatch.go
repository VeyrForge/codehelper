package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/VeyrForge/codehelper/internal/daemon"
	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/registry"
)

// ephemeralWatchPathSegments are path elements that mark throwaway / fixture
// trees. Analyzing these (testbeds, eval clones, stubs) must NOT leave a
// permanent watch daemon behind — that is what spawned 18+ eager watchers and
// lagged the machine after a bulk reindex.
var ephemeralWatchPathSegments = map[string]struct{}{
	".testbeds":             {},
	".eval-projects":        {},
	".ci-testbeds":          {},
	".ci-testbeds-extended": {},
	".stub-src":             {},
	"testdata":              {},
}

// autoEnsureWatchDaemon starts watch daemon for one workspace if not already
// running. It is intentionally best-effort to avoid blocking primary commands.
// Skips ephemeral fixture paths and respects a global live-daemon cap so a
// bulk `analyze` of many repos cannot saturate the host.
func autoEnsureWatchDaemon(pathArg, subdir string) {
	if os.Getenv("CODEHELPER_WATCH_DAEMON") == "1" {
		return
	}
	if autoWatchDisabled() {
		return
	}
	absPath := pathArg
	if absPath == "" {
		wd, err := os.Getwd()
		if err != nil {
			return
		}
		absPath = wd
	}
	_, indexRoot, err := indexer.ResolveIndexPaths(absPath, subdir)
	if err != nil {
		return
	}
	if !shouldAutoWatch(indexRoot) {
		slog.Debug("auto watch daemon skipped (ephemeral path)", "root", indexRoot)
		return
	}
	if atAutoWatchCap(indexRoot) {
		slog.Debug("auto watch daemon skipped (global cap)", "root", indexRoot, "max", maxAutoWatchDaemons())
		return
	}
	lock, err := daemon.Acquire(indexRoot)
	if err == nil {
		_ = lock.Release()
		if _, err := spawnDaemon(absPath, "", "", subdir, "eager", 0, 0); err != nil {
			slog.Debug("auto watch daemon", "root", indexRoot, "err", err)
		}
		return
	}
	var already daemon.ErrAlreadyRunning
	if errors.As(err, &already) {
		return
	}
	slog.Debug("auto watch daemon lock", "root", indexRoot, "err", err)
}

// autoEnsureWatchDaemonsForRegistry ensures indexed repos have a running watch
// daemon, subject to the same ephemeral-path and global-cap gates as
// autoEnsureWatchDaemon. Best-effort by design; it should never block MCP startup.
func autoEnsureWatchDaemonsForRegistry(reg *registry.Registry) {
	if reg == nil || os.Getenv("CODEHELPER_WATCH_DAEMON") == "1" || autoWatchDisabled() {
		return
	}
	for _, e := range reg.List() {
		if e.RootPath == "" {
			continue
		}
		if _, _, err := indexer.ResolveIndexPaths(e.RootPath, ""); err != nil {
			continue
		}
		if !shouldAutoWatch(e.RootPath) {
			continue
		}
		if atAutoWatchCap(e.RootPath) {
			slog.Debug("auto watch daemon registry capped", "max", maxAutoWatchDaemons())
			return
		}
		lock, err := daemon.Acquire(e.RootPath)
		if err == nil {
			_ = lock.Release()
			_, err = spawnDaemon(e.RootPath, e.Name, "", "", "eager", 0, 0)
		}
		if err != nil {
			var already daemon.ErrAlreadyRunning
			if errors.As(err, &already) {
				continue
			}
			slog.Debug("auto watch daemon registry", "repo", e.Name, "root", e.RootPath, "err", err)
		}
	}
}

func autoWatchDisabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("CODEHELPER_AUTO_WATCH")))
	if v == "0" || v == "false" || v == "off" || v == "no" {
		return true
	}
	if strings.TrimSpace(os.Getenv("CODEHELPER_NO_AUTO_WATCH")) == "1" {
		return true
	}
	return false
}

// shouldAutoWatch reports whether indexRoot is a durable project that may get
// an auto-started watch daemon (vs fixtures / nested test trees).
func shouldAutoWatch(indexRoot string) bool {
	cleaned := filepath.Clean(indexRoot)
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		if _, ok := ephemeralWatchPathSegments[strings.ToLower(seg)]; ok {
			return false
		}
	}
	return true
}

// atAutoWatchCap is true when the global live-watch count has reached the cap
// and indexRoot is not already one of the live daemons (so re-ensure is OK).
func atAutoWatchCap(indexRoot string) bool {
	max := maxAutoWatchDaemons()
	if max <= 0 {
		return true
	}
	live, hasSelf := countLiveWatchDaemons(indexRoot)
	if hasSelf {
		return false
	}
	return live >= max
}

func maxAutoWatchDaemons() int {
	if v := strings.TrimSpace(os.Getenv("CODEHELPER_MAX_WATCH_DAEMONS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n < 0 {
				return 0
			}
			return n
		}
	}
	// Leave most cores for the editor; a few durable project watches are enough.
	n := runtime.NumCPU() / 8
	if n < 2 {
		n = 2
	}
	if n > 6 {
		n = 6
	}
	return n
}

// countLiveWatchDaemons returns how many registry repos currently have a live
// watch daemon, and whether indexRoot is among them.
func countLiveWatchDaemons(indexRoot string) (live int, hasSelf bool) {
	self := indexRoot
	reg, err := registry.Load()
	if err != nil || reg == nil {
		return 0, false
	}
	seen := map[int]struct{}{}
	for _, e := range reg.List() {
		if e.RootPath == "" {
			continue
		}
		st, rerr := daemon.ReadState(e.RootPath)
		if rerr != nil || st == nil || st.PID <= 0 || !daemon.ProcessAlive(st.PID) {
			continue
		}
		if _, ok := seen[st.PID]; ok {
			continue
		}
		seen[st.PID] = struct{}{}
		live++
		// Windows: F:\x vs f:/x must count as the same live daemon.
		if paths.EqualIndexRoot(e.RootPath, self) || (st.IndexRoot != "" && paths.EqualIndexRoot(st.IndexRoot, self)) {
			hasSelf = true
		}
	}
	// Also check the target root even if it is not (yet) in the registry.
	cleaned := filepath.Clean(self)
	if !hasSelf && cleaned != "" && cleaned != "." {
		if st, rerr := daemon.ReadState(self); rerr == nil && st != nil && st.PID > 0 && daemon.ProcessAlive(st.PID) {
			if _, ok := seen[st.PID]; !ok {
				live++
			}
			hasSelf = true
		}
	}
	return live, hasSelf
}
