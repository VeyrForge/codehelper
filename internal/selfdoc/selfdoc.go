// Package selfdoc generates human-readable, per-package API documentation for the
// indexed project directly from the code graph — deterministically and without an
// LLM. It is the self-documentation counterpart to the external-library `docs`
// engine: where `docs` fetches third-party docs, selfdoc renders YOUR project.
//
// Design goals (see the feature request that motivated it):
//   - Bounded size: only the public/exported surface is documented, written as one
//     small markdown file per package plus an index — so the output grows O(public
//     symbols), never O(every function + traced flow), and a reader loads only the
//     package they care about instead of one giant file.
//   - Commit-friendly: packages, symbols, and caller lists are sorted, and files
//     are only rewritten when their content actually changes (content-hash), so a
//     one-line code change yields a one-line doc diff.
//   - Multi-language: rendered from the language-agnostic graph; the only
//     per-language logic is the exported-symbol heuristic.
//   - LLM-free: signatures come from the symbol's declaration line in the source.
package selfdoc

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// Options controls generation. Zero value is usable after defaults are applied.
type Options struct {
	OutDir       string // output directory for the markdown (default <root>/docs/api)
	MaxCallers   int    // cap on in-repo callers listed per symbol (default 8)
	IncludeTests bool   // include test files (default false)
}

func (o *Options) applyDefaults(repoRoot string) {
	if o.OutDir == "" {
		o.OutDir = filepath.Join(repoRoot, "docs", "api")
	}
	if o.MaxCallers == 0 {
		o.MaxCallers = 8
	}
}

// Result summarises a generation run.
type Result struct {
	OutDir         string
	Packages       int
	Symbols        int
	FilesWritten   int // package/index files whose content changed and were rewritten
	FilesUnchanged int // files whose content was identical (not rewritten)
	IndexPath      string
}

// documentableKinds are the symbol kinds worth listing in API docs.
var documentableKinds = map[types.SymbolKind]bool{
	types.SymbolKindFunction:  true,
	types.SymbolKindMethod:    true,
	types.SymbolKindClass:     true,
	types.SymbolKindInterface: true,
	types.SymbolKindTypeAlias: true,
	types.SymbolKindEnum:      true,
	types.SymbolKindNamespace: true,
}

// symEntry is one documented symbol plus its rendered context.
type symEntry struct {
	sym       types.Symbol
	signature string
	callers   []string // distinct in-repo caller display names (sorted)
	moreCall  int      // callers beyond the cap
}

// pkgDoc accumulates the documented symbols for a single package (directory).
type pkgDoc struct {
	name      string // package path relative to repo root, e.g. "internal/foo"
	files     map[string]struct{}
	types     []symEntry
	functions []symEntry
	methods   []symEntry
}

