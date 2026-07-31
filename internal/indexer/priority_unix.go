//go:build !windows

package indexer

import (
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// parseThreadNice returns the nice increment applied to tree-sitter parse-worker
// threads. Background indexing is CPU-bound; running the parse threads at a lower
// OS priority lets the editor and interactive MCP tool calls preempt them, so the
// machine stays responsive even while an index converges — and because nice is the
// kernel's cross-process fairness knob, several codehelper daemons all niced this
// way still yield to (normal-priority) interactive work. Default 10; override with
// CODEHELPER_NICE (0 disables, max 19).
func parseThreadNice() int {
	v := strings.TrimSpace(os.Getenv("CODEHELPER_NICE"))
	if v == "" {
		return 10
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 10
	}
	if n > 19 {
		n = 19
	}
	return n
}

// lowerParseThreadPriority pins the calling goroutine to its OS thread and nices
// that thread. On Linux nice values are per-thread, so only this parse worker is
// deprioritized — unlike a whole-process nice, the request-serving threads of an
// MCP server keep normal priority. Best-effort; ignored if the OS refuses.
//
// On success the goroutine stays locked for the rest of the worker's life: Go
// terminates a locked thread when its goroutine exits, so the elevated nice is
// discarded rather than leaking back into the runtime's reusable thread pool.
func lowerParseThreadPriority() {
	nice := parseThreadNice()
	if nice <= 0 {
		return
	}
	runtime.LockOSThread()
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, 0, nice); err != nil {
		// Renice refused (unusual); unlock so we don't needlessly burn a thread.
		runtime.UnlockOSThread()
		slog.Debug("parse worker renice failed", "err", err)
	}
}
