package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points the global secret store at a throwaway HOME so tests never touch
// the real ~/.codehelper.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestSetGetRoundTrip(t *testing.T) {
	isolate(t)
	repo := "/some/repo"
	if err := Set(repo, "staging_pg", "s3cr3t"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := Get(repo, "staging_pg")
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if got != "s3cr3t" {
		t.Fatalf("round trip = %q", got)
	}
	if !Has(repo, "staging_pg") {
		t.Fatal("Has should be true after Set")
	}
}

func TestScopedPerRepo(t *testing.T) {
	isolate(t)
	if err := Set("/repo/a", "db", "A"); err != nil {
		t.Fatal(err)
	}
	if err := Set("/repo/b", "db", "B"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := Get("/repo/a", "db"); v != "A" {
		t.Fatalf("repo a leaked: %q", v)
	}
	if v, _, _ := Get("/repo/b", "db"); v != "B" {
		t.Fatalf("repo b leaked: %q", v)
	}
}

func TestEmptyPlaintextDeletes(t *testing.T) {
	isolate(t)
	repo := "/r"
	_ = Set(repo, "x", "v")
	if err := Set(repo, "x", ""); err != nil {
		t.Fatalf("Set empty: %v", err)
	}
	if Has(repo, "x") {
		t.Fatal("empty plaintext should delete the secret")
	}
}

func TestDeleteAndNames(t *testing.T) {
	isolate(t)
	repo := "/r"
	_ = Set(repo, "a", "1")
	_ = Set(repo, "b", "2")
	names := Names(repo)
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("Names = %v", names)
	}
	ok, err := Delete(repo, "a")
	if err != nil || !ok {
		t.Fatalf("Delete ok=%v err=%v", ok, err)
	}
	if Has(repo, "a") {
		t.Fatal("secret a should be gone")
	}
}

func TestGetMissing(t *testing.T) {
	isolate(t)
	if _, ok, err := Get("/r", "nope"); ok || err != nil {
		t.Fatalf("missing secret: ok=%v err=%v", ok, err)
	}
}

// TestStorageContract is the security guard: key + store live under HOME (global),
// never in the repo tree, are 0600, and the plaintext never appears on disk.
func TestStorageContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	if err := Set(repo, "db", "topsecret-xyz"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	sp, err := storePath(repo)
	if err != nil {
		t.Fatal(err)
	}
	kp, err := keyPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{sp, kp} {
		if !strings.HasPrefix(p, home) || strings.HasPrefix(p, repo) {
			t.Fatalf("%s must live under HOME and never in the repo", p)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s perms = %o, want 600", p, perm)
		}
	}
	raw, err := os.ReadFile(sp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "topsecret-xyz") {
		t.Fatal("plaintext found in store file — not encrypted")
	}
}

// TestCorruptKeyRejected ensures a wrong-size key is a hard error, not a silent
// regen that would orphan every stored secret.
func TestCorruptKeyRejected(t *testing.T) {
	isolate(t)
	kp, _ := keyPath()
	if err := os.MkdirAll(filepath.Dir(kp), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kp, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateKey(); err == nil {
		t.Fatal("wrong-size key must error")
	}
}
