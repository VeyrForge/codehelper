//go:build windows

package daemon

import "syscall"

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259 // GetExitCodeProcess: process still running
)

// processAlive reports whether pid is a live process. Signal(0) is unsupported
// on Windows (os.Process.Signal always errors), so we OpenProcess + check the
// exit code instead — required for watch-daemon freshness on this OS.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
