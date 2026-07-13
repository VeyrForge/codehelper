package parser

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func scanOne(t *testing.T, lang, pattern, path string, src []byte, limit int) []ASTMatch {
	t.Helper()
	s, err := NewASTScanner(lang, pattern)
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	defer s.Close()
	var out []ASTMatch
	if err := s.ScanFile(context.Background(), path, src, &out, limit); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func TestASTQuery_GoFunctionNames(t *testing.T) {
	src := []byte(`package p

func Alpha() {}

func beta(x int) error { return nil }

type T struct{}

func (t T) Gamma() {}
`)
	out := scanOne(t, "go", `(function_declaration name: (identifier) @fn)`, "p.go", src, 50)
	got := map[string]int{}
	for _, m := range out {
		if m.Capture != "fn" {
			t.Errorf("unexpected capture %q", m.Capture)
		}
		got[m.Text] = m.Line
	}
	// function_declaration excludes methods (method_declaration), so Gamma is absent.
	for _, want := range []string{"Alpha", "beta"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing function %q in %v", want, got)
		}
	}
	if _, ok := got["Gamma"]; ok {
		t.Errorf("method Gamma should not match function_declaration")
	}
	if got["Alpha"] != 3 {
		t.Errorf("Alpha expected line 3, got %d", got["Alpha"])
	}
}

func TestASTQuery_MethodWithReceiver(t *testing.T) {
	src := []byte("package p\ntype T struct{}\nfunc (t T) Gamma() {}\n")
	out := scanOne(t, "go", `(method_declaration name: (field_identifier) @m)`, "p.go", src, 50)
	if len(out) != 1 || out[0].Text != "Gamma" {
		t.Fatalf("expected one method Gamma, got %+v", out)
	}
}

func TestASTQuery_LimitIsRespected(t *testing.T) {
	src := []byte("package p\nfunc A(){}\nfunc B(){}\nfunc C(){}\n")
	out := scanOne(t, "go", `(function_declaration name: (identifier) @fn)`, "p.go", src, 2)
	if len(out) != 2 {
		t.Fatalf("limit not respected: got %d want 2", len(out))
	}
}

func TestASTQuery_ScannerReusableAcrossFiles(t *testing.T) {
	s, err := NewASTScanner("go", `(function_declaration name: (identifier) @fn)`)
	if err != nil {
		t.Fatalf("new scanner: %v", err)
	}
	defer s.Close()
	var out []ASTMatch
	if err := s.ScanFile(context.Background(), "a.go", []byte("package p\nfunc A(){}\n"), &out, 50); err != nil {
		t.Fatalf("scan a: %v", err)
	}
	if err := s.ScanFile(context.Background(), "b.go", []byte("package p\nfunc B(){}\n"), &out, 50); err != nil {
		t.Fatalf("scan b: %v", err)
	}
	if len(out) != 2 || out[0].Text != "A" || out[1].Text != "B" {
		t.Fatalf("reuse across files broken: %+v", out)
	}
	if out[0].Path != "a.go" || out[1].Path != "b.go" {
		t.Fatalf("paths not threaded through: %+v", out)
	}
}

func TestNewASTScanner_Errors(t *testing.T) {
	if _, err := NewASTScanner("cobol", `(x)`); err == nil {
		t.Error("expected error for unsupported language")
	}
	if _, err := NewASTScanner("go", `(this is not valid`); err == nil {
		t.Error("expected error for malformed pattern")
	}
}

// A malformed #match? regex makes the tree-sitter binding call
// regexp.MustCompile, which panics. ScanFile must recover and return an error
// rather than letting the panic escape and crash the server.
func TestASTQuery_BadMatchRegexDoesNotPanic(t *testing.T) {
	pat := `((function_declaration name: (identifier) @n) (#match? @n "["))`
	s, err := NewASTScanner("go", pat)
	if err != nil {
		t.Fatalf("compile (predicate syntax is valid): %v", err)
	}
	defer s.Close()
	var out []ASTMatch
	err = s.ScanFile(context.Background(), "p.go", []byte("package p\nfunc Alpha(){}\n"), &out, 50)
	if err == nil {
		t.Fatalf("expected a recovered error from the malformed regex, got nil")
	}
}

