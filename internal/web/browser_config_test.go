package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserConfigDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEHELPER_BROWSER_ACTION_PREVIEWS", "")

	cfg, err := LoadBrowserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActionPreviews {
		t.Fatal("action_previews should default false")
	}
	if PreviewActionsAllowed(true) {
		t.Fatal("preview should be blocked when config disabled")
	}
	if PreviewActionsAllowed(false) {
		t.Fatal("preview should be blocked when not requested")
	}
}

func TestBrowserConfigSaveAndEffective(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEHELPER_BROWSER_ACTION_PREVIEWS", "")

	if err := SaveBrowserConfig(BrowserConfig{ActionPreviews: true}); err != nil {
		t.Fatal(err)
	}
	path, _ := BrowserConfigPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !EffectiveBrowserConfig().ActionPreviews {
		t.Fatal("expected action_previews true from file")
	}
	if !PreviewActionsAllowed(true) {
		t.Fatal("preview should be allowed when config on and requested")
	}
}

func TestBrowserConfigEnvOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEHELPER_BROWSER_ACTION_PREVIEWS", "1")

	if err := SaveBrowserConfig(BrowserConfig{}); err != nil {
		t.Fatal(err)
	}
	if !EffectiveBrowserConfig().ActionPreviews {
		t.Fatal("env should enable action_previews")
	}

	// Ensure path is under temp home, not real ~/.codehelper.
	path, err := BrowserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(home, ".codehelper")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("config path dir = %q want %q", filepath.Dir(path), wantDir)
	}
}
