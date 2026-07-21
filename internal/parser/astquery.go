package parser

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/elixir"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/hcl"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/kotlin"
	"github.com/smacker/go-tree-sitter/lua"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/protobuf"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/scala"
	"github.com/smacker/go-tree-sitter/swift"
	tsx "github.com/smacker/go-tree-sitter/typescript/tsx"
	tst "github.com/smacker/go-tree-sitter/typescript/typescript"
)

// astLanguages maps a canonical language name (and common aliases) to the
// tree-sitter grammar used for structural search. Only grammars with a real
// (non symbol-lite) parser are exposed — symbol-lite languages have no useful
// node structure to query.
var astLanguages = map[string]func() *sitter.Language{
	"go":         golang.GetLanguage,
	"golang":     golang.GetLanguage,
	"python":     python.GetLanguage,
	"py":         python.GetLanguage,
	"typescript": tst.GetLanguage,
	"ts":         tst.GetLanguage,
	"tsx":        tsx.GetLanguage,
	"javascript": tsx.GetLanguage, // tsx grammar is a superset that parses JS/JSX
	"js":         tsx.GetLanguage,
	"jsx":        tsx.GetLanguage,
	"rust":       rust.GetLanguage,
	"rs":         rust.GetLanguage,
	"java":       java.GetLanguage,
	"csharp":     csharp.GetLanguage,
	"cs":         csharp.GetLanguage,
	"c":          c.GetLanguage,
	"cpp":        cpp.GetLanguage,
	"c++":        cpp.GetLanguage,
	"php":        php.GetLanguage,
	"ruby":       ruby.GetLanguage,
	"rb":         ruby.GetLanguage,
	"kotlin":     kotlin.GetLanguage,
	"kt":         kotlin.GetLanguage,
	"swift":      swift.GetLanguage,
	"scala":      scala.GetLanguage,
	"lua":        lua.GetLanguage,
	"elixir":     elixir.GetLanguage,
	"bash":       bash.GetLanguage,
	"sh":         bash.GetLanguage,
	"hcl":        hcl.GetLanguage,
	"terraform":  hcl.GetLanguage,
	"protobuf":   protobuf.GetLanguage,
	"proto":      protobuf.GetLanguage,
	// Svelte SFCs have no dedicated grammar here — map to the TSX/JS grammar and
	// scan extracted <script> bodies (see ScanFile svelte mode).
	"svelte": tsx.GetLanguage,
}

// astExtensions maps a language name to the file extensions it should scan, so
// the ast_query tool can pick the right files without re-deriving them from the
// extractor registry (which keys by extension, not language).
var astExtensions = map[string][]string{
	"go":         {".go"},
	"python":     {".py"},
	"typescript": {".ts", ".tsx"},
	"javascript": {".js", ".jsx", ".mjs", ".cjs"},
	"rust":       {".rs"},
	"java":       {".java"},
	"csharp":     {".cs"},
	"c":          {".c", ".h"},
	"cpp":        {".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx"},
	"php":        {".php"},
	"ruby":       {".rb"},
	"kotlin":     {".kt", ".kts"},
	"swift":      {".swift"},
	"scala":      {".scala", ".sc"},
	"lua":        {".lua"},
	"elixir":     {".ex", ".exs"},
	"bash":       {".sh", ".bash"},
	"hcl":        {".tf", ".tfvars", ".hcl"},
	"protobuf":   {".proto"},
	"svelte":     {".svelte"},
}

// CanonicalASTLanguage resolves an alias to its canonical language name, or "" if unsupported.
func CanonicalASTLanguage(name string) string {
	return canonicalLang(name)
}

// canonicalLang resolves an alias to its canonical language name (the key used
// by astExtensions), or "" if unsupported.
func canonicalLang(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "golang":
		return "go"
	case "py":
		return "python"
	case "ts":
		return "typescript"
	case "tsx":
		return "typescript"
	case "js", "jsx":
		return "javascript"
	case "rs":
		return "rust"
	case "cs":
		return "csharp"
	case "c++":
		return "cpp"
	case "rb":
		return "ruby"
	case "kt":
		return "kotlin"
	case "sh":
		return "bash"
	case "terraform":
		return "hcl"
	case "proto":
		return "protobuf"
	case "svelte", "sveltekit":
		return "svelte"
	}
	if _, ok := astExtensions[name]; ok {
		return name
	}
	return ""
}

