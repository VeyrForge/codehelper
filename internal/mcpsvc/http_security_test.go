package mcpsvc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeMCPHTTPAddr(t *testing.T) {
	got, loop, err := normalizeMCPHTTPAddr(":8765")
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:8765" || !loop {
		t.Fatalf("host-less bind: got %q loop=%v", got, loop)
	}
	got, loop, err = normalizeMCPHTTPAddr("127.0.0.1:9000")
	if err != nil || got != "127.0.0.1:9000" || !loop {
		t.Fatalf("loopback: got %q loop=%v err=%v", got, loop, err)
	}
	got, loop, err = normalizeMCPHTTPAddr("0.0.0.0:9000")
	if err != nil || loop {
		t.Fatalf("all-interfaces should be non-loopback: got %q loop=%v err=%v", got, loop, err)
	}
	got, loop, err = normalizeMCPHTTPAddr("[::1]:9000")
	if err != nil || got != "[::1]:9000" || !loop {
		t.Fatalf("IPv6 loopback: got %q loop=%v err=%v", got, loop, err)
	}
	got, loop, err = normalizeMCPHTTPAddr("localhost:9000")
	if err != nil || got != "localhost:9000" || !loop {
		t.Fatalf("localhost host: got %q loop=%v err=%v", got, loop, err)
	}
}

func TestRequireMCPHTTPToken(t *testing.T) {
	if err := requireMCPHTTPToken(""); err == nil {
		t.Fatal("empty token must fail (including loopback)")
	}
	if err := requireMCPHTTPToken("sekret"); err != nil {
		t.Fatalf("non-empty token should be ok: %v", err)
	}
}

func TestResolveMCPHTTPToken_EnvWins(t *testing.T) {
	t.Setenv(mcpTokenEnv, "from-env")
	t.Setenv(mcpTokenFileEnv, filepath.Join(t.TempDir(), "unused.token"))
	tok, src, err := resolveMCPHTTPToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "from-env" {
		t.Fatalf("token=%q", tok)
	}
	if !strings.Contains(src, "env:") {
		t.Fatalf("source=%q", src)
	}
}

func TestResolveMCPHTTPToken_AutoGenerate(t *testing.T) {
	t.Setenv(mcpTokenEnv, "")
	path := filepath.Join(t.TempDir(), "mcp_http.token")
	t.Setenv(mcpTokenFileEnv, path)
	tok1, src1, err := resolveMCPHTTPToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok1 == "" || !strings.HasPrefix(src1, "generated:") {
		t.Fatalf("tok=%q src=%q", tok1, src1)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		// Windows may not preserve Unix mode bits; still require the file exists.
		if perm != 0o600 && perm != 0o666 && perm != 0o644 {
			t.Fatalf("token file perm=%o want 0600 (or platform default)", perm)
		}
	}
	tok2, src2, err := resolveMCPHTTPToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok2 != tok1 {
		t.Fatalf("re-read mismatch: %q vs %q", tok2, tok1)
	}
	if !strings.HasPrefix(src2, "file:") {
		t.Fatalf("second source=%q", src2)
	}
}

func TestMCPBearerAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := mcpBearerAuth("sekret", ok)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d", rr.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req2.Header.Set("Authorization", "Bearer sekret")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusNoContent {
		t.Fatalf("valid token status=%d", rr2.Code)
	}

	passthrough := mcpBearerAuth("", ok)
	rr3 := httptest.NewRecorder()
	passthrough.ServeHTTP(rr3, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rr3.Code != http.StatusNoContent {
		t.Fatalf("empty token should not auth: status=%d", rr3.Code)
	}
}

func TestMCPHTTPGuard_HostAndOrigin(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := mcpHTTPGuard("127.0.0.1:8765", true, "sekret", ok)

	// Bad Host (DNS rebinding)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Host = "evil.example:8765"
	req.Header.Set("Authorization", "Bearer sekret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("evil Host status=%d", rr.Code)
	}

	// Browser Origin from non-loopback
	req2 := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req2.Host = "127.0.0.1:8765"
	req2.Header.Set("Origin", "https://evil.example")
	req2.Header.Set("Authorization", "Bearer sekret")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusForbidden {
		t.Fatalf("evil Origin status=%d", rr2.Code)
	}

	// Loopback Host + token, no Origin
	req3 := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req3.Host = "127.0.0.1:8765"
	req3.Header.Set("Authorization", "Bearer sekret")
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusNoContent {
		t.Fatalf("good request status=%d", rr3.Code)
	}

	// Loopback Origin allowed
	req4 := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req4.Host = "localhost:8765"
	req4.Header.Set("Origin", "http://127.0.0.1:3000")
	req4.Header.Set("Authorization", "Bearer sekret")
	rr4 := httptest.NewRecorder()
	h.ServeHTTP(rr4, req4)
	if rr4.Code != http.StatusNoContent {
		t.Fatalf("loopback Origin status=%d", rr4.Code)
	}
}

func TestMCPHostAllowed_ExtraHosts(t *testing.T) {
	extra := []string{"mcp.example.com"}
	if !mcpHostAllowed("mcp.example.com", "0.0.0.0:8765", false, extra) {
		t.Fatal("allow-listed Host should pass")
	}
	if mcpHostAllowed("other.example.com", "0.0.0.0:8765", false, extra) {
		t.Fatal("unknown Host must fail")
	}
}

func TestMCPOriginAllowed(t *testing.T) {
	if !mcpOriginAllowed("", true, nil) {
		t.Fatal("empty Origin ok")
	}
	if !mcpOriginAllowed("null", true, nil) {
		t.Fatal("null Origin ok")
	}
	if !mcpOriginAllowed("http://localhost:5173", true, nil) {
		t.Fatal("loopback Origin ok on loopback bind")
	}
	if mcpOriginAllowed("https://evil.example", true, nil) {
		t.Fatal("remote Origin must fail")
	}
	if !mcpOriginAllowed("https://mcp.example.com", false, []string{"mcp.example.com"}) {
		t.Fatal("allow-listed Origin should pass")
	}
}
