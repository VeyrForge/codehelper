package retrieval

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/green"
)

func TestProbeLocalEmbedServer_FindsHealthyEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	orig := defaultEmbedProbeURLs
	defaultEmbedProbeURLs = []string{srv.URL}
	t.Cleanup(func() { defaultEmbedProbeURLs = orig })

	if got := probeLocalEmbedServer(); got != srv.URL {
		t.Fatalf("probe = %q, want %q", got, srv.URL)
	}
}

func TestProbeLocalEmbedServer_EmptyWhenNoneUp(t *testing.T) {
	orig := defaultEmbedProbeURLs
	defaultEmbedProbeURLs = []string{"http://127.0.0.1:1"}
	t.Cleanup(func() { defaultEmbedProbeURLs = orig })
	if got := probeLocalEmbedServer(); got != "" {
		t.Fatalf("expected empty probe, got %q", got)
	}
}

func TestProbeLocalEmbedServer_SkipsUnhealthy(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(bad.Close)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(good.Close)

	orig := defaultEmbedProbeURLs
	defaultEmbedProbeURLs = []string{bad.URL, good.URL}
	t.Cleanup(func() { defaultEmbedProbeURLs = orig })

	if got := probeLocalEmbedServer(); got != good.URL {
		t.Fatalf("probe = %q, want %q", got, good.URL)
	}
}

func TestEnsureEmbedder_ExplicitURLWins(t *testing.T) {
	t.Cleanup(func() { SetEmbedder(nil) })
	SetEmbedder(nil)
	t.Setenv("CODEHELPER_EMBED_URL", "http://explicit.test:9999")
	t.Setenv("CODEHELPER_EMBED_AUTO", "1")

	orig := defaultEmbedProbeURLs
	defaultEmbedProbeURLs = []string{"http://127.0.0.1:1"}
	t.Cleanup(func() { defaultEmbedProbeURLs = orig })

	EnsureEmbedder()
	if !SemanticEnabled() {
		t.Fatal("expected embedder from explicit URL")
	}
	h, ok := activeEmbedder.(*httpEmbedder)
	if !ok || h.endpoint != "http://explicit.test:9999/v1/embeddings" {
		t.Fatalf("unexpected embedder: %#v", activeEmbedder)
	}
}

func TestEnsureEmbedder_ProbeWhenNoEnv(t *testing.T) {
	t.Cleanup(func() { SetEmbedder(nil) })
	SetEmbedder(nil)
	t.Setenv("CODEHELPER_EMBED_URL", "")
	t.Setenv("CODEHELPER_EMBED_AUTO", "1")
	home := t.TempDir() // no green.json
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	orig := defaultEmbedProbeURLs
	defaultEmbedProbeURLs = []string{srv.URL}
	t.Cleanup(func() { defaultEmbedProbeURLs = orig })

	EnsureEmbedder()
	if !SemanticEnabled() {
		t.Fatal("expected embedder from probe")
	}
	if got := os.Getenv("CODEHELPER_EMBED_URL"); got != srv.URL {
		t.Fatalf("CODEHELPER_EMBED_URL=%q want %q", got, srv.URL)
	}
}

func TestEnsureEmbedder_GreenConfigExportsURL(t *testing.T) {
	t.Cleanup(func() { SetEmbedder(nil) })
	SetEmbedder(nil)
	t.Setenv("CODEHELPER_EMBED_URL", "")
	t.Setenv("CODEHELPER_EMBED_AUTO", "1")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := green.DefaultEmbedOnlyConfig("ge")
	cfg.Servers[0].Port = 18766
	if err := green.Save(cfg); err != nil {
		t.Fatal(err)
	}

	orig := defaultEmbedProbeURLs
	defaultEmbedProbeURLs = []string{"http://127.0.0.1:1"} // probe must not be required
	t.Cleanup(func() { defaultEmbedProbeURLs = orig })

	EnsureEmbedder()
	if !SemanticEnabled() {
		t.Fatal("expected embedder from green.json")
	}
	if got := os.Getenv("CODEHELPER_EMBED_URL"); got != "http://127.0.0.1:18766" {
		t.Fatalf("CODEHELPER_EMBED_URL=%q", got)
	}
	path, _ := green.ConfigPath()
	raw, _ := os.ReadFile(path)
	var round green.Config
	_ = json.Unmarshal(raw, &round)
	if filepath.Base(filepath.Dir(path)) != ".codehelper" {
		t.Fatalf("unexpected config path %q", path)
	}
}

func TestEnsureEmbedder_AutoOffSkipsProbeAndGreen(t *testing.T) {
	t.Cleanup(func() { SetEmbedder(nil) })
	SetEmbedder(nil)
	t.Setenv("CODEHELPER_EMBED_URL", "")
	t.Setenv("CODEHELPER_EMBED_AUTO", "0")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	orig := defaultEmbedProbeURLs
	defaultEmbedProbeURLs = []string{srv.URL}
	t.Cleanup(func() { defaultEmbedProbeURLs = orig })

	EnsureEmbedder()
	if SemanticEnabled() {
		t.Fatal("auto=0 should leave embedder disabled without explicit URL")
	}
}

func TestEmbedAutoDetectEnabled(t *testing.T) {
	t.Setenv("CODEHELPER_EMBED_AUTO", "")
	if !embedAutoDetectEnabled() {
		t.Fatal("default should allow auto")
	}
	t.Setenv("CODEHELPER_EMBED_AUTO", "off")
	if embedAutoDetectEnabled() {
		t.Fatal("off should disable")
	}
}
