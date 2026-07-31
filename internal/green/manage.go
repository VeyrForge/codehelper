package green

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
)

var spawnMu sync.Mutex

func stateDir() (string, error) {
	dir, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return dir, nil
}

func pidPath(name string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "green-"+name+".pid"), nil
}

func logPath(name string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", "green-"+name+".log"), nil
}

var healthClient = &http.Client{Timeout: 2 * time.Second}

// Healthy reports whether the server answers its readiness probe with HTTP 200.
func Healthy(ctx context.Context, s Server) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.healthURL(), nil)
	if err != nil {
		return false
	}
	resp, err := healthClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// EnsureServer returns nil once the server is healthy: a no-op if it already is,
// otherwise it spawns the process (detached, so it outlives codehelper) and waits
// up to StartTimeout for readiness. Logf may be nil.
func EnsureServer(ctx context.Context, s Server, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if s.External {
		// Supervised elsewhere (e.g. systemd). codehelper never spawns it — it only
		// reports whether the externally-managed server is reachable.
		if Healthy(ctx, s) {
			return nil
		}
		return fmt.Errorf("green %s: external server on :%d is not reachable (started elsewhere, e.g. systemd)", s.Name, s.Port)
	}
	if Healthy(ctx, s) {
		return nil
	}

	spawnMu.Lock()
	// Re-check under lock: a concurrent EnsureServer may have just started it.
	if Healthy(ctx, s) {
		spawnMu.Unlock()
		return nil
	}
	err := spawn(s)
	spawnMu.Unlock()
	if err != nil {
		return fmt.Errorf("green %s: spawn: %w", s.Name, err)
	}

	timeout := s.StartTimeout
	if timeout <= 0 {
		timeout = 120
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	logf("green %s: starting on :%d", s.Name, s.Port)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(750 * time.Millisecond):
		}
		if Healthy(ctx, s) {
			logf("green %s: ready on :%d", s.Name, s.Port)
			return nil
		}
	}
	return fmt.Errorf("green %s: not ready on :%d within %ds (see %s)", s.Name, s.Port, timeout, mustLogPath(s.Name))
}

// spawn launches the server detached (new session) with output appended to its
// log, and records the PID. The process intentionally outlives codehelper.
func spawn(s Server) error {
	if strings.TrimSpace(s.Cmd) == "" {
		return fmt.Errorf("no command configured")
	}
	lp, _ := logPath(s.Name)
	if lp != "" {
		_ = os.MkdirAll(filepath.Dir(lp), 0o755)
	}
	var out *os.File
	if lp != "" {
		out, _ = os.OpenFile(lp, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	}

	cmd := exec.Command(s.Cmd, s.renderedArgs()...)
	cmd.SysProcAttr = detachAttr() // detach from codehelper's session (platform-specific)
	if out != nil {
		cmd.Stdout = out
		cmd.Stderr = out
	}
	if err := cmd.Start(); err != nil {
		if out != nil {
			out.Close()
		}
		return err
	}
	// Reap asynchronously so we don't leave a zombie if it exits, without blocking.
	go func() {
		_ = cmd.Wait()
		if out != nil {
			out.Close()
		}
	}()

	if pp, err := pidPath(s.Name); err == nil {
		_ = os.WriteFile(pp, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	}
	return nil
}

func mustLogPath(name string) string {
	lp, _ := logPath(name)
	return lp
}

// Ensure brings every enabled server up (best-effort: a failure on one is logged
// and the others still proceed, so the engine degrades partially rather than all
// or nothing). Returns the first error encountered, if any.
func Ensure(ctx context.Context, c Config, logf func(string, ...any)) error {
	if !c.Enabled {
		return nil
	}
	var firstErr error
	for _, s := range c.Servers {
		if err := EnsureServer(ctx, s, logf); err != nil {
			if logf != nil {
				logf("%v", err)
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Watch keeps the engine alive: it ensures the servers immediately, then re-checks
// every interval and respawns any that died — this is the "always works while mcp
// works" supervisor. It returns when ctx is cancelled (MCP shutdown). Run it in a
// goroutine.
func Watch(ctx context.Context, c Config, interval time.Duration, logf func(string, ...any)) {
	if !c.Enabled {
		return
	}
	if interval <= 0 {
		interval = 20 * time.Second
	}
	_ = Ensure(ctx, c, logf)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = Ensure(ctx, c, logf)
		}
	}
}

// Stop terminates a managed server via its recorded PID (whole process group, so
// child workers die too). Missing pidfile or already-dead process is not an error.
func Stop(name string) error {
	pp, err := pidPath(name)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(pp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err == nil && pid > 0 {
		terminatePID(pid) // whole process group on unix; the process on windows
	}
	_ = os.Remove(pp)
	return nil
}

// StopAll stops every codehelper-managed server in the config. External servers
// (systemd etc.) are left untouched — codehelper never stops what it did not start.
func StopAll(c Config) {
	for _, s := range c.Servers {
		if s.External {
			continue
		}
		_ = Stop(s.Name)
	}
}

// ServerStatus is a point-in-time view for the CLI.
type ServerStatus struct {
	Name     string
	Port     int
	URLEnv   string
	Healthy  bool
	External bool
	PID      int
}

// Status probes each server's health and PID for `codehelper green status`.
func Status(ctx context.Context, c Config) []ServerStatus {
	out := make([]ServerStatus, 0, len(c.Servers))
	for _, s := range c.Servers {
		st := ServerStatus{Name: s.Name, Port: s.Port, URLEnv: s.URLEnv, External: s.External, Healthy: Healthy(ctx, s)}
		if pp, err := pidPath(s.Name); err == nil {
			if b, err := os.ReadFile(pp); err == nil {
				st.PID, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			}
		}
		out = append(out, st)
	}
	return out
}
