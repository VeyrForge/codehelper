package green

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // redirect ~/.codehelper away from the real one

	in := Config{
		Enabled: true,
		Servers: []Server{
			{Name: "embed", Cmd: "/bin/python", Args: []string{"s.py", "--port", "{{port}}"}, Port: 8780, URLEnv: "CODEHELPER_EMBED_URL"},
			{Name: "llm", Cmd: "python3", Args: []string{"-m", "x", "--port", "{{port}}"}, Port: 8781, URLEnv: "CODEHELPER_ENRICH_URL", Env: map[string]string{"CODEHELPER_ENRICH_MODEL": "m"}},
		},
	}
	if err := Save(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if !got.Enabled || len(got.Servers) != 2 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.Servers[1].Env["CODEHELPER_ENRICH_MODEL"] != "m" {
		t.Fatalf("env lost: %+v", got.Servers[1])
	}
}

func TestLoadAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, ok, err := Load()
	if ok || err != nil {
		t.Fatalf("absent config: ok=%v err=%v", ok, err)
	}
}

func TestBaseURLAndRenderedArgs(t *testing.T) {
	s := Server{Port: 8780, Args: []string{"--host", "127.0.0.1", "--port", "{{port}}"}}
	if s.BaseURL() != "http://127.0.0.1:8780" {
		t.Fatalf("base url = %q", s.BaseURL())
	}
	got := s.renderedArgs()
	if got[3] != "8780" {
		t.Fatalf("port not substituted: %v", got)
	}
	// original args untouched (no aliasing)
	if s.Args[3] != "{{port}}" {
		t.Fatalf("source args mutated: %v", s.Args)
	}
}

func TestExportEnv(t *testing.T) {
	t.Setenv("CODEHELPER_EMBED_URL", "") // start empty so ExportEnv may set it
	cfg := Config{Enabled: true, Servers: []Server{{Port: 8780, URLEnv: "CODEHELPER_EMBED_URL"}}}
	set := ExportEnv(cfg)
	if len(set) != 1 || os.Getenv("CODEHELPER_EMBED_URL") != "http://127.0.0.1:8780" {
		t.Fatalf("export failed: set=%v val=%q", set, os.Getenv("CODEHELPER_EMBED_URL"))
	}
}

func TestExportEnvDisabledNoop(t *testing.T) {
	t.Setenv("CODEHELPER_EMBED_URL", "")
	cfg := Config{Enabled: false, Servers: []Server{{Port: 8780, URLEnv: "CODEHELPER_EMBED_URL"}}}
	if set := ExportEnv(cfg); set != nil {
		t.Fatalf("disabled should set nothing, got %v", set)
	}
	if os.Getenv("CODEHELPER_EMBED_URL") != "" {
		t.Fatalf("disabled set the var: %q", os.Getenv("CODEHELPER_EMBED_URL"))
	}
}

func TestStopAllSkipsExternal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Pretend an external server left a pidfile around; StopAll must NOT touch it
	// (codehelper never stops what it didn't start, e.g. a systemd-managed server).
	pp, err := pidPath("llm")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pp, []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	StopAll(Config{Enabled: true, Servers: []Server{{Name: "llm", External: true, Port: 8767}}})
	if _, err := os.Stat(pp); err != nil {
		t.Fatalf("StopAll removed the external server's pidfile: %v", err)
	}
}

func TestExternalEnsureDoesNotSpawn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// External + unreachable → an informative, external-specific error and no spawn.
	err := EnsureServer(context.Background(), Server{Name: "llm", External: true, Port: 59999}, nil)
	if err == nil || !strings.Contains(err.Error(), "external server") {
		t.Fatalf("want external-not-reachable error, got %v", err)
	}
}

func TestExportEnvDoesNotOverride(t *testing.T) {
	t.Setenv("CODEHELPER_EMBED_URL", "http://manual:9999") // user set it by hand
	cfg := Config{Enabled: true, Servers: []Server{{Port: 8780, URLEnv: "CODEHELPER_EMBED_URL"}}}
	ExportEnv(cfg)
	if os.Getenv("CODEHELPER_EMBED_URL") != "http://manual:9999" {
		t.Fatalf("override clobbered user value: %q", os.Getenv("CODEHELPER_EMBED_URL"))
	}
}
