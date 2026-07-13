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

// Extract dispatches by file extension.
func Extract(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	ext := strings.ToLower(filepath.Ext(relPath))
	extMu.RLock()
	e, ok := extRegistry[ext]
	extMu.RUnlock()
	if !ok || e == nil {
		return parseGenericTextLite(ctx, repoID, relPath, buf)
	}
	return e.Extract(ctx, repoID, relPath, buf)
}
