package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// terminateStaleMCPServers sends SIGTERM to any running `codehelper mcp` or
// `codehelper-mcp` server process that started BEFORE the current binary was
// built. Editors keep a long-lived stdio MCP server alive for the whole session,
// so after `codehelper update` rebuilds the binary the editor would otherwise
// keep serving stale code forever (the classic "I updated but nothing changed" —
// e.g. responses still JSON instead of TOON). Killing the stale server makes the
// editor respawn a fresh one (now the new binary) on its next tool call.
//
// Linux-only (reads /proc); a safe no-op on other platforms. Never kills the
// current process, and never kills a server newer than the binary (so a
// freshly-spawned server is left running).
func terminateStaleMCPServers() int {
	exe, err := os.Executable()
	if err != nil {
		return 0
	}
	st, err := os.Stat(exe)
	if err != nil {
		return 0
	}
	binMtime := st.ModTime()

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0 // not Linux — best-effort, skip
	}
	self := os.Getpid()
	killed := 0
	for _, e := range entries {
		pid, perr := strconv.Atoi(e.Name())
		if perr != nil || pid == self {
			continue
		}
		cmdline, rerr := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if rerr != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if !isCodehelperMCPCmd(args) {
			continue
		}
		// /proc/<pid> directory mtime approximates process start time.
		pst, serr := os.Stat("/proc/" + e.Name())
		if serr != nil || !pst.ModTime().Before(binMtime) {
			continue // newer than (or same age as) the binary → already fresh
		}
		if p, ferr := os.FindProcess(pid); ferr == nil {
			if p.Signal(syscall.SIGTERM) == nil {
				killed++
			}
		}
	}
	return killed
}

// isCodehelperMCPCmd reports whether an argv is a codehelper MCP server:
// `codehelper mcp` (subcommand) or the standalone `codehelper-mcp` binary.
func isCodehelperMCPCmd(args []string) bool {
	if len(args) == 0 {
		return false
	}
	base := filepath.Base(args[0])
	if base == "codehelper-mcp" {
		return true
	}
	return base == "codehelper" && len(args) >= 2 && args[1] == "mcp"
}
