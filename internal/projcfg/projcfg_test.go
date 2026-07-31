package projcfg

import (
	"os"
	"path/filepath"
	"testing"
)

// withIndexHome points RepoIndexDir at a temp dir via CODEHELPER_INDEX_HOME so
// the config file lands outside any real repo.
func withIndexHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CODEHELPER_INDEX_HOME", dir)
	return dir
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	withIndexHome(t)
	cfg, err := Load("/some/repo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.ToolsEnabled || cfg.Track != TrackSummary {
		t.Fatalf("missing file should be Default(), got %+v", cfg)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withIndexHome(t)
	want := Config{ToolsEnabled: false, Track: TrackSummary}
	if err := Save("/repo/x", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load("/repo/x")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("round trip: got %+v want %+v", got, want)
	}
}

// A partial file must merge over Default(), not zero the omitted field — a file
// that only turns tools off must keep telemetry at the summary default.
func TestPartialFileMergesOverDefault(t *testing.T) {
	withIndexHome(t)
	p := Path("/repo/y")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"tools_enabled":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("/repo/y")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ToolsEnabled {
		t.Fatal("tools_enabled should be false from file")
	}
	if cfg.Track != TrackSummary {
		t.Fatalf("omitted track should default to summary, got %q", cfg.Track)
	}
}

func TestUnknownTrackNormalizesToSummary(t *testing.T) {
	withIndexHome(t)
	p := Path("/repo/z")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"track":"bogus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("/repo/z")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Track != TrackSummary {
		t.Fatalf("unknown track should normalize to summary, got %q", cfg.Track)
	}
}

func TestRecording(t *testing.T) {
	if (Config{Track: TrackOff}).Recording() {
		t.Fatal("TrackOff should not record")
	}
	for _, lvl := range []string{TrackSummary} {
		if !(Config{Track: lvl}).Recording() {
			t.Fatalf("%q should record", lvl)
		}
	}
}
