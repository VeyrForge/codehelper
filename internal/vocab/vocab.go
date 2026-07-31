// Package vocab builds a project vocabulary seed: the most frequent identifiers
// and identifier sub-words across the indexed codebase, in every supported
// language. It is a DERIVED artifact — a deterministic function of the symbol
// graph — so it is regenerated on each index and stored in the (gitignored) index
// dir. Its purpose is to seed the human-reviewed project glossary: a later review
// step promotes the genuinely meaningful terms into project_memory.json, which is
// the shared, committable knowledge layer.
package vocab

import (
	"encoding/json"
	"os"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/paths"
)

// TermCount is one ranked sub-word term and how often it appears across all
// identifiers (and their signatures) in the project.
type TermCount struct {
	Term  string `json:"term"`
	Count int    `json:"count"`
}

// IdentCount is one ranked whole identifier (symbol name) and its frequency.
type IdentCount struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
	Kind  string `json:"kind,omitempty"` // most common kind for this identifier
}

// Vocabulary is the persisted seed. Identifiers are whole symbol names; Terms are
// the camelCase/snake_case sub-words that compose them and their signatures — the
// raw material for a project glossary, before review.
type Vocabulary struct {
	RepoID      string       `json:"repo_id"`
	SymbolCount int          `json:"symbol_count"`
	Languages   []string     `json:"languages"`
	Identifiers []IdentCount `json:"identifiers"`
	Terms       []TermCount  `json:"terms"`
}

// Caps bound the persisted file so it stays scannable on large repos; the full
// frequency tables are not kept, only the most frequent head.
const (
	maxIdentifiers = 400
	maxTerms       = 600
)

// stopTerms drops two kinds of noise so the seed stays focused on project
// vocabulary: ultra-generic programming sub-words, and common English function
// words — the latter matter because some languages' signature field carries the
// doc comment (prose), not just a type signature, so identifiers and prose mix in
// the term stream. The review step can add anything back; this only trims obvious
// noise and is not authoritative.
var stopTerms = newStopSet(
	// generic programming tokens
	"get", "set", "new", "err", "ctx", "val", "tmp", "str", "num", "len",
	"ptr", "ok", "id", "fn", "func", "var", "int", "map", "arr", "obj",
	"buf", "idx", "cur", "out", "res", "req", "args", "opts", "cfg", "nil",
	"true", "false", "void", "this", "self", "init", "data", "value", "key",
	// common English function words (doc-comment prose)
	"the", "and", "for", "with", "from", "into", "that", "this", "are", "was",
	"its", "it", "is", "be", "to", "of", "in", "on", "or", "an", "as", "at",
	"by", "if", "so", "we", "do", "no", "not", "but", "can", "all", "any",
	"per", "via", "one", "two", "use", "used", "uses", "when", "then", "than",
	"they", "them", "you", "your", "our", "has", "have", "had", "will", "would",
	"should", "must", "may", "might", "each", "only", "also", "such", "same",
	"other", "which", "what", "where", "who", "how", "why", "returns", "return",
	"using", "calls", "call", "called", "after", "before", "back", "over",
	"because", "without", "within", "across", "between", "first", "last", "still",
	"never", "always", "just", "even", "here", "there", "their", "while", "does",
)

func newStopSet(words ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}

// SplitIdentifier breaks one identifier into lowercased sub-word tokens, handling
// every casing convention across the supported languages at once: snake_case and
// kebab-case (delimiter split), camelCase and PascalCase (lower/digit→upper
// boundary), acronym runs (HTTPServer → http, server), and letter↔digit
// transitions (sha256 → sha, 256). Tokens shorter than two runes and pure-numeric
// tokens are dropped.
func SplitIdentifier(s string) []string {
	runes := []rune(s)
	var tokens []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			tokens = append(tokens, string(cur))
			cur = cur[:0]
		}
	}
	for i, r := range runes {
		if !isAlnum(r) {
			flush()
			continue
		}
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			switch {
			case isUpper(r) && (isLower(prev) || isDigit(prev)):
				flush() // fooBar / foo2Bar → foo | Bar
			case isUpper(r) && isUpper(prev) && i+1 < len(runes) && isLower(runes[i+1]):
				flush() // HTTPServer → HTTP | Server
			case isDigit(r) != isDigit(prev):
				flush() // sha256 → sha | 256
			}
		}
		cur = append(cur, r)
	}
	flush()

	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		lt := strings.ToLower(t)
		if len([]rune(lt)) < 2 || allDigits(lt) {
			continue
		}
		out = append(out, lt)
	}
	return out
}

