package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigFromEnv_fileFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEHELPER_LLM_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("CODEHELPER_LLM_MODEL", "")
	t.Setenv("CODEHELPER_LLM_API_KEY", "k")

	p := filepath.Join(dir, ".codehelper", "llm.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"base_url":"http://file","model":"m-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := ConfigFromEnv()
	if cfg.BaseURL != "http://file" || cfg.Model != "m-file" || cfg.APIKey != "k" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestConfigFromEnv_envOverridesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEHELPER_LLM_BASE_URL", "http://env")
	t.Setenv("CODEHELPER_LLM_MODEL", "m-env")
	t.Setenv("CODEHELPER_LLM_API_KEY", "k")

	p := filepath.Join(dir, ".codehelper", "llm.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"base_url":"http://file","model":"m-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := ConfigFromEnv()
	if cfg.BaseURL != "http://env" || cfg.Model != "m-env" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestSaveLoadUserFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	temp := 0.3
	in := UserFile{BaseURL: "http://x", Model: "m", Temperature: &temp}
	if err := SaveUserFile(in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadUserFile()
	if err != nil {
		t.Fatal(err)
	}
	if out.BaseURL != in.BaseURL || out.Model != in.Model || out.Temperature == nil || *out.Temperature != temp {
		t.Fatalf("out = %+v", out)
	}
}
