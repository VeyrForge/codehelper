package main

import (
	"errors"
	"log/slog"
	"os"

	"github.com/VeyrForge/codehelper/internal/daemon"
	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/registry"
)

// autoEnsureWatchDaemon starts watch daemon for one workspace if not already
// running. It is intentionally best-effort to avoid blocking primary commands.
func autoEnsureWatchDaemon(pathArg, subdir string) {
	if os.Getenv("CODEHELPER_WATCH_DAEMON") == "1" {
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

// autoEnsureWatchDaemonsForRegistry ensures all indexed repos have a running
// watch daemon. Best-effort by design; it should never block MCP startup.
func autoEnsureWatchDaemonsForRegistry(reg *registry.Registry) {
	if reg == nil || os.Getenv("CODEHELPER_WATCH_DAEMON") == "1" {
		return
	}
	for _, e := range reg.List() {
		if e.RootPath == "" {
			continue
		}
		if _, _, err := indexer.ResolveIndexPaths(e.RootPath, ""); err != nil {
			continue
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
