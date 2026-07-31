package parser

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

// Capabilities describes extractor strength for tooling and docs.
type Capabilities struct {
	Symbols      bool
	Imports      bool
	Calls        bool
	Inheritance  bool
	SymbolLite   bool
	LanguageName string
}

// Extractor parses one source file into symbols and edges.
type Extractor interface {
	Extract(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error)
	Capabilities() Capabilities
}

type fnExtractor struct {
	fn   func(context.Context, string, string, []byte) (*ParseResult, error)
	caps Capabilities
}

func (f *fnExtractor) Extract(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	return f.fn(ctx, repoID, relPath, buf)
}

func (f *fnExtractor) Capabilities() Capabilities { return f.caps }

var (
	extMu       sync.RWMutex
	extRegistry = map[string]Extractor{}
)

// RegisterExtractor binds file extensions (lowercase, with dot) to an extractor.
func RegisterExtractor(exts []string, caps Capabilities, fn func(context.Context, string, string, []byte) (*ParseResult, error)) {
	e := &fnExtractor{fn: fn, caps: caps}
	extMu.Lock()
	defer extMu.Unlock()
	for _, ext := range exts {
		x := strings.ToLower(ext)
		if !strings.HasPrefix(x, ".") {
			x = "." + x
		}
		extRegistry[x] = e
	}
}

// ExtractorForExt returns the extractor for an extension, or nil.
func ExtractorForExt(ext string) Extractor {
	extMu.RLock()
	defer extMu.RUnlock()
	return extRegistry[strings.ToLower(ext)]
}

// Extract dispatches by devops basename (Dockerfile/Makefile/compose), path-hinted
// Kubernetes/Ansible YAML, then file extension.
// Unreal/C++ headers often use .h (not .hpp); route those through ParseCpp when
// the buffer looks like C++/UE so class_specifier symbols are not dropped by ParseC.
func Extract(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	switch devopsKind(relPath) {
	case "dockerfile":
		return parseDockerfileLite(ctx, repoID, relPath, buf)
	case "compose":
		return parseComposeLite(ctx, repoID, relPath, buf)
	case "makefile":
		return parseMakefileLite(ctx, repoID, relPath, buf)
	}
	switch opsYAMLKind(relPath) {
	case "kubernetes":
		return parseKubernetesLite(ctx, repoID, relPath, buf)
	case "ansible":
		return parseAnsibleLite(ctx, repoID, relPath, buf)
	}
	// Blade templates share the .php extension but are not valid PHP; the PHP
	// grammar yields almost no symbols for them (see blade.go).
	if IsBladePath(relPath) {
		return ParseBlade(ctx, repoID, relPath, buf)
	}
	ext := strings.ToLower(filepath.Ext(relPath))
	if ext == ".h" && looksLikeCppHeader(buf, relPath) {
		return ParseCpp(ctx, repoID, relPath, buf)
	}
	// filepath.Ext("x.blade.php") == ".php" — Blade must win before ParsePHP.
	if IsBladePath(relPath) {
		return ParseBlade(ctx, repoID, relPath, buf)
	}
	extMu.RLock()
	e, ok := extRegistry[ext]
	extMu.RUnlock()
	if !ok || e == nil {
		return parseGenericTextLite(ctx, repoID, relPath, buf)
	}
	return e.Extract(ctx, repoID, relPath, buf)
}
