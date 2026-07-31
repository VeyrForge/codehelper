package web

import (
	"runtime"
	"strings"
	"testing"
)

func TestHeadedFromEnv(t *testing.T) {
	t.Setenv("CODEHELPER_BROWSER_HEADED", "")
	if HeadedFromEnv() {
		t.Fatal("empty env should be false")
	}
	for _, v := range []string{"1", "true", "YES", "on"} {
		t.Setenv("CODEHELPER_BROWSER_HEADED", v)
		if !HeadedFromEnv() {
			t.Fatalf("%q should enable headed", v)
		}
	}
	t.Setenv("CODEHELPER_BROWSER_HEADED", "0")
	if HeadedFromEnv() {
		t.Fatal("0 should be false")
	}
}

func TestPauseOnFailFromEnv(t *testing.T) {
	t.Setenv("CODEHELPER_BROWSER_PAUSE_ON_FAIL", "yes")
	if !PauseOnFailFromEnv() {
		t.Fatal("expected true")
	}
	t.Setenv("CODEHELPER_BROWSER_PAUSE_ON_FAIL", "")
	if PauseOnFailFromEnv() {
		t.Fatal("expected false")
	}
}

func TestDisplayAvailableAndHint(t *testing.T) {
	hint := HeadedUnavailableHint()
	if !strings.Contains(hint, "xvfb") || !strings.Contains(hint, "headed=false") {
		t.Fatalf("hint missing recovery options: %s", hint)
	}
	err := ErrHeadedNoDisplay()
	if err == nil || !strings.Contains(err.Error(), "graphical display") {
		t.Fatalf("ErrHeadedNoDisplay: %v", err)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "freebsd" {
		t.Setenv("DISPLAY", "")
		t.Setenv("WAYLAND_DISPLAY", "")
		if DisplayAvailable() {
			t.Fatal("linux without DISPLAY/WAYLAND should be unavailable")
		}
		t.Setenv("DISPLAY", ":99")
		if !DisplayAvailable() {
			t.Fatal("DISPLAY=:99 should be available")
		}
	}
}

func TestNormalizeOutlineRef(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{"e3", "e3"},
		{"E3", "e3"},
		{"3", "e3"},
		{"ref:e2", "e2"},
		{"ref=e1", "e1"},
	}
	for _, c := range cases {
		if got := NormalizeOutlineRef(c.in); got != c.want {
			t.Errorf("NormalizeOutlineRef(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
