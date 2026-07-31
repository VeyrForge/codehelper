package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWalkSourceFiles_IncludesDevOpsBasenames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Dockerfile", "Makefile", "docker-compose.yml", "skip.yml", "main.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	k8sDir := filepath.Join(dir, "k8s")
	if err := os.MkdirAll(k8sDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(k8sDir, "deployment.yaml"), []byte("kind: Deployment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pbDir := filepath.Join(dir, "playbooks")
	if err := os.MkdirAll(pbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pbDir, "site.yml"), []byte("- hosts: all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy.ps1"), []byte("function X {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := WalkSourceFiles(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[filepath.ToSlash(f)] = true
	}
	for _, want := range []string{"Dockerfile", "Makefile", "docker-compose.yml", "main.go", "k8s/deployment.yaml", "playbooks/site.yml", "deploy.ps1"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, files)
		}
	}
	if got["skip.yml"] {
		t.Errorf("plain .yml should not be indexed: %v", files)
	}
}