func TestScanFiles_ParallelMatchesSerialAndSorts(t *testing.T) {
	src := map[string][]byte{}
	rels := []string{}
	for i := 0; i < 200; i++ {
		rel := fmt.Sprintf("pkg/f%03d.go", i)
		src[rel] = []byte(fmt.Sprintf("package p\nfunc Fn%d() error { return nil }\n", i))
		rels = append(rels, rel)
	}
	read := func(rel string) ([]byte, error) { return src[rel], nil }
	pat := `(function_declaration name: (identifier) @n)`

	for _, workers := range []int{1, 4, 32} {
		res, err := ScanFiles(context.Background(), "go", pat, rels, read, 1000, workers)
		if err != nil {
			t.Fatalf("w=%d: %v", workers, err)
		}
		if len(res.Matches) != 200 {
			t.Errorf("w=%d: expected 200 matches, got %d", workers, len(res.Matches))
		}
		if res.Scanned != 200 {
			t.Errorf("w=%d: expected 200 scanned, got %d", workers, res.Scanned)
		}
		// Sorted by path:line regardless of worker completion order.
		for i := 1; i < len(res.Matches); i++ {
			if res.Matches[i-1].Path > res.Matches[i].Path {
				t.Fatalf("w=%d: results not sorted at %d", workers, i)
			}
		}
	}
}

func TestScanFiles_EarlyExitAtLimit(t *testing.T) {
	rels := []string{}
	src := map[string][]byte{}
	for i := 0; i < 500; i++ {
		rel := fmt.Sprintf("f%03d.go", i)
		src[rel] = []byte("package p\nfunc A() {}\n")
		rels = append(rels, rel)
	}
	read := func(rel string) ([]byte, error) { return src[rel], nil }
	res, err := ScanFiles(context.Background(), "go", `(function_declaration name: (identifier) @n)`, rels, read, 10, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) < 10 {
		t.Errorf("expected at least the limit of 10 matches, got %d", len(res.Matches))
	}
	// Early exit means we should NOT have scanned anywhere near all 500 files.
	if res.Scanned >= 500 {
		t.Errorf("early-exit failed: scanned all %d files", res.Scanned)
	}
}

func TestScanFiles_BadPredicateRegexErrorsNotPanics(t *testing.T) {
	rels := []string{"a.go", "b.go", "c.go"}
	read := func(rel string) ([]byte, error) { return []byte("package p\nfunc A() {}\n"), nil }
	pat := `((function_declaration name: (identifier) @n) (#match? @n "(("))`
	_, err := ScanFiles(context.Background(), "go", pat, rels, read, 100, 4)
	if err == nil {
		t.Fatal("expected an error from the malformed predicate regex")
	}
}

func TestScanFiles_BadPatternValidatedUpFront(t *testing.T) {
	_, err := ScanFiles(context.Background(), "go", `(unbalanced`, []string{"a.go"}, func(string) ([]byte, error) { return nil, nil }, 10, 4)
	if err == nil {
		t.Fatal("expected a compile error for an invalid pattern")
	}
}

func TestASTQuery_AliasesResolve(t *testing.T) {
	for _, alias := range []string{"golang", "py", "ts", "js", "rs"} {
		if got := canonicalLang(alias); got == "" {
			t.Errorf("alias %q did not resolve", alias)
		}
		if ExtensionsForASTLanguage(alias) == nil {
			t.Errorf("alias %q has no extensions", alias)
		}
	}
}

func TestGoDocComment_IndexedForNLSearch(t *testing.T) {
	src := []byte("package p\n\n// RRF merges two ranked lists by reciprocal rank fusion.\nfunc RRF() {}\n\nfunc Bare() {}\n\n// leading line\n\n// gapped comment (blank line above) should be ignored\nfunc Gapped() {}\n")
	res, err := ParseGo(context.Background(), "r", "h.go", src)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range res.Symbols {
		got[s.Name] = s.Signature
	}
	if !strings.Contains(got["RRF"], "reciprocal rank fusion") {
		t.Errorf("RRF doc comment not captured into Signature: %q", got["RRF"])
	}
	if got["Bare"] != "" {
		t.Errorf("function with no doc should have empty Signature, got %q", got["Bare"])
	}
	if strings.Contains(got["Gapped"], "leading line") {
		t.Errorf("comment separated by a blank line must not attach: %q", got["Gapped"])
	}
}
