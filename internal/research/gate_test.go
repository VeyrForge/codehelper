package research

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/patterns"
)

func TestShouldResearch_authTopic(t *testing.T) {
	if !ShouldResearch("add oauth login", nil, patterns.ExpandOutput{}) {
		t.Fatal("expected research for auth topic")
	}
}

func TestNetworkEnabled_missingFile(t *testing.T) {
	if NetworkEnabled("/nonexistent/path") {
		t.Fatal("expected false without learning.json")
	}
}

func TestLearningEnabled_EnabledBool(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".codehelper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "learning.json"), []byte(`{"enabled":true,"mode":"approval"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !LearningEnabled(dir) {
		t.Fatal("expected enabled=true to count as learning enabled")
	}
}

func TestLearningEnabled_StateString(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".codehelper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "learning.json"), []byte(`{"state":"enabled"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !LearningEnabled(dir) {
		t.Fatal("expected state=enabled to count")
	}
}
