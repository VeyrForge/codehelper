package vocab

import (
	"reflect"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
)

func TestSplitIdentifier(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"parseAndIngest", []string{"parse", "and", "ingest"}},
		{"WalkSourceFiles", []string{"walk", "source", "files"}},
		{"max_source_file_bytes", []string{"max", "source", "file", "bytes"}},
		{"HTTPServer", []string{"http", "server"}},
		{"sha256Sum", []string{"sha", "sum"}}, // pure-digit "256" is dropped
		{"kebab-case-name", []string{"kebab", "case", "name"}},
		{"x", nil},                          // too short
		{"v2", nil},                         // single letter + pure digit, both dropped
		{"repo_id", []string{"repo", "id"}}, // "id" is 2 runes, kept by the splitter
	}
	for _, c := range cases {
		got := SplitIdentifier(c.in)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitIdentifier(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBuildRanksByFrequency(t *testing.T) {
	syms := []graph.SymbolNameRow{
		{Name: "parseFile", Kind: "function", Language: "go", Signature: "(path string) error"},
		{Name: "parseDir", Kind: "function", Language: "go"},
		{Name: "writeFile", Kind: "function", Language: "go", Signature: "(path string) error"},
		{Name: "parseFile", Kind: "function", Language: "python"},
	}
	v := Build("demo", syms)

	if v.SymbolCount != 4 {
		t.Errorf("SymbolCount = %d, want 4", v.SymbolCount)
	}
	if !reflect.DeepEqual(v.Languages, []string{"go", "python"}) {
		t.Errorf("Languages = %v, want [go python]", v.Languages)
	}
	// "parse" appears in parseFile(x2) + parseDir = 3 (tied with "file", which sorts
	// first alphabetically) — verify the count rather than the tie-break position.
	if got := termCount(v.Terms, "parse"); got != 3 {
		t.Fatalf("expected term 'parse' count 3, got %d in %+v", got, v.Terms)
	}
	// "parseFile" is the most frequent whole identifier (count 2).
	if len(v.Identifiers) == 0 || v.Identifiers[0].Text != "parseFile" || v.Identifiers[0].Count != 2 {
		t.Fatalf("expected top identifier parseFile/2, got %+v", v.Identifiers)
	}
	// "string"/"path" come from signatures, proving params are captured.
	var sawParam bool
	for _, term := range v.Terms {
		if term.Term == "string" || term.Term == "path" {
			sawParam = true
		}
	}
	if !sawParam {
		t.Errorf("expected signature-derived terms (path/string) in %+v", v.Terms)
	}
}

func termCount(terms []TermCount, want string) int {
	for _, t := range terms {
		if t.Term == want {
			return t.Count
		}
	}
	return 0
}
