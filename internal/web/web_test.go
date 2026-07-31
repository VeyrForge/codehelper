package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","items":[{"id":42}]}`))
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Welcome</h1><p>Dashboard ready</p><script>x()</script></body></html>`))
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/health", http.StatusFound)
	})
	mux.HandleFunc("/spa", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><script src="/bundle.js"></script></head><body><div id="root"></div></body></html>`))
	})
	return httptest.NewServer(mux)
}

type mockRenderer struct{ html string }

func (m mockRenderer) Render(_ context.Context, _ string) (string, error) { return m.html, nil }

func TestNeedsJSRenderSignal(t *testing.T) {
	srv := testServer()
	defer srv.Close()
	// No renderer wired: an SPA shell must be flagged, not silently returned.
	res := Run(context.Background(), Check{URL: srv.URL + "/spa", ExtractText: true})
	if !res.NeedsJSRender {
		t.Errorf("expected needs_js_render for SPA shell, got %+v", res)
	}
	if res.Rendered {
		t.Errorf("rendered should be false with no renderer")
	}
}

func TestRendererEscalation(t *testing.T) {
	srv := testServer()
	defer srv.Close()
	DefaultRenderer = mockRenderer{html: `<html><body><h1>Hydrated</h1><p>Real content here now</p></body></html>`}
	defer func() { DefaultRenderer = nil }()

	res := Run(context.Background(), Check{
		URL: srv.URL + "/spa", ExtractText: true,
		ExpectContains: []string{"Hydrated"},
	})
	if !res.Rendered {
		t.Errorf("expected rendered=true after escalation, got %+v", res)
	}
	if res.NeedsJSRender {
		t.Errorf("needs_js_render should clear once rendered")
	}
	if !res.Passed {
		t.Errorf("expected Hydrated content after render, assertions=%+v", res.Assertions)
	}
}

// A normal HTML page with real text must NOT be flagged as JS-rendered.
func TestNormalPageNotFlagged(t *testing.T) {
	srv := testServer()
	defer srv.Close()
	res := Run(context.Background(), Check{URL: srv.URL + "/page", ExtractText: true})
	if res.NeedsJSRender {
		t.Errorf("normal content page wrongly flagged needs_js_render")
	}
}

func TestStatusAndContains(t *testing.T) {
	srv := testServer()
	defer srv.Close()
	res := Run(context.Background(), Check{
		URL: srv.URL + "/page", ExpectStatus: 200,
		ExpectContains: []string{"Welcome", "Dashboard"},
		ExpectAbsent:   []string{"error"},
		ExtractText:    true,
	})
	if !res.Passed {
		t.Fatalf("expected pass, got %+v", res.Assertions)
	}
	if res.Text == "" || contains(res.Text, "x()") {
		t.Errorf("text extraction wrong (script not stripped?): %q", res.Text)
	}
}

func TestJSONPathAssertion(t *testing.T) {
	srv := testServer()
	defer srv.Close()
	res := Run(context.Background(), Check{
		URL: srv.URL + "/health", ExpectJSONPath: "status", ExpectJSONVal: "ok",
	})
	if !res.Passed {
		t.Fatalf("status path: %+v", res.Assertions)
	}
	res2 := Run(context.Background(), Check{
		URL: srv.URL + "/health", ExpectJSONPath: "items.0.id", ExpectJSONVal: "42",
	})
	if !res2.Passed {
		t.Fatalf("nested array path: %+v", res2.Assertions)
	}
	res3 := Run(context.Background(), Check{
		URL: srv.URL + "/health", ExpectJSONPath: "status", ExpectJSONVal: "down",
	})
	if res3.Passed {
		t.Errorf("should fail on wrong value")
	}
}

func TestFailingAssertionReported(t *testing.T) {
	srv := testServer()
	defer srv.Close()
	res := Run(context.Background(), Check{URL: srv.URL + "/page", ExpectStatus: 404})
	if res.Passed {
		t.Errorf("expected fail on wrong status")
	}
	if len(res.Assertions) != 1 || res.Assertions[0].Pass {
		t.Errorf("assertion detail wrong: %+v", res.Assertions)
	}
}

func TestRedirectControl(t *testing.T) {
	srv := testServer()
	defer srv.Close()
	// Default: do not follow -> 302.
	res := Run(context.Background(), Check{URL: srv.URL + "/redirect", ExpectStatus: 302})
	if !res.Passed {
		t.Errorf("expected 302 without follow, got %d", res.StatusCode)
	}
	// Follow -> 200 from /health.
	res2 := Run(context.Background(), Check{URL: srv.URL + "/redirect", FollowRedirect: true, ExpectStatus: 200})
	if !res2.Passed {
		t.Errorf("expected 200 with follow, got %d", res2.StatusCode)
	}
}

func TestNoAssertionsDefaultsToHTTPOK(t *testing.T) {
	srv := testServer()
	defer srv.Close()
	res := Run(context.Background(), Check{URL: srv.URL + "/health"})
	if !res.Passed {
		t.Errorf("2xx with no assertions should pass")
	}
}

func TestNetworkErrorIsStructured(t *testing.T) {
	res := Run(context.Background(), Check{URL: "http://127.0.0.1:0/nope", TimeoutSec: 1})
	if res.Passed || res.Error == "" {
		t.Errorf("expected structured failure, got %+v", res)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