// SupportedASTLanguages returns the canonical language names ast_query accepts,
// sorted, for use in tool help and error messages.
func SupportedASTLanguages() []string {
	out := make([]string, 0, len(astExtensions))
	for k := range astExtensions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ExtensionsForASTLanguage returns the file extensions scanned for a language
// (canonical name or alias), or nil if the language is unsupported.
func ExtensionsForASTLanguage(name string) []string {
	c := canonicalLang(name)
	if c == "" {
		return nil
	}
	return astExtensions[c]
}

// ASTMatch is one capture from a structural query: the captured node's text and
// its location. One QueryMatch with several captures yields several ASTMatches.
type ASTMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"` // 1-based start line of the captured node
	Col     int    `json:"col"`  // 1-based start column
	Capture string `json:"capture"`
	Text    string `json:"text"`
	Kind    string `json:"kind"` // tree-sitter node type of the captured node
}

const astSnippetCap = 240 // keep captured text token-cheap

// ASTScanner holds a compiled query plus a reusable parser and cursor so a
// pattern can be run across many files without recompiling the query or
// reallocating C resources per file. Construct once, ScanFile in a loop, Close
// when done. Not safe for concurrent use (one parser/cursor).
type ASTScanner struct {
	lang       *sitter.Language
	q          *sitter.Query
	p          *sitter.Parser
	qc         *sitter.QueryCursor
	svelteMode bool // extract <script> bodies from .svelte before querying
}

// NewASTScanner compiles the pattern for the language once. Returns a precise
// error (unsupported language, or tree-sitter syntax error with offset) without
// touching the filesystem.
func NewASTScanner(language, pattern string) (*ASTScanner, error) {
	langKey := strings.ToLower(strings.TrimSpace(language))
	getLang, ok := astLanguages[langKey]
	if !ok {
		return nil, fmt.Errorf("unsupported language %q (supported: %s)", language, strings.Join(SupportedASTLanguages(), ", "))
	}
	lang := getLang()
	q, err := sitter.NewQuery([]byte(pattern), lang)
	if err != nil {
		return nil, fmt.Errorf("invalid tree-sitter pattern: %w", err)
	}
	p := sitter.NewParser()
	p.SetLanguage(lang)
	return &ASTScanner{
		lang:       lang,
		q:          q,
		p:          p,
		qc:         sitter.NewQueryCursor(),
		svelteMode: langKey == "svelte" || langKey == "sveltekit",
	}, nil
}

// Close releases the compiled query and cursor. Safe to call once.
func (s *ASTScanner) Close() {
	if s.q != nil {
		s.q.Close()
	}
	if s.qc != nil {
		s.qc.Close()
	}
}

// ScanFile runs the compiled pattern over one file's source and appends matches
// to out (stopping once len(*out) >= limit). Parse failures for a single file
// are non-fatal (skipped). Errors only on predicate evaluation panics.
//
// In svelteMode, each <script> body is queried with the JS/TS grammar and match
// lines are remapped onto the .svelte file (markup outside scripts is ignored).
func (s *ASTScanner) ScanFile(ctx context.Context, relPath string, buf []byte, out *[]ASTMatch, limit int) (err error) {
	// The tree-sitter binding evaluates a `#match?` predicate by calling
	// regexp.MustCompile on the user-supplied regex, which PANICS on a malformed
	// pattern. Convert that into a clean error so a bad query can't crash the
	// server process.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("invalid tree-sitter pattern (predicate evaluation failed): %v", r)
		}
	}()

	if s.svelteMode {
		return s.scanSvelteScripts(ctx, relPath, buf, out, limit)
	}
	return s.scanBuffer(ctx, relPath, buf, 0, out, limit)
}

func (s *ASTScanner) scanSvelteScripts(ctx context.Context, relPath string, buf []byte, out *[]ASTMatch, limit int) error {
	text := string(buf)
	for _, m := range svelteScriptRe.FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 6 || len(*out) >= limit {
			break
		}
		bodyStart, bodyEnd := m[4], m[5]
		body := text[bodyStart:bodyEnd]
		if strings.TrimSpace(body) == "" {
			continue
		}
		lineOffset := 1 + strings.Count(text[:bodyStart], "\n")
		if err := s.scanBuffer(ctx, relPath, []byte(body), lineOffset-1, out, limit); err != nil {
			return err
		}
	}
	return nil
}