// Build computes the ranked vocabulary from every symbol in the repo. Whole
// identifiers come from symbol names; sub-word terms are drawn from both names and
// signatures (so parameter and type identifiers are captured too).
func Build(repoID string, syms []graph.SymbolNameRow) Vocabulary {
	identCounts := map[string]int{}
	identKinds := map[string]map[string]int{}
	termCounts := map[string]int{}
	langSet := map[string]struct{}{}

	for _, s := range syms {
		name := strings.TrimSpace(s.Name)
		if name != "" {
			identCounts[name]++
			if identKinds[name] == nil {
				identKinds[name] = map[string]int{}
			}
			if s.Kind != "" {
				identKinds[name][s.Kind]++
			}
		}
		if s.Language != "" {
			langSet[s.Language] = struct{}{}
		}
		for _, tok := range SplitIdentifier(name) {
			if _, skip := stopTerms[tok]; !skip {
				termCounts[tok]++
			}
		}
		for _, tok := range SplitIdentifier(s.Signature) {
			if _, skip := stopTerms[tok]; !skip {
				termCounts[tok]++
			}
		}
	}

	idents := make([]IdentCount, 0, len(identCounts))
	for text, c := range identCounts {
		idents = append(idents, IdentCount{Text: text, Count: c, Kind: topKind(identKinds[text])})
	}
	sortIdents(idents)
	if len(idents) > maxIdentifiers {
		idents = idents[:maxIdentifiers]
	}

	terms := make([]TermCount, 0, len(termCounts))
	for t, c := range termCounts {
		terms = append(terms, TermCount{Term: t, Count: c})
	}
	sortTerms(terms)
	if len(terms) > maxTerms {
		terms = terms[:maxTerms]
	}

	langs := make([]string, 0, len(langSet))
	for l := range langSet {
		langs = append(langs, l)
	}
	sort.Strings(langs)

	return Vocabulary{
		RepoID:      repoID,
		SymbolCount: len(syms),
		Languages:   langs,
		Identifiers: idents,
		Terms:       terms,
	}
}

// Write builds and persists the vocabulary seed for a repo. It mirrors the other
// index artifacts: pretty JSON, atomic tmp+rename.
func Write(repoRoot, repoID string, syms []graph.SymbolNameRow) error {
	v := Build(repoID, syms)
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	path := paths.VocabPath(repoRoot)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads the persisted vocabulary, returning a zero value when absent.
func Load(repoRoot string) (Vocabulary, error) {
	var v Vocabulary
	b, err := os.ReadFile(paths.VocabPath(repoRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return v, nil
		}
		return v, err
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, err
	}
	return v, nil
}

func sortIdents(s []IdentCount) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Count != s[j].Count {
			return s[i].Count > s[j].Count
		}
		return s[i].Text < s[j].Text
	})
}

func sortTerms(s []TermCount) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Count != s[j].Count {
			return s[i].Count > s[j].Count
		}
		return s[i].Term < s[j].Term
	})
}

func topKind(kinds map[string]int) string {
	best, bestN := "", 0
	for k, n := range kinds {
		if n > bestN || (n == bestN && k < best) {
			best, bestN = k, n
		}
	}
	return best
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }
func isAlnum(r rune) bool { return isUpper(r) || isLower(r) || isDigit(r) }

func allDigits(s string) bool {
	for _, r := range s {
		if !isDigit(r) {
			return false
		}
	}
	return s != ""
}
