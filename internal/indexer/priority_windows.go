//go:build windows

package indexer

// lowerParseThreadPriority is a no-op on Windows (no POSIX per-thread nice).
// Parse concurrency is still bounded by maxParseWorkers; see analyze.go. Kept for
// cross-platform parity with priority_unix.go.
func lowerParseThreadPriority() {}