// Generate renders per-package API docs for repoID into opts.OutDir and returns a
// summary. It reads source files (under repoRoot) only to extract signature lines.
func Generate(ctx context.Context, st *graph.Store, repoID, repoRoot string, opts Options) (Result, error) {
	opts.applyDefaults(repoRoot)
	res := Result{OutDir: opts.OutDir}

	pathsSet, err := st.AllSymbolPaths(ctx, repoID)
	if err != nil {
		return res, err
	}
	allPaths := make([]string, 0, len(pathsSet))
	for p := range pathsSet {
		allPaths = append(allPaths, p)
	}
	sort.Strings(allPaths)

	pkgs := map[string]*pkgDoc{}
	for _, path := range allPaths {
		if !opts.IncludeTests && isTestPath(path) {
			continue
		}
		lines := readLines(filepath.Join(repoRoot, filepath.FromSlash(path)))
		syms, err := st.SymbolsForPath(ctx, repoID, path)
		if err != nil {
			return res, err
		}
		// Resolve method receiver names within this file's symbols.
		nameByID := make(map[string]string, len(syms))
		for _, s := range syms {
			nameByID[s.ID] = s.Name
		}
		pkgName := packageOf(path)
		for _, s := range syms {
			if !documentableKinds[s.Kind] || !isPublic(s.Name, s.Language) {
				continue
			}
			pd := pkgs[pkgName]
			if pd == nil {
				pd = &pkgDoc{name: pkgName, files: map[string]struct{}{}}
				pkgs[pkgName] = pd
			}
			pd.files[path] = struct{}{}
			callers, more := callerNames(ctx, st, repoID, s.ID, opts.MaxCallers, opts.IncludeTests)
			e := symEntry{
				sym:       s,
				signature: signatureLine(lines, s.LineStart),
				callers:   callers,
				moreCall:  more,
			}
			switch s.Kind {
			case types.SymbolKindMethod:
				if recv := receiverName(s, nameByID); recv != "" {
					e.sym.Name = recv + "." + s.Name
				}
				pd.methods = append(pd.methods, e)
			case types.SymbolKindFunction:
				pd.functions = append(pd.functions, e)
			default:
				pd.types = append(pd.types, e)
			}
			res.Symbols++
		}
	}

	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return res, err
	}

	pkgNames := make([]string, 0, len(pkgs))
	for name := range pkgs {
		pkgNames = append(pkgNames, name)
	}
	sort.Strings(pkgNames)
	res.Packages = len(pkgNames)

	for _, name := range pkgNames {
		pd := pkgs[name]
		sortEntries(pd.types)
		sortEntries(pd.functions)
		sortEntries(pd.methods)
		md := renderPackage(pd)
		out := filepath.Join(opts.OutDir, slug(name)+".md")
		changed, err := writeIfChanged(out, md)
		if err != nil {
			return res, err
		}
		tallyWrite(&res, changed)
	}

	idx := renderIndex(repoID, pkgs, pkgNames)
	res.IndexPath = filepath.Join(opts.OutDir, "README.md")
	changed, err := writeIfChanged(res.IndexPath, idx)
	if err != nil {
		return res, err
	}
	tallyWrite(&res, changed)

	return res, nil
}

func tallyWrite(res *Result, changed bool) {
	if changed {
		res.FilesWritten++
	} else {
		res.FilesUnchanged++
	}
}

// renderPackage renders one package's markdown. Deterministic given sorted input.
func renderPackage(pd *pkgDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# package %s\n\n", pd.name)
	fmt.Fprintf(&b, "%s\n\n", genNotice)
	total := len(pd.types) + len(pd.functions) + len(pd.methods)
	files := make([]string, 0, len(pd.files))
	for f := range pd.files {
		files = append(files, f)
	}
	sort.Strings(files)
	fmt.Fprintf(&b, "_%d public symbols across %d file(s)._\n\n", total, len(files))

	writeSection(&b, "Types & Interfaces", pd.types)
	writeSection(&b, "Functions", pd.functions)
	writeSection(&b, "Methods", pd.methods)

	b.WriteString("## Files\n\n")
	for _, f := range files {
		fmt.Fprintf(&b, "- `%s`\n", f)
	}
	b.WriteString("\n")
	return b.String()
}

func writeSection(b *strings.Builder, title string, entries []symEntry) {
	if len(entries) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s\n\n", title)
	for _, e := range entries {
		fmt.Fprintf(b, "### %s\n\n", e.sym.Name)
		loc := fmt.Sprintf("%s:%d", e.sym.Path, e.sym.LineStart)
		if e.signature != "" {
			fmt.Fprintf(b, "```%s\n%s\n```\n", langTag(e.sym.Language), e.signature)
		}
		fmt.Fprintf(b, "_%s · `%s`_\n\n", e.sym.Kind, loc)
		if len(e.callers) > 0 {
			callers := append([]string(nil), e.callers...)
			sort.Strings(callers)
			line := strings.Join(callers, ", ")
			if e.moreCall > 0 {
				line += fmt.Sprintf(" (+%d more)", e.moreCall)
			}
			fmt.Fprintf(b, "Used by: %s\n\n", line)
		}
	}
}

// renderIndex renders the top-level README linking every package doc.
func renderIndex(repoID string, pkgs map[string]*pkgDoc, pkgNames []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — project API documentation\n\n", repoID)
	fmt.Fprintf(&b, "%s\n\n", genNotice)
	b.WriteString("| Package | Public symbols | Documentation |\n")
	b.WriteString("|---|---|---|\n")
	for _, name := range pkgNames {
		pd := pkgs[name]
		total := len(pd.types) + len(pd.functions) + len(pd.methods)
		fmt.Fprintf(&b, "| `%s` | %d | [%s](%s.md) |\n", name, total, name, slug(name))
	}
	b.WriteString("\n")
	return b.String()
}

