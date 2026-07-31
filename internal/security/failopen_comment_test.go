package security

import (
	"os"
	"path/filepath"
	"strings"
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
			// Prefer the empty catch (line 5) over the comment (line 3) so harness
			// cite_comment_only soft FPs do not fire on descrybe-style fail-open.
			if f.Line != 5 {
				t.Fatalf("want empty-catch cite line 5, got %d evidence=%q", f.Line, f.Evidence)
			}
			if strings.Contains(strings.ToLower(f.Evidence), "fail-open") &&
				!strings.Contains(strings.ToLower(f.Evidence), "catch") {
				t.Fatalf("evidence should be the catch line, got %q", f.Evidence)
			}
		}
	}
	if !found {
		t.Fatalf("expected authz-gap from fail-open comment, got %+v", got)
	}
}

func TestFailOpenCommentFinding_NoCatchFallsBackToComment(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lib", "auth.ts")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "// fail-open if rate limiter unavailable\n" +
		"export function check() { return true }\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ScanRepoForSecuritySmells(dir, RepoScanOptions{Limit: 20})
	found := false
	for _, f := range got {
		if f.Rule == "authz-gap" && f.File == "lib/auth.ts" {
			found = true
			if f.Line != 1 {
				t.Fatalf("want comment cite line 1 when no empty catch, got %d", f.Line)
			}
		}
	}
	if !found {
		t.Fatalf("expected authz-gap from fail-open comment alone, got %+v", got)
	}
}

func TestIsEmptyCatchSwallow(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"} catch (_) {}", true},
		{"catch (e) {}", true},
		{"catch { }", true},
		{"catch (e) { console.log(e) }", false},
		{"except Exception: pass", true},
		{"except ValueError: raise", false},
		{"try { foo() }", false},
	}
	for _, c := range cases {
		if got := isEmptyCatchSwallow(c.in); got != c.want {
			t.Fatalf("isEmptyCatchSwallow(%q)=%v want %v", c.in, got, c.want)
		}
	}
}
