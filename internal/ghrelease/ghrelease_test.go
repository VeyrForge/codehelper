package ghrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpectedArchiveName(t *testing.T) {
	if got := expectedArchiveName("2.4.1", "windows", "amd64"); got != "codehelper_2.4.1_windows_amd64.zip" {
		t.Fatalf("windows: %q", got)
	}
	if got := expectedArchiveName("2.4.1", "linux", "amd64"); got != "codehelper_2.4.1_linux_amd64.tar.gz" {
		t.Fatalf("linux: %q", got)
	}
}

func TestStripV(t *testing.T) {
	if stripV("v1.2.3") != "1.2.3" {
		t.Fatal()
	}
	if stripV("1.2.3") != "1.2.3" {
		t.Fatal()
	}
}

func TestDefaultRepoPublic(t *testing.T) {
	if DefaultRepo != "VeyrForge/codehelper" {
		t.Fatalf("DefaultRepo=%q", DefaultRepo)
	}
}

func TestOptionsRepoEnvOverride(t *testing.T) {
	t.Setenv(EnvUpgradeRepo, "VeyrForge/codehelper")
	if got := (Options{}).repo(); got != "VeyrForge/codehelper" {
		t.Fatalf("env override: %q", got)
	}
	if got := (Options{GitHubRepo: "VeyrForge/codehelper"}).repo(); got != "VeyrForge/codehelper" {
		t.Fatalf("explicit wins: %q", got)
	}
}

func TestValidSHA256Hex(t *testing.T) {
	ok := strings.Repeat("a", 64)
	if !validSHA256Hex(ok) {
		t.Fatal("expected valid")
	}
	if validSHA256Hex("deadbeef") {
		t.Fatal("too short")
	}
	if validSHA256Hex(strings.Repeat("g", 64)) {
		t.Fatal("non-hex")
	}
}

func TestReadChecksumForMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checksums.txt")
	content := "not-a-hash  codehelper_1.0.0_linux_amd64.tar.gz\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readChecksumFor(path, "codehelper_1.0.0_linux_amd64.tar.gz")
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("want malformed error, got %v", err)
	}
}

func TestReadChecksumForMissingEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checksums.txt")
	hash := strings.Repeat("ab", 32)
	content := hash + "  other_file.tar.gz\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readChecksumFor(path, "codehelper_1.0.0_linux_amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("want empty hash, got %q", got)
	}
}

func TestVerifyChecksumMissingAssetFailClosed(t *testing.T) {
	rel := &releaseJSON{TagName: "v1.0.0"}
	err := verifyChecksum(http.DefaultClient, Options{}, rel, filepath.Join(t.TempDir(), "a.zip"), "a.zip")
	if err == nil || !strings.Contains(err.Error(), "no checksums.txt") {
		t.Fatalf("want fail-closed missing asset, got %v", err)
	}
	if !strings.Contains(err.Error(), "allow-unverified") {
		t.Fatalf("want override hint, got %v", err)
	}
}

func TestVerifyChecksumMissingAssetAllowUnverified(t *testing.T) {
	rel := &releaseJSON{TagName: "v1.0.0"}
	err := verifyChecksum(http.DefaultClient, Options{AllowUnverified: true}, rel, filepath.Join(t.TempDir(), "a.zip"), "a.zip")
	if err != nil {
		t.Fatalf("allow-unverified should skip: %v", err)
	}
}

func TestVerifyChecksumMissingEntryFailClosed(t *testing.T) {
	hash := strings.Repeat("cd", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(hash + "  other.zip\n"))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	archive := filepath.Join(dir, "want.zip")
	if err := os.WriteFile(archive, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel := &releaseJSON{
		TagName: "v1.0.0",
		Assets: []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}
	err := verifyChecksum(srv.Client(), Options{}, rel, archive, "want.zip")
	if err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("want fail-closed missing entry, got %v", err)
	}
}

func TestVerifyChecksumMissingEntryAllowUnverified(t *testing.T) {
	hash := strings.Repeat("cd", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(hash + "  other.zip\n"))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	archive := filepath.Join(dir, "want.zip")
	if err := os.WriteFile(archive, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel := &releaseJSON{
		TagName: "v1.0.0",
		Assets: []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}
	err := verifyChecksum(srv.Client(), Options{AllowUnverified: true}, rel, archive, "want.zip")
	if err != nil {
		t.Fatalf("allow-unverified should skip missing entry: %v", err)
	}
}

func TestVerifyChecksumOK(t *testing.T) {
	payload := []byte("hello-upgrade")
	sum := sha256.Sum256(payload)
	wantHash := hex.EncodeToString(sum[:])
	name := "want.zip"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(wantHash + "  " + name + "\n"))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	archive := filepath.Join(dir, name)
	if err := os.WriteFile(archive, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	rel := &releaseJSON{
		TagName: "v1.0.0",
		Assets: []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}
	if err := verifyChecksum(srv.Client(), Options{}, rel, archive, name); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadFileMaxSize(t *testing.T) {
	old := MaxDownloadBytes
	MaxDownloadBytes = 16
	t.Cleanup(func() { MaxDownloadBytes = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 64)))
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "big.bin")
	err := downloadFile(srv.Client(), Options{}, srv.URL, dest)
	if err == nil || !strings.Contains(err.Error(), "max size") {
		t.Fatalf("want max size error, got %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("oversized download should be removed, stat=%v", statErr)
	}
}
