package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/VeyrForge/codehelper/internal/autoindex"
	"github.com/VeyrForge/codehelper/internal/daemon"
	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/spf13/cobra"
)

// watchCmd starts a continuous, debounced auto-index loop over a repo or
// shard. When --daemon is set the process is detached and a single-instance
// PID lock is acquired under .codehelper/watch.lock.
func watchCmd() *cobra.Command {
	var name, shard, subdir string
	var invalidation string
	var watchProfile string
	var daemonize, stopRequested, statusOnly bool
	var debounceMs, maxWaitMs int
	c := &cobra.Command{
		Use:   "watch [path]",
		Short: "Auto-index on file change (foreground or --daemon)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			abs, err := filepath.Abs(root)
			if err != nil {
				return err
			}

			_, indexRoot, perr := indexer.ResolveIndexPaths(abs, subdir)
			if perr != nil {
				return perr
			}

			if statusOnly {
				return printWatchStatus(indexRoot)
			}
			if stopRequested {
				return stopDaemon(indexRoot)
			}
			if daemonize {
				pid, err := spawnDaemon(abs, name, shard, subdir, invalidation, debounceMs, maxWaitMs)
				if err != nil {
					return err
				}
				fmt.Printf("watch daemon started (pid=%d)\n", pid)
				return nil
			}

			invMode := indexer.InvalidationMode(strings.ToLower(strings.TrimSpace(invalidation)))
			if invMode != "" && invMode != indexer.InvalidationLazy && invMode != indexer.InvalidationEager {
				return errInvalidInvalidation
			}
			profile := strings.ToLower(strings.TrimSpace(watchProfile))
			if profile != "" && profile != "auto" && profile != "small" && profile != "large" {
				return fmt.Errorf("invalid --watch-profile: %s", watchProfile)
			}
			autoEnsureCodehelperGitignore(abs)

			eng, err := autoindex.New(autoindex.Options{
				Path:           abs,
				RepoName:       name,
				ShardName:      shard,
				IndexSubdir:    subdir,
				Invalidation:   invMode,
				Debounce:       msToDuration(debounceMs),
				MaxWait:        msToDuration(maxWaitMs),
				InitialAnalyze: true,
				WatchProfile:   profile,
			})
			if err != nil {
				return err
			}

			if os.Getenv("CODEHELPER_WATCH_DAEMON") == "1" {
				return runAsDaemon(cmd.Context(), eng)
			}
			return runForeground(cmd.Context(), eng)
		},
	}
	c.Flags().StringVar(&name, "name", "", "repository name for registry")
	c.Flags().StringVar(&shard, "shard", "", "logical repo name for sharded subdirectories")
	c.Flags().StringVar(&subdir, "path", "", "subdirectory under git root to watch as its own workspace")
	c.Flags().StringVar(&invalidation, "invalidation", "eager", "transitive invalidation: eager|lazy")
	c.Flags().StringVar(&watchProfile, "watch-profile", "auto", "watch tuning profile: auto|small|large")
	c.Flags().BoolVar(&daemonize, "daemon", false, "spawn detached background watcher (single-instance per repo)")
	c.Flags().BoolVar(&stopRequested, "stop", false, "stop the running daemon for this repo (if any)")
	c.Flags().BoolVar(&statusOnly, "status", false, "print current daemon state and exit")
	c.Flags().IntVar(&debounceMs, "debounce-ms", 0, "override debounce window in milliseconds")
	c.Flags().IntVar(&maxWaitMs, "max-wait-ms", 0, "override burst max-wait ceiling in milliseconds")
	c.SilenceUsage = true
	return c
}

func runForeground(parent context.Context, eng *autoindex.Engine) error {
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	st := eng.Status()
	slog.Info("watch foreground started", "root", eng.IndexRoot(), "repo", eng.RepoName(), "profile", st.WatchProfile)
	err := eng.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func runAsDaemon(parent context.Context, eng *autoindex.Engine) error {
	// Background reindexing must never lag the editor: nice ourselves so interactive
	// work always wins the CPU. Combined with the bounded parse-worker pool, this
	// keeps codehelper from saturating the machine even with several IDE windows.
	lowerPriority()
	startedAt := time.Now().UTC()
	lock, err := daemon.Acquire(eng.IndexRoot())
	if err != nil {
		return err
	}
	defer func() {
		if rerr := lock.Release(); rerr != nil {
			slog.Warn("watch daemon lock release", "err", rerr)
		}
	}()
	if werr := lock.WriteState(daemon.State{
		PID:       os.Getpid(),
		StartedAt: startedAt,
		IndexRoot: eng.IndexRoot(),
		RepoName:  eng.RepoName(),
		Status:    "starting",
	}); werr != nil {
		slog.Warn("daemon state write", "err", werr)
	}
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				st := eng.Status()
				_ = lock.WriteState(daemon.State{
					PID:       os.Getpid(),
					StartedAt: startedAt,
					IndexRoot: eng.IndexRoot(),
					RepoName:  eng.RepoName(),
					Status:    fmt.Sprintf("running profile=%s runs=%d pending=%d", st.WatchProfile, st.TotalRuns, st.PendingPaths),
				})
			}
		}
	}()
	err = eng.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func spawnDaemon(path, name, shard, subdir, invalidation string, debounceMs, maxWaitMs int) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	args := []string{"watch", path,
		"--invalidation", invalidation,
	}
	if name != "" {
		args = append(args, "--name", name)
	}
	if shard != "" {
		args = append(args, "--shard", shard)
	}
	if subdir != "" {
		args = append(args, "--path", subdir)
	}
	if debounceMs > 0 {
		args = append(args, "--debounce-ms", fmt.Sprint(debounceMs))
	}
	if maxWaitMs > 0 {
		args = append(args, "--max-wait-ms", fmt.Sprint(maxWaitMs))
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "CODEHELPER_WATCH_DAEMON=1")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	detachAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return pid, nil
}

func stopDaemon(indexRoot string) error {
	st, err := daemon.ReadState(indexRoot)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("watch daemon not running")
			return nil
		}
		return err
	}
	if st.PID <= 0 {
		fmt.Println("watch daemon not running")
		return nil
	}
	pid := st.PID
	lock, lerr := daemon.Acquire(indexRoot)
	if lerr == nil {
		// Lock is free, so no active daemon owns this repo lock.
		_ = lock.Release()
		fmt.Println("watch daemon not running")
		return nil
	}
	var already daemon.ErrAlreadyRunning
	if errors.As(lerr, &already) && already.PID > 0 {
		pid = already.PID
	}
	if pid <= 1 {
		return errors.New("could not determine running daemon pid safely")
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	fmt.Printf("sent SIGTERM to watch daemon pid=%d\n", pid)
	return nil
}

func printWatchStatus(indexRoot string) error {
	st, err := daemon.ReadState(indexRoot)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("watch daemon not running")
			return nil
		}
		return err
	}
	fmt.Printf("pid=%d started=%s status=%q root=%s repo=%s\n",
		st.PID, st.StartedAt.Format(time.RFC3339), st.Status, st.IndexRoot, st.RepoName)
	return nil
}

func msToDuration(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
