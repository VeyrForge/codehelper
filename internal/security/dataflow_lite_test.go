package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectFailOpenAuth_EmptyTokenSkipsCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.go")
	src := `package api

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				writeJSONError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := ScanFileDataflowLite(path, "internal/api/auth.go", 10)
	found := false
	for _, h := range hits {
		if h.Rule == "authz-fail-open" {
			found = true
			if h.Confidence != "high" {
				t.Fatalf("expected high confidence, got %q", h.Confidence)
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected authz-fail-open, got %+v", hits)
	}
}

func TestDetectFailOpenAuth_FailClosedEmptyReject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.go")
	src := `package api

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token == "" {
			writeJSONError(w, http.StatusUnauthorized, "token is required")
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := ScanFileDataflowLite(path, "internal/api/auth.go", 10)
	for _, h := range hits {
		if h.Rule == "authz-fail-open" {
			t.Fatalf("fail-closed must not flag authz-fail-open: %+v", h)
		}
	}
}

func TestDetectRequestToSinkTaint_SQL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handler.go")
	src := `package app

func GetUser(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	q := "SELECT * FROM users WHERE id = '" + id + "'"
	db.Query(q)
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := ScanFileDataflowLite(path, "app/handler.go", 10)
	found := false
	for _, h := range hits {
		if h.Rule == "injection-taint" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected injection-taint, got %+v", hits)
	}
}

func TestDetectRequestToSinkTaint_ParameterizedOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handler.go")
	src := `package app

func GetUser(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	db.Query("SELECT * FROM users WHERE id = ?", id)
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := ScanFileDataflowLite(path, "app/handler.go", 10)
	for _, h := range hits {
		if h.Rule == "injection-taint" {
			t.Fatalf("parameterized query must not flag injection-taint: %+v", h)
		}
	}
}

func TestLibraryInternalPath_Skipped(t *testing.T) {
	dir := t.TempDir()
	rel := "django/db/models/query.py"
	path := filepath.Join(dir, "query.py")
	src := `
def raw(self, request):
    id = request.GET["id"]
    cursor.execute("SELECT * FROM t WHERE id = '" + id + "'")
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := ScanFileDataflowLite(path, rel, 10)
	if len(hits) != 0 {
		t.Fatalf("library internals must be skipped, got %+v", hits)
	}
}

func TestIsLibraryInternalPath(t *testing.T) {
	if !isLibraryInternalPath("django/db/models/query.py") {
		t.Fatal("django/")
	}
	if !isLibraryInternalPath("packages/compiler-core/src/index.ts") {
		t.Fatal("compiler-core")
	}
	if isLibraryInternalPath("app/api/users/route.ts") {
		t.Fatal("app path must not be library internal")
	}
}

func TestScanRepoMergesDataflowLite(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "app"), 0o755)
	src := `package app

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" {
			got := r.Header.Get("Authorization")
			_ = subtle.ConstantTimeCompare([]byte(got), []byte(s.Token))
		}
		next.ServeHTTP(w, r)
	})
}
`
	if err := os.WriteFile(filepath.Join(dir, "app", "auth.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 20})
	found := false
	for _, h := range hits {
		if h.Rule == "authz-fail-open" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("repo scan should include dataflow-lite fail-open, got rules=%v", rulesOf(hits))
	}
}

func rulesOf(hits []ContextFinding) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Rule)
	}
	return out
}

func TestEnrichKeepsFailOpenHigh(t *testing.T) {
	in := []ContextFinding{{
		Rule: "authz-fail-open", Severity: "high", File: "a.go", Line: 1,
		Evidence: `if s.Token != "" { … } next.ServeHTTP`,
		Kind:     "sink_candidate", Confidence: "high",
	}}
	got := EnrichAndRankFindings(in)
	if got[0].Confidence != "high" || got[0].Kind != "sink_candidate" {
		t.Fatalf("got %+v", got[0])
	}
	if strings.Contains(strings.ToLower(got[0].Hint), "fail-closed") {
		t.Fatalf("must not demote to fail-closed hint: %q", got[0].Hint)
	}
}

func TestDetectFailOpenAuth_JSMissingTokenCallsNext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.ts")
	src := `
export function requireAuth(req, res, next) {
  const token = req.headers.authorization
  if (!token) return next()
  verify(token)
  next()
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := ScanFileDataflowLite(path, "middleware/auth.ts", 10)
	found := false
	for _, h := range hits {
		if h.Rule == "authz-fail-open" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected authz-fail-open for if (!token) return next(), got %+v", hits)
	}
}

func TestDetectRequestToSinkTaint_OpenRedirect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handler.js")
	src := `function go(req, res) {
  const next = req.query.next
  res.redirect(next)
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	hits := ScanFileDataflowLite(path, "app/handler.js", 10)
	found := false
	for _, h := range hits {
		if h.Rule == "open-redirect-taint" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected open-redirect-taint, got %+v", hits)
	}
}
