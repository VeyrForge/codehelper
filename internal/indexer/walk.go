package indexer

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var defaultSkipDirs = map[string]struct{}{
	"node_modules": {}, "vendor": {}, ".vendor": {}, ".git": {}, "dist": {}, "build": {},
	".codehelper": {}, "target": {}, "__pycache__": {}, ".venv": {}, "venv": {},
	".idea": {}, ".vscode": {}, "coverage": {}, ".next": {}, ".nuxt": {},
	".mypy_cache": {}, ".pytest_cache": {}, ".cache": {},
}

// SourceExtensions lists indexed file suffixes (must match parser registry).
var SourceExtensions = map[string]struct{}{
	".ts": {}, ".tsx": {}, ".js": {}, ".jsx": {}, ".mjs": {}, ".cjs": {},
	".py": {}, ".go": {}, ".rs": {}, ".java": {}, ".cs": {},
	".c": {}, ".h": {}, ".cc": {}, ".cpp": {}, ".cxx": {}, ".hpp": {}, ".hh": {}, ".hxx": {},
	".php": {}, ".rb": {}, ".kt": {}, ".kts": {}, ".swift": {}, ".scala": {}, ".sc": {},
	".sh": {}, ".bash": {}, ".lua": {}, ".ex": {}, ".exs": {}, ".gd": {},
	".tf": {}, ".tfvars": {}, ".hcl": {}, ".proto": {},
	".sql": {}, ".html": {}, ".htm": {}, ".css": {}, ".scss": {}, ".dart": {},
	".vue": {}, ".svelte": {}, ".astro": {}, ".mdx": {},
	// Shader/material languages (Unity/Unreal/Godot + GL/Vulkan/Metal/WebGPU).
	".hlsl": {}, ".hlsli": {}, ".fx": {}, ".fxh": {}, ".cginc": {}, ".compute": {}, ".usf": {}, ".ush": {},
	".shader": {}, ".gdshader": {}, ".gdshaderinc": {},
	".glsl": {}, ".vert": {}, ".frag": {}, ".geom": {}, ".comp": {}, ".tesc": {}, ".tese": {},
	".rgen": {}, ".rchit": {}, ".rmiss": {}, ".rahit": {}, ".rint": {}, ".rcall": {},
	".metal": {}, ".wgsl": {},
}

// WalkSourceFiles walks root and yields relative POSIX paths.
// maxSourceFileBytes is the per-file size ceiling for indexing. Files above it
// (minified bundles, generated code, huge fixtures) are skipped — they dominate
// parse time while adding little. Default 1.5MB; override CODEHELPER_MAX_FILE_BYTES.
func maxSourceFileBytes() int64 {
	if v := strings.TrimSpace(os.Getenv("CODEHELPER_MAX_FILE_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 1_500_000
}

// WalkSourceFiles walks root and yields relative POSIX paths of source files.
//
// skipDir is an EXCLUSION-only predicate (e.g. a gitignore matcher): when it
// returns true for a directory's relative path the whole subtree is pruned and
// never descended. It must NOT be an inclusion filter (like a path_glob that
// keeps only one subdir) — that would prune the very directory you want. Pass nil
// to prune nothing beyond the built-in skip set.
//
// skip is the per-FILE filter (extension/glob/gitignore): a file for which it
// returns true is dropped from the output. It is never applied to directories.
func WalkSourceFiles(root string, skipDir, skip func(rel string) bool) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			base := filepath.Base(path)
			if _, ok := defaultSkipDirs[base]; ok {
				return filepath.SkipDir
			}
			// Prune excluded subtrees WHOLE — never descend into a gitignored
			// fixture tree (a vendored linux/kubernetes checkout under .testbeds/,
			// say) just to stat ~100k files and discard them.
			if skipDir != nil && rel != "." && skipDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if _, ok := SourceExtensions[ext]; !ok {
			return nil
		}
		// Skip pathologically large files — minified/generated bundles and giant
		// fixtures (e.g. a 3-4MB TypeScript fixture or a bundled vendor.js). Parsing
		// one such file with tree-sitter can cost as much as thousands of normal
		// files while adding little real symbol value; this is a major indexing-speed
		// win (the TS case). Override with CODEHELPER_MAX_FILE_BYTES.
		if info, statErr := d.Info(); statErr == nil && info.Size() > maxSourceFileBytes() {
			return nil
		}
		if skip != nil && skip(rel) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	return out, err
}
