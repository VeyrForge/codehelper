package retrieval

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DiskMatch is an on-disk identifier hit found by scanning source files directly,
// bypassing the symbol index. It bridges the gap when a symbol exists in a new or
// uncommitted file the index has not caught up with yet — the case that makes a
// pure-index tool lose to a live-filesystem grep.
type DiskMatch struct {
	Path  string `json:"path"` // repo-relative, forward-slashed
	Line  int    `json:"line"`
	Text  string `json:"text"`
	IsDef bool   `json:"is_def,omitempty"` // line looks like a definition, not just a reference
}

// diskScanSkipDirs mirrors the indexer/freshness ignore set. Kept local so this
// package stays import-cycle free.
var diskScanSkipDirs = map[string]bool{
	".git": true, ".codehelper": true, "node_modules": true, "vendor": true,
	"target": true, "obj": true, "dist": true, "build": true, "out": true, "tmp": true,
	"__pycache__": true,
	".venv": true, "venv": true, ".idea": true, ".vscode": true, "third_party": true,
	"coverage": true, ".next": true, ".nuxt": true, ".cache": true,
	".turbo": true, ".parcel-cache": true, ".output": true, ".svelte-kit": true,
	"storybook-static": true, ".angular": true, ".vercel": true, ".netlify": true,
	".dart_tool": true, ".gradle": true, ".tox": true, ".nyc_output": true,
	"site-packages": true,
}

// diskScanSrcExt is the set of source extensions worth scanning for an identifier.
var diskScanSrcExt = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".py": true, ".rs": true, ".java": true,
	".cs": true, ".c": true, ".h": true, ".cc": true, ".cpp": true,
	".cxx": true, ".hpp": true, ".hh": true, ".hxx": true, ".php": true, ".rb": true,
	".kt": true, ".swift": true, ".scala": true, ".lua": true, ".ex": true, ".exs": true,
}

const (
	diskScanFileCap    = 6000 // bound the walk on a pathological monorepo
	diskScanMaxLineLen = 240  // truncate the returned snippet text to this
	diskScanMaxPerFile = 4000 // stop scanning a file past this many lines
	diskScanMaxRawLine = 2000 // skip minified/generated lines longer than this
)

// defKeywords mark a line as a likely DEFINITION of the identifier rather than a
// mere reference — these rank first so the agent gets the declaration.
var defKeywords = []string{
	"func ", "type ", "class ", "def ", "function ", "interface ", "struct ",
	"const ", "var ", "let ", "fn ", "impl ", "trait ", "enum ", "module ", "object ",
}

// DiskGrepIdentifier scans source files under root for `name` appearing as a whole
// identifier, returning up to `limit` matches with definition-looking lines first.
// It is the disk fallback for a symbol-index miss: it sees new/uncommitted files
// the graph has not indexed yet. Bounded (diskScanFileCap files, early-exit once
// enough definition matches are collected) so it stays a cheap last resort.
func DiskGrepIdentifier(root, name string, limit int) []DiskMatch {
	name = strings.TrimSpace(name)
	if root == "" || name == "" || limit <= 0 {
		return nil
	}
	// Only scan for plausible identifiers — a multi-word phrase or symbols would
	// match too loosely and is the wrong job for a disk grep.
	if !isIdentifierLike(name) {
		return nil
	}
	re, err := regexp.Compile(`(^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(name) + `($|[^\p{L}\p{N}_])`)
	if err != nil {
		return nil
	}
	root = filepath.Clean(root)
	var defs, refs []DiskMatch
	files := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && diskScanSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		// Enough definitions found — stop walking.
		if len(defs) >= limit {
			return fs.SkipAll
		}
		if !diskScanSrcExt[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		files++
		if files > diskScanFileCap {
			return fs.SkipAll
		}
		f, ferr := os.Open(p)
		if ferr != nil {
			return nil
		}
		defer f.Close()
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			if lineNo > diskScanMaxPerFile {
				break
			}
			line := sc.Text()
			if len(line) > diskScanMaxRawLine {
				continue
			}
			if !re.MatchString(line) {
				continue
			}
			text := strings.TrimSpace(line)
			if len(text) > diskScanMaxLineLen {
				text = text[:diskScanMaxLineLen] + "…"
			}
			m := DiskMatch{Path: rel, Line: lineNo, Text: text, IsDef: looksLikeDef(line, name)}
			if m.IsDef {
				defs = append(defs, m)
				if len(defs) >= limit {
					break
				}
			} else if len(refs) < limit {
				refs = append(refs, m)
			}
		}
		return nil
	})
	// Definitions first, then references, capped to limit.
	out := defs
	for _, r := range refs {
		if len(out) >= limit {
			break
		}
		out = append(out, r)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// looksLikeDef reports whether a line declares `name` (vs. merely referencing it).
func looksLikeDef(line, name string) bool {
	low := strings.ToLower(line)
	for _, kw := range defKeywords {
		if strings.Contains(low, kw) {
			return true
		}
	}
	// `name = function(...)` / `name := ...` / `name(...) {` style definitions.
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, name) {
		rest := strings.TrimSpace(trimmed[len(name):])
		if strings.HasPrefix(rest, ":=") || strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, "(") {
			return true
		}
	}
	return false
}

// isIdentifierLike reports whether s is a single bare identifier (or a sym: id),
// the only input a disk grep can match precisely.
func isIdentifierLike(s string) bool {
	if strings.HasPrefix(s, "sym:") {
		// A sym: id ends with the bare name after the last colon.
		if i := strings.LastIndexByte(s, ':'); i >= 0 {
			s = s[i+1:]
		}
	}
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return false
	}
	for _, r := range s {
		if r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}
