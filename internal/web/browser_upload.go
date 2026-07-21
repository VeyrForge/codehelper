package web

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Upload path separators for multi-file attach. Commas are NOT used (paths may
// contain them); agents should pass newline- or "||"-separated absolute paths.
const (
	uploadSepPipe = "||"
)

// SplitUploadPaths parses Action.Text for upload into one or more filesystem paths.
func SplitUploadPaths(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var parts []string
	if strings.Contains(raw, uploadSepPipe) {
		parts = strings.Split(raw, uploadSepPipe)
	} else if strings.Contains(raw, "\n") {
		parts = strings.Split(raw, "\n")
	} else {
		parts = []string{raw}
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// UploadAllowRoots returns the directories upload paths must live under: workspace
// root, explicit allow dirs, and CODEHELPER_BROWSER_UPLOAD_ALLOW (os.PathListSeparator).
func UploadAllowRoots(workspace string, allowDirs []string) []string {
	seen := map[string]struct{}{}
	var roots []string
	add := func(d string) {
		d = strings.TrimSpace(d)
		if d == "" {
			return
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			return
		}
		abs = filepath.Clean(abs)
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		roots = append(roots, abs)
	}
	add(workspace)
	for _, d := range allowDirs {
		add(d)
	}
	if env := strings.TrimSpace(os.Getenv("CODEHELPER_BROWSER_UPLOAD_ALLOW")); env != "" {
		for _, d := range filepath.SplitList(env) {
			add(d)
		}
	}
	return roots
}

// ResolveUploadPaths validates and resolves upload file paths against the
// workspace/allowlist sandbox. Rejects path escapes via .. or symlink outside roots.
func ResolveUploadPaths(raw string, workspace string, allowDirs []string) ([]string, error) {
	parts := SplitUploadPaths(raw)
	if len(parts) == 0 {
		return nil, fmt.Errorf("upload requires text= (filesystem path; multi-file: newline or || separated)")
	}
	roots := UploadAllowRoots(workspace, allowDirs)
	if len(roots) == 0 {
		return nil, fmt.Errorf("upload sandbox: set BrowserOptions.WorkspaceRoot, UploadAllowDirs, or CODEHELPER_BROWSER_UPLOAD_ALLOW")
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		resolved, err := resolveOneUploadPath(p, roots)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

func resolveOneUploadPath(p string, roots []string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("upload path is empty")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("upload path %q: %w", p, err)
	}
	abs = filepath.Clean(abs)
	// Prefer EvalSymlinks when the file exists so a symlink cannot escape the sandbox.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if !pathUnderAnyRoot(abs, roots) {
		return "", fmt.Errorf("upload path %q outside workspace/allowlist (roots: %s)", p, strings.Join(roots, ", "))
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("upload file %q: %w", p, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("upload path %q is a directory", p)
	}
	return abs, nil
}

func pathUnderAnyRoot(abs string, roots []string) bool {
	for _, root := range roots {
		root = filepath.Clean(root)
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}
