package version

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// linkVersion is set at link time: -ldflags "-X github.com/VeyrForge/codehelper/internal/version.linkVersion=1.2.3"
var linkVersion string

var (
	once     sync.Once
	resolved string
)

// SetBuildVersion records a version injected by legacy -X main.Version= ldflags.
func SetBuildVersion(v string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	if strings.TrimSpace(linkVersion) == "" {
		linkVersion = v
	}
	once.Do(func() {})
	resolved = v
}

// Current returns the release version (link-time override, else repo VERSION file, else 0.0.0-dev).
func Current() string {
	once.Do(func() {
		if v := strings.TrimSpace(linkVersion); v != "" {
			resolved = v
			return
		}
		if v := findVERSIONFile(); v != "" {
			resolved = v
			return
		}
		resolved = "0.0.0-dev"
	})
	return resolved
}

// ReadFromDir returns the trimmed first line of VERSION under dir, or "" if missing.
func ReadFromDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("empty directory")
	}
	b, err := os.ReadFile(filepath.Join(dir, "VERSION"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return firstLine(string(b)), nil
}

// WriteToDir writes repo-root VERSION (one line, semver text).
func WriteToDir(dir, ver string) error {
	ver = firstLine(ver)
	if ver == "" {
		return fmt.Errorf("version must not be empty")
	}
	if strings.ContainsAny(ver, " \t\r\n/\\") {
		return fmt.Errorf("invalid version %q", ver)
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("empty directory")
	}
	return os.WriteFile(filepath.Join(dir, "VERSION"), []byte(ver+"\n"), 0o644)
}

// LdflagsX returns -ldflags -X fragment for injecting Current from dir's VERSION file.
func LdflagsX(dir string) (string, error) {
	ver, err := ReadFromDir(dir)
	if err != nil {
		return "", err
	}
	if ver == "" {
		ver = Current()
	}
	if ver == "" || ver == "0.0.0-dev" {
		return "", fmt.Errorf("no VERSION file in %s and no embedded version", dir)
	}
	return fmt.Sprintf("-s -w -X github.com/VeyrForge/codehelper/internal/version.linkVersion=%s", ver), nil
}

func findVERSIONFile() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if v, err := ReadFromDir(dir); err == nil && v != "" {
			return v
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
