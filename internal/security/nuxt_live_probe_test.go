package security_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/security"
)

func TestNuxtLiveBedSecurityProbes(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".eval-projects", "nuxt-app"))
	if _, err := os.Stat(filepath.Join(root, "middleware", "auth.ts")); err != nil {
		t.Skip(err)
	}
	smells := security.ScanRepoForSecuritySmells(root, security.RepoScanOptions{Limit: 40})
	t.Logf("smells=%d", len(smells))
	var foundSecret, foundXSS bool
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
	if !foundSecret {
		t.Error("expected hardcoded-secret on api/probe")
	}
	if !foundXSS {
		t.Error("expected raw-html-xss (v-html=) on api/probe")
	}
}
