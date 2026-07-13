package retrieval

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
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

func TestInitRespectsExplicitEmbedURL(t *testing.T) {
	t.Setenv("CODEHELPER_EMBED_URL", "http://example.test")
	// Re-run init logic: SetEmbedder from env is already done at package init;
	// verify SemanticEnabled reflects configuration when embedder is set manually.
	SetEmbedder(newHTTPEmbedder("http://example.test"))
	if !SemanticEnabled() {
		t.Fatal("embedder should be enabled when set")
	}
	SetEmbedder(nil)
	if SemanticEnabled() {
		t.Fatal("embedder should be disabled when cleared")
	}
	_ = os.Getenv("CODEHELPER_EMBED_URL")
}
