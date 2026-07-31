package security_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/security"
)

func TestNextjsLiveBedSecurityProbes(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".eval-projects", "nextjs-app-router-playground"))
	if _, err := os.Stat(filepath.Join(root, "middleware.ts")); err != nil {
		t.Skip(err)
	}
	smells := security.ScanRepoForSecuritySmells(root, security.RepoScanOptions{Limit: 40})
	app := security.AppSecurityGuidance(root, 8)
	t.Logf("smells=%d app=%d", len(smells), len(app))
	var foundSecret, foundXSS, foundMW bool
	for _, s := range smells {
		rel := filepath.ToSlash(s.File)
		t.Logf("smell %s %s:%d", s.Rule, rel, s.Line)
		if s.Rule == "hardcoded-secret" && strings.Contains(rel, "api/probe") {
			foundSecret = true
		}
		if s.Rule == "raw-html-xss" && strings.Contains(rel, "api/probe") {
			foundXSS = true
		}
	}
	for _, f := range app {
		t.Logf("app %s %s:%d kind=%s", f.Rule, f.File, f.Line, f.Kind)
		if f.Rule == "app-auth-middleware" {
			foundMW = true
		}
	}
	if !foundSecret {
		t.Error("expected hardcoded-secret on api/probe")
	}
	if !foundXSS {
		t.Error("expected raw-html-xss on api/probe")
	}
	if !foundMW {
		t.Error("expected app-auth-middleware from middleware.ts")
	}
}