const genNotice = "<!-- Generated by `codehelper docgen` — deterministic, no LLM. Do not edit by hand. -->"

// signatureLine returns the trimmed declaration line for a symbol (1-based
// lineStart), capped to a readable length. Empty when unavailable.
func signatureLine(lines []string, lineStart int) string {
	if lineStart <= 0 || lineStart > len(lines) {
		return ""
	}
	s := strings.TrimSpace(lines[lineStart-1])
	// Drop a trailing opening brace so the signature reads cleanly.
	s = strings.TrimRight(s, " {")
	const maxLen = 200
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}

// callerNames returns up to max distinct in-repo caller display names (sorted)
// and the count of callers beyond the cap. Test-file callers are excluded unless
// includeTests is set, so "Used by" reflects real production usage.
func callerNames(ctx context.Context, st *graph.Store, repoID, symID string, max int, includeTests bool) ([]string, int) {
	callers, err := st.CallersOf(ctx, repoID, symID)
	if err != nil {
		return nil, 0
	}
	seen := map[string]struct{}{}
	var names []string
	for _, c := range callers {
		if _, ok := seen[c.Name]; ok || c.Name == "" {
			continue
		}
		if !includeTests && isTestPath(c.Path) {
			continue
		}
		seen[c.Name] = struct{}{}
		names = append(names, c.Name)
	}
	sort.Strings(names)
	if len(names) > max {
		return names[:max], len(names) - max
	}
	return names, 0
}

// receiverName resolves a method's receiver type name from its ParentID when the
// parent symbol is in the same file; otherwise returns "".
func receiverName(s types.Symbol, nameByID map[string]string) string {
	if s.ParentID == "" {
		return ""
	}
	return nameByID[s.ParentID]
}

// isPublic reports whether a symbol is part of the public/exported surface.
// Go uses capitalization; other languages treat a leading underscore as private.
func isPublic(name, language string) bool {
	base := name
	if i := strings.LastIndex(base, "."); i >= 0 {
		base = base[i+1:]
	}
	if base == "" {
		return false
	}
	switch language {
	case "go", "golang":
		r, _ := utf8.DecodeRuneInString(base)
		return unicode.IsUpper(r)
	default:
		return !strings.HasPrefix(base, "_")
	}
}

// packageOf returns the package identifier for a repo-relative file path: its
// directory (POSIX slashes), or "root" for top-level files.
func packageOf(path string) string {
	dir := filepath.ToSlash(filepath.Dir(filepath.ToSlash(path)))
	if dir == "." || dir == "" {
		return "root"
	}
	return dir
}

// slug turns a package path into a flat, filesystem-safe markdown filename stem.
func slug(pkg string) string {
	if pkg == "root" || pkg == "." || pkg == "" {
		return "root"
	}
	s := strings.ReplaceAll(filepath.ToSlash(pkg), "/", "__")
	return s
}

func isTestPath(path string) bool {
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.Contains(base, ".test.") || strings.Contains(base, ".spec."):
		return true
	case strings.Contains(filepath.ToSlash(path), "__tests__/"):
		return true
	}
	return false
}

func langTag(language string) string {
	switch language {
	case "go", "golang":
		return "go"
	case "typescript", "ts":
		return "ts"
	case "javascript", "js":
		return "js"
	case "python", "py":
		return "python"
	default:
		return ""
	}
}

func sortEntries(es []symEntry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].sym.Name != es[j].sym.Name {
			return es[i].sym.Name < es[j].sym.Name
		}
		return es[i].sym.LineStart < es[j].sym.LineStart
	})
}

func readLines(absPath string) []string {
	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	return strings.Split(string(b), "\n")
}

// writeIfChanged writes content only when it differs from what's on disk,
// returning whether a write occurred. Keeps doc diffs minimal across runs.
func writeIfChanged(path, content string) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil {
		if sha256.Sum256(existing) == sha256.Sum256([]byte(content)) {
			return false, nil
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