func (s *ASTScanner) scanBuffer(ctx context.Context, relPath string, buf []byte, lineDelta int, out *[]ASTMatch, limit int) error {
	tree, perr := s.p.ParseCtx(ctx, nil, buf)
	if perr != nil || tree == nil {
		return nil // unparseable file: skip, not fatal
	}
	defer tree.Close()

	s.qc.Exec(s.q, tree.RootNode())
	for {
		if len(*out) >= limit {
			return nil
		}
		m, ok := s.qc.NextMatch()
		if !ok {
			break
		}
		m = s.qc.FilterPredicates(m, buf)
		for _, cap := range m.Captures {
			if len(*out) >= limit {
				return nil
			}
			node := cap.Node
			start := node.StartPoint()
			*out = append(*out, ASTMatch{
				Path:    relPath,
				Line:    int(start.Row) + 1 + lineDelta,
				Col:     int(start.Column) + 1,
				Capture: s.q.CaptureNameForId(cap.Index),
				Text:    snippet(node.Content(buf)),
				Kind:    node.Type(),
			})
		}
	}
	return nil
}

func snippet(s string) string {
	s = strings.TrimRight(s, "\n")
	if len(s) > astSnippetCap {
		return s[:astSnippetCap] + "…"
	}
	return s
}

// ScanResult is the outcome of a parallel scan over many files.
type ScanResult struct {
	Matches []ASTMatch
	Scanned int // files actually read+parsed (may be < len(files) on early exit)
}

// ScanFiles runs the pattern over many files concurrently — the technique tools
// like ast-grep use to stay fast on 100k+ file repos — with one ASTScanner per
// worker (the parser/cursor are not safe to share). It validates the pattern
// once up front (clean error, no goroutines spawned on a bad query), stripes
// files across workers so early matches are found across the whole tree, and
// stops every worker once `limit` total matches are collected. `readFile` is
// injected so the caller owns IO. Results are sorted by path:line for a stable
// response. workers<=0 defaults to GOMAXPROCS.
func ScanFiles(ctx context.Context, language, pattern string, files []string, readFile func(rel string) ([]byte, error), limit, workers int) (ScanResult, error) {
	// Compile once to surface a precise syntax/language error before doing any work.
	if probe, err := NewASTScanner(language, pattern); err != nil {
		return ScanResult{}, err
	} else {
		probe.Close()
	}
	if limit <= 0 || len(files) == 0 {
		return ScanResult{}, nil
	}
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(files) {
		workers = len(files)
	}

	var (
		mu           sync.Mutex
		all          []ASTMatch
		totalMatches int64
		scanned      int64
		firstErr     error
		errOnce      sync.Once
		wg           sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			sc, err := NewASTScanner(language, pattern)
			if err != nil { // already validated above; treat as fatal if it ever happens
				errOnce.Do(func() { firstErr = err })
				return
			}
			defer sc.Close()
			var local []ASTMatch
			for i := start; i < len(files); i += workers {
				if ctx.Err() != nil {
					break
				}
				if atomic.LoadInt64(&totalMatches) >= int64(limit) {
					break // another worker already hit the cap
				}
				buf, rerr := readFile(files[i])
				if rerr != nil {
					continue
				}
				atomic.AddInt64(&scanned, 1)
				before := len(local)
				if serr := sc.ScanFile(ctx, files[i], buf, &local, limit); serr != nil {
					errOnce.Do(func() { firstErr = serr })
					return // a predicate-eval failure repeats on every file; bail this worker
				}
				if added := len(local) - before; added > 0 {
					atomic.AddInt64(&totalMatches, int64(added))
				}
			}
			if len(local) > 0 {
				mu.Lock()
				all = append(all, local...)
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	if firstErr != nil {
		return ScanResult{Scanned: int(scanned)}, firstErr
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Path != all[j].Path {
			return all[i].Path < all[j].Path
		}
		if all[i].Line != all[j].Line {
			return all[i].Line < all[j].Line
		}
		return all[i].Col < all[j].Col
	})
	return ScanResult{Matches: all, Scanned: int(scanned)}, nil
}
