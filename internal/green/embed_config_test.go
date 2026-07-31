package green

import "testing"

func TestDefaultEmbedOnlyConfig_NoLLM(t *testing.T) {
	cfg := DefaultEmbedOnlyConfig("ge")
	if !cfg.Enabled {
		t.Fatal("expected enabled")
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("servers=%d want 1 (embed only)", len(cfg.Servers))
	}
	s := cfg.Servers[0]
	if s.Name != "embed" || s.Port != 8766 || s.URLEnv != "CODEHELPER_EMBED_URL" {
		t.Fatalf("unexpected embed server: %+v", s)
	}
	hasMCP := false
	for _, a := range s.Args {
		if a == "--mcp" {
			hasMCP = true
		}
	}
	if !hasMCP {
		t.Fatal("embed args should include --mcp")
	}
}

func TestDefaultFullConfig_IncludesEnrich(t *testing.T) {
	cfg := DefaultFullConfig("")
	if len(cfg.Servers) != 2 {
		t.Fatalf("servers=%d want 2", len(cfg.Servers))
	}
	if cfg.Servers[1].URLEnv != "CODEHELPER_ENRICH_URL" {
		t.Fatalf("second server url_env=%q", cfg.Servers[1].URLEnv)
	}
}
