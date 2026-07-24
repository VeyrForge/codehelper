package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFailOpenCommentFinding(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lib", "api-auth.ts")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "export async function authenticateRequest() {\n" +
		"  try {\n" +
		"    // Lightweight rate-limit check (fail-open if limiter unavailable)\n" +
		"    await check()\n" +
		"  } catch (_) {}\n" +
		"}\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 20})
	found := false
	for _, f := range got {
		if f.Rule == "authz-gap" && f.File == "lib/api-auth.ts" {
			found = true
			if f.Line != 3 {
				t.Fatalf("want line 3, got %d", f.Line)
			}
		}
	}
	if !found {
		t.Fatalf("expected authz-gap from fail-open comment, got %+v", got)
	}
}
