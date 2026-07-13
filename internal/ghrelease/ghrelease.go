// Package ghrelease downloads official GitHub release archives for codehelper (see .goreleaser.yaml).
package ghrelease

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultRepo is the default GitHub owner/repo for public releases.
const DefaultRepo = "VeyrForge/codehelper"

// Options configures a release install.
type Options struct {
	GitHubRepo     string // "owner/repo", default DefaultRepo
	Tag            string // "latest" or "v1.2.3"
	CurrentVersion string // embedded main.Version; upgrade skipped if equal (unless Force)
	Force          bool
	GitHubToken    string // optional: GITHUB_TOKEN for higher API limits / private forks
	UserAgent      string
}

type releaseJSON struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (o Options) repo() string {
	s := strings.TrimSpace(o.GitHubRepo)
	if s == "" {
		return DefaultRepo
	}
	return s
}

func (o Options) userAgent() string {
	if strings.TrimSpace(o.UserAgent) != "" {
		return o.UserAgent
	}
	return "codehelper-upgrade/1.0"
}

func stripV(tag string) string {
	return strings.TrimPrefix(strings.TrimSpace(tag), "v")
}

func releaseAPIURL(ownerRepo, tag string) string {
	if tag == "" || strings.EqualFold(tag, "latest") {
		return fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", ownerRepo)
	}
	t := strings.TrimSpace(tag)
	if !strings.HasPrefix(t, "v") {
		t = "v" + t
	}
	return fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", ownerRepo, t)
}

func expectedArchiveName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("codehelper_%s_%s_%s.%s", version, goos, goarch, ext)
}

// Upgrade downloads the matching release archive and installs it via replace(tmpExe, destExe).
func Upgrade(destExe string, replace func(tmpExe, destExe string) error, opts Options) error {
	client := &http.Client{Timeout: 90 * time.Minute}

	relURL := releaseAPIURL(opts.repo(), opts.Tag)
	req, err := http.NewRequest(http.MethodGet, relURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", opts.userAgent())
	if t := strings.TrimSpace(opts.GitHubToken); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch release %s: %w", relURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("GitHub API %s: %s — %s", resp.Status, relURL, strings.TrimSpace(string(b)))
	}
	var rel releaseJSON
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return fmt.Errorf("parse release JSON: %w", err)
	}
	ver := stripV(rel.TagName)
	if ver == "" {
		return errors.New("release has empty tag_name")
	}
	if strings.TrimSpace(opts.CurrentVersion) != "" && !opts.Force {
		if stripV(opts.CurrentVersion) == ver {
			fmt.Printf("codehelper %s is already the latest release (%s). Use --force to reinstall.\n", opts.CurrentVersion, rel.TagName)
			return nil
		}
	}

	goos := runtime.GOOS
	goarch := runtime.GOARCH
	wantName := expectedArchiveName(ver, goos, goarch)
	var archiveURL string
	for _, a := range rel.Assets {
		if a.Name == wantName {
			archiveURL = a.BrowserDownloadURL
			break
		}
	}
	if archiveURL == "" {
		return fmt.Errorf("release %s has no asset %q (this build may not publish %s/%s)", rel.TagName, wantName, goos, goarch)
	}

	tmpRoot, err := os.MkdirTemp("", "codehelper-upgrade-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpRoot) }()

	archivePath := filepath.Join(tmpRoot, wantName)
	if err := downloadFile(client, opts, archiveURL, archivePath); err != nil {
		return fmt.Errorf("download %s: %w", wantName, err)
	}

	if err := verifyChecksum(client, opts, &rel, archivePath, wantName); err != nil {
		return err
	}

	extractDir := filepath.Join(tmpRoot, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return err
	}
	if strings.HasSuffix(wantName, ".zip") {
		if err := unzipArchive(archivePath, extractDir); err != nil {
			return err
		}
	} else {
		if err := untarGz(archivePath, extractDir); err != nil {
			return err
		}
	}

	binPath, err := findBinary(extractDir)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(binPath, 0o755); err != nil {
			return err
		}
	}

	tmpExe := filepath.Join(tmpRoot, filepath.Base(binPath))
	if err := copyFile(binPath, tmpExe); err != nil {
		return err
	}

	if err := replace(tmpExe, destExe); err != nil {
		return err
	}
	fmt.Printf("upgrade complete: %s (%s)\n", rel.TagName, wantName)
	return nil
}

func downloadFile(client *http.Client, opts Options, url, destPath string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", opts.userAgent())
	if t := strings.TrimSpace(opts.GitHubToken); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	// GitHub redirects to S3; follow redirects.
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}
	return nil
}

func verifyChecksum(client *http.Client, opts Options, rel *releaseJSON, archivePath, wantName string) error {
	var sumURL string
	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" {
			sumURL = a.BrowserDownloadURL
			break
		}
	}
	if sumURL == "" {
		return nil
	}
	sumPath := archivePath + ".checksums.txt"
	if err := downloadFile(client, opts, sumURL, sumPath); err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}
	wantHash, err := readChecksumFor(sumPath, wantName)
	_ = os.Remove(sumPath)
	if err != nil {
		return err
	}
	if wantHash == "" {
		return nil
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantHash) {
		return fmt.Errorf("sha256 mismatch for %s (got %s want %s)", wantName, got, wantHash)
	}
	return nil
}

func readChecksumFor(checksumFile, filename string) (string, error) {
	f, err := os.Open(checksumFile)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	wantBase := filepath.Base(filename)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		hash := strings.TrimPrefix(strings.ToLower(parts[0]), "sha256:")
		base := filepath.Base(parts[len(parts)-1])
		if strings.EqualFold(base, wantBase) {
			return hash, nil
		}
	}
	return "", sc.Err()
}

func unzipArchive(src, destDir string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(f.Name)
		want := "codehelper.exe"
		if !strings.EqualFold(base, want) {
			continue
		}
		return extractZipFile(f, filepath.Join(destDir, base))
	}
	return errors.New("codehelper.exe not found in zip")
}

func extractZipFile(f *zip.File, dest string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

func untarGz(src, destDir string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	want := "codehelper"
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(hdr.Name)
		if base != want {
			continue
		}
		dest := filepath.Join(destDir, want)
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return errors.New("codehelper binary not found in tar.gz")
}

func findBinary(dir string) (string, error) {
	var hit string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if runtime.GOOS == "windows" {
			if strings.EqualFold(name, "codehelper.exe") {
				hit = path
				return filepath.SkipAll
			}
			return nil
		}
		if name == "codehelper" {
			hit = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if hit == "" {
		return "", errors.New("extracted archive did not contain codehelper binary")
	}
	return hit, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
