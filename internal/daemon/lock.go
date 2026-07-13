// Package daemon provides single-instance lockfile + status persistence for
// the auto-indexing daemon (`codehelper watch --daemon`).
//
// The lock file lives under the repo's `.codehelper/` directory so that one
// daemon per repo is enforced naturally and uninstall removes it.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// State is the JSON document persisted alongside the lockfile so other
// callers (status, MCP resources) can read what the daemon is doing.
type State struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	IndexRoot string    `json:"index_root"`
	RepoName  string    `json:"repo_name"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Status    string    `json:"status,omitempty"`
}

// Lock is an exclusive PID lock over a daemon's index root.
type Lock struct {
	path  string
	state string
	f     *os.File
}

// LockPath returns the lockfile path for an index root.
func LockPath(indexRoot string) string {
	return filepath.Join(paths.RepoIndexDir(indexRoot), "watch.lock")
}

// StatePath returns the JSON state path for an index root.
func StatePath(indexRoot string) string {
	return filepath.Join(paths.RepoIndexDir(indexRoot), "watch.state.json")
}

// Acquire locks the daemon for indexRoot. Returns ErrAlreadyRunning if a
// live daemon is detected.
func Acquire(indexRoot string) (*Lock, error) {
	if err := os.MkdirAll(paths.RepoIndexDir(indexRoot), 0o755); err != nil {
		return nil, err
	}
	lp := LockPath(indexRoot)
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := flock(f); err != nil {
		_ = f.Close()
		// Lock is held by another process — best-effort detect liveness.
		if existing, rerr := readPID(lp); rerr == nil && processAlive(existing) {
			return nil, ErrAlreadyRunning{PID: existing}
		}
		return nil, err
	}
	if err := f.Truncate(0); err != nil {
		_ = unflock(f)
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		_ = unflock(f)
		_ = f.Close()
		return nil, err
	}
	if _, err := f.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		_ = unflock(f)
		_ = f.Close()
		return nil, err
	}
	return &Lock{path: lp, state: StatePath(indexRoot), f: f}, nil
}

// Release removes the lockfile and state file. Safe to call multiple times.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = unflock(l.f)
	_ = l.f.Close()
	l.f = nil
	_ = os.Remove(l.path)
	_ = os.Remove(l.state)
	return nil
}

// WriteState atomically writes a state document next to the lock.
func (l *Lock) WriteState(s State) error {
	if l == nil {
		return errors.New("daemon: lock is nil")
	}
	s.UpdatedAt = time.Now().UTC()
	if s.PID == 0 {
		s.PID = os.Getpid()
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.state + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.state)
}

// ReadState returns the persisted state for an index root if any.
func ReadState(indexRoot string) (*State, error) {
	b, err := os.ReadFile(StatePath(indexRoot))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ErrAlreadyRunning is returned when another daemon already holds the lock.
type ErrAlreadyRunning struct {
	PID int
}

func (e ErrAlreadyRunning) Error() string {
	return fmt.Sprintf("watch daemon already running (pid=%d)", e.PID)
}

func readPID(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ProcessAlive reports whether a process with the given PID is currently running.
// Exported so callers (e.g. freshness) can verify a watch daemon recorded in the
// state file is actually alive rather than trusting a possibly-stale PID.
func ProcessAlive(pid int) bool {
	return processAlive(pid)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}
