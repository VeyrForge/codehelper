package mcpsvc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

const (
	mcpTokenEnv      = "CODEHELPER_MCP_TOKEN"
	mcpTokenFileEnv  = "CODEHELPER_MCP_TOKEN_FILE"
	mcpAllowHostsEnv = "CODEHELPER_MCP_HTTP_ALLOW_HOSTS"
	mcpTokenFileName = "mcp_http.token"
)

// normalizeMCPHTTPAddr rewrites host-less binds (:port) to loopback so
// `codehelper mcp --http :8765` does not listen on all interfaces by accident.
func normalizeMCPHTTPAddr(addr string) (string, bool, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", true, nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Allow bare ":8765" — SplitHostPort accepts it with empty host.
		return "", false, fmt.Errorf("invalid --http address %q: %w", addr, err)
	}
	if host == "" {
		return net.JoinHostPort("127.0.0.1", port), true, nil
	}
	return net.JoinHostPort(host, port), isLoopbackHost(host), nil
}

func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(strings.ToLower(host))
	h = strings.Trim(h, "[]")
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// IsLoopbackHost reports whether host is an explicit loopback name or IP
// (localhost, 127.0.0.1, ::1, or any IP.IsLoopback()).
func IsLoopbackHost(host string) bool {
	return isLoopbackHost(host)
}

// requireMCPHTTPToken returns an error when token is empty.
// MCP Streamable HTTP is fail-closed: a bearer token is required on every bind,
// including loopback (DNS-rebinding / local-browser CSRF).
func requireMCPHTTPToken(token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("MCP HTTP requires a bearer token: set %s, or allow auto-generation to ~/.codehelper/%s", mcpTokenEnv, mcpTokenFileName)
	}
	return nil
}

// resolveMCPHTTPToken returns the bearer token for MCP HTTP.
// Preference: CODEHELPER_MCP_TOKEN env → existing token file → generate + write 0600.
// Token file path: CODEHELPER_MCP_TOKEN_FILE, else ~/.codehelper/mcp_http.token.
func resolveMCPHTTPToken() (token, source string, err error) {
	if t := strings.TrimSpace(os.Getenv(mcpTokenEnv)); t != "" {
		return t, "env:" + mcpTokenEnv, nil
	}
	path, err := mcpHTTPTokenFilePath()
	if err != nil {
		return "", "", err
	}
	if b, readErr := os.ReadFile(path); readErr == nil {
		t := strings.TrimSpace(string(b))
		if t != "" {
			return t, "file:" + path, nil
		}
	}
	t, err := generateMCPHTTPToken()
	if err != nil {
		return "", "", err
	}
	if err := writeMCPHTTPTokenFile(path, t); err != nil {
		return "", "", err
	}
	return t, "generated:" + path, nil
}

func mcpHTTPTokenFilePath() (string, error) {
	if p := strings.TrimSpace(os.Getenv(mcpTokenFileEnv)); p != "" {
		return p, nil
	}
	dir, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, mcpTokenFileName), nil
}

func generateMCPHTTPToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate MCP HTTP token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func writeMCPHTTPTokenFile(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create MCP token dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("write MCP token file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install MCP token file: %w", err)
	}
	return nil
}

func mcpHTTPAllowHosts() []string {
	raw := strings.TrimSpace(os.Getenv(mcpAllowHostsEnv))
	if raw == "" {
		return nil
	}
	var out []string
	for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		h := strings.TrimSpace(strings.ToLower(tok))
		h = strings.Trim(h, "[]")
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

// mcpHostAllowed validates the request Host header against the bind address.
// Loopback binds only accept loopback Host values (DNS-rebinding defense).
// Non-loopback binds accept the bind host. Extra hosts from
// CODEHELPER_MCP_HTTP_ALLOW_HOSTS cover reverse-proxy public names.
func mcpHostAllowed(hostHeader, bindAddr string, loopback bool, extra []string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.TrimSpace(strings.ToLower(strings.Trim(host, "[]")))
	if host == "" {
		return false
	}
	if loopback && isLoopbackHost(host) {
		return true
	}
	if !loopback {
		bindHost, _, err := net.SplitHostPort(bindAddr)
		if err == nil {
			bindHost = strings.TrimSpace(strings.ToLower(strings.Trim(bindHost, "[]")))
			if bindHost != "" && bindHost == host {
				return true
			}
		}
	}
	for _, h := range extra {
		if h == host {
			return true
		}
	}
	return false
}

// mcpOriginAllowed rejects browser Origin headers that are not loopback (or
// explicitly allow-listed hosts). Empty / "null" Origin is allowed — typical
// non-browser MCP clients do not send Origin.
func mcpOriginAllowed(origin string, loopback bool, extra []string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" || strings.EqualFold(origin, "null") {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	h := strings.ToLower(strings.Trim(u.Hostname(), "[]"))
	if loopback && isLoopbackHost(h) {
		return true
	}
	for _, allow := range extra {
		if allow == h {
			return true
		}
	}
	return false
}

// mcpBearerAuth wraps h so requests must present Authorization: Bearer <token>.
// Empty token leaves h unchanged (tests / callers that skip auth must pass "").
func mcpBearerAuth(token string, h http.Handler) http.Handler {
	token = strings.TrimSpace(token)
	if token == "" {
		return h
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		got = strings.TrimSpace(got)
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// mcpHTTPGuard enforces Host + Origin checks, then bearer auth.
func mcpHTTPGuard(bindAddr string, loopback bool, token string, h http.Handler) http.Handler {
	extra := mcpHTTPAllowHosts()
	inner := mcpBearerAuth(token, h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mcpHostAllowed(r.Host, bindAddr, loopback, extra) {
			http.Error(w, "invalid Host header", http.StatusForbidden)
			return
		}
		if !mcpOriginAllowed(r.Header.Get("Origin"), loopback, extra) {
			http.Error(w, "browser Origin not allowed", http.StatusForbidden)
			return
		}
		inner.ServeHTTP(w, r)
	})
}
