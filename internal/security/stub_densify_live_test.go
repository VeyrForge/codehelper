package security_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/VeyrForge/codehelper/internal/security"
)

func TestMinimalStubSecurityDensify(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "minimal-testbeds"))
	if _, err := os.Stat(root); err != nil {
		t.Skip(err)
	}
	for _, name := range []string{"nest", "wordpress", "rails", "nextjs", "nuxt"} {
		bed := filepath.Join(root, name)
		hits := security.ScanRepoForSecuritySmells(bed, security.RepoScanOptions{Limit: 20})
		if len(hits) == 0 {
			t.Errorf("%s: expected densified sinks, got 0", name)
			continue
		}
		t.Logf("%s hits=%d top=%s:%d %s", name, len(hits), hits[0].File, hits[0].Line, hits[0].Rule)
	}
}

func TestScanRepoFollowsWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junctions only")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(filepath.Join(target, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "const password = \"SuperSecretFixtureValue99\";\n"
	if err := os.WriteFile(filepath.Join(target, "src", "a.ts"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("mklink failed: %v (%s)", err, out)
	}
	hits := security.ScanRepoForSecuritySmells(link, security.RepoScanOptions{Limit: 10})
	if len(hits) == 0 {
		t.Fatalf("junction scan returned 0 hits — WalkDir missed sources under reparse point")
	}
}

func TestScanRepoDataflowLiteFollowsWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junctions only")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(filepath.Join(target, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(filepath.Join(target, "app", "auth.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("mklink failed: %v (%s)", err, out)
	}
	hits := security.ScanRepoDataflowLite(link, security.RepoScanOptions{Limit: 20})
	found := false
	for _, h := range hits {
		if h.Rule == "authz-fail-open" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("junction dataflow-lite returned no authz-fail-open (hits=%d)", len(hits))
	}
}

func TestAppSecurityGuidanceFollowsWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows junctions only")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	link := filepath.Join(base, "link")
	appJava := filepath.Join(target, "src", "main", "java", "com", "example")
	if err := os.MkdirAll(appJava, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := `package com.example;
import org.springframework.boot.autoconfigure.SpringBootApplication;
@SpringBootApplication
public class DemoApplication {}
`
	ctrl := `package com.example;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;
@RestController
public class OwnerController {
  @GetMapping("/owners")
  public String findOwner() { return "x"; }
}
`
	if err := os.WriteFile(filepath.Join(appJava, "DemoApplication.java"), []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appJava, "OwnerController.java"), []byte(ctrl), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("mklink failed: %v (%s)", err, out)
	}
	hits := security.AppSecurityGuidance(link, 6)
	found := false
	for _, h := range hits {
		if h.Rule == "app-missing-spring-security" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("junction app-security missed missing-spring-security (hits=%v)", hits)
	}
}
