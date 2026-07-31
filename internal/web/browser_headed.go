package web

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// HeadedFromEnv reports whether CODEHELPER_BROWSER_HEADED requests a visible
// browser (1/true/yes/on). Used when the caller did not pass headed/gui explicitly.
func HeadedFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEHELPER_BROWSER_HEADED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// PauseOnFailFromEnv reports whether CODEHELPER_BROWSER_PAUSE_ON_FAIL is set.
func PauseOnFailFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEHELPER_BROWSER_PAUSE_ON_FAIL"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// DisplayAvailable is a best-effort check that a graphical session exists.
// Linux/Unix require DISPLAY or WAYLAND_DISPLAY; Windows/macOS assume a GUI is
// reachable (launch still fails clearly if Chromium cannot open a window).
func DisplayAvailable() bool {
	switch runtime.GOOS {
	case "windows", "darwin":
		return true
	default:
		return strings.TrimSpace(os.Getenv("DISPLAY")) != "" ||
			strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != ""
	}
}

// HeadedUnavailableHint explains how to recover when headed mode cannot open a
// window (SSH/CI without a display, or Chromium launch failure).
func HeadedUnavailableHint() string {
	return "headed mode needs a graphical display. Options: run on a local desktop, " +
		"use `xvfb-run …` (or set DISPLAY), or disable headed (`headed=false` / `--headed=false` / unset CODEHELPER_BROWSER_HEADED)"
}

// ErrHeadedNoDisplay is returned when Headed is requested but no display is available.
func ErrHeadedNoDisplay() error {
	return fmt.Errorf("cannot launch headed browser: %s", HeadedUnavailableHint())
}
