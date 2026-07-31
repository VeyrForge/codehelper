package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrite_IncludesRepoAppendWhenPresent(t *testing.T) {
	repo := t.TempDir()
	appendDir := filepath.Join(repo, ".codehelper")
	if err := os.MkdirAll(appendDir, 0o755); err != nil {
		t.Fatalf("mkdir append dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appendDir, "AGENTS.append.md"), []byte("Extra rules"), 0o644); err != nil {
		t.Fatalf("write append file: %v", err)
	}

	if err := Write(repo); err != nil {
		t.Fatalf("write agents: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read agents: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "<repo_append>") || !strings.Contains(content, "Extra rules") {
		t.Fatalf("expected repo append block, got: %q", content)
	}
}

func TestWrite_SkipsEmptyRepoAppend(t *testing.T) {
	repo := t.TempDir()
	appendDir := filepath.Join(repo, ".codehelper")
	if err := os.MkdirAll(appendDir, 0o755); err != nil {
		t.Fatalf("mkdir append dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appendDir, "AGENTS.append.md"), []byte(" \n "), 0o644); err != nil {
		t.Fatalf("write append file: %v", err)
	}

	if err := Write(repo); err != nil {
		t.Fatalf("write agents: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read agents: %v", err)
	}
	if strings.Contains(string(b), "<repo_append>") {
		t.Fatalf("did not expect repo append block for empty content")
	}
}

func TestWrite_CreatesLearningConfigAndPolicyBlock(t *testing.T) {
	repo := t.TempDir()
	if err := Write(repo); err != nil {
		t.Fatalf("write agents: %v", err)
	}

	cfgPath := filepath.Join(repo, ".codehelper", "learning.json")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read learning config: %v", err)
	}
	cfgContent := string(cfg)
	if !strings.Contains(cfgContent, "\"enabled\": true") {
		t.Fatalf("expected learning enabled by default in approval mode, got %q", cfgContent)
	}
	if !strings.Contains(cfgContent, "\"project_scoped_only\": true") {
		t.Fatalf("expected project scoped default, got %q", cfgContent)
	}
	if !strings.Contains(cfgContent, "\"mode\": \"approval\"") {
		t.Fatalf("expected default mode approval, got %q", cfgContent)
	}

	agentsPath := filepath.Join(repo, "AGENTS.md")
	b, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read agents: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "## Local learning loop") {
		t.Fatalf("expected local learning section in AGENTS.md")
	}
	if !strings.Contains(content, "State: enabled") {
		t.Fatalf("expected learning state enabled in AGENTS.md, got %q", content)
	}
	if !strings.Contains(content, "project-only memory") {
		t.Fatalf("expected project-only memory policy in AGENTS.md")
	}
	if !strings.Contains(content, "mode=approval") {
		t.Fatalf("expected approval mode behavior text in AGENTS.md")
	}
}

func TestWrite_NormalizesLearningConfigMode(t *testing.T) {
	repo := t.TempDir()
	cfgDir := filepath.Join(repo, ".codehelper")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir .codehelper: %v", err)
	}
	initial := `{"enabled":true,"mode":"invalid","project_scoped_only":false}`
	if err := os.WriteFile(filepath.Join(cfgDir, "learning.json"), []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial learning config: %v", err)
	}

	if err := Write(repo); err != nil {
		t.Fatalf("write agents: %v", err)
	}

	cfg, err := os.ReadFile(filepath.Join(cfgDir, "learning.json"))
	if err != nil {
		t.Fatalf("read normalized learning config: %v", err)
	}
	content := string(cfg)
	if !strings.Contains(content, "\"mode\": \"approval\"") {
		t.Fatalf("expected invalid mode normalized to approval, got %q", content)
	}
	if !strings.Contains(content, "\"project_scoped_only\": true") {
		t.Fatalf("expected project scope enforced true, got %q", content)
	}
}
