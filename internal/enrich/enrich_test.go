package enrich

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// fakeChat returns canned output and counts calls, so the suite never depends on a
// running model (project rule: never depend on Ollama for tests).
type fakeChat struct {
	reply string
	err   error
	calls int
}

func (f *fakeChat) Model() string { return "fake-7b" }
func (f *fakeChat) Complete(_ context.Context, _, _ string) (string, error) {
	f.calls++
	return f.reply, f.err
}

func sym(id, name, sig string) types.Symbol {
	return types.Symbol{ID: id, Name: name, Signature: sig, Kind: types.SymbolKindFunction, Language: "go"}
}

func TestParseEnrichment_FiltersAliasesInName(t *testing.T) {
	// "login" is a substring of the name → must be dropped (adds no lexical recall);
	// "auth"/"signin" are genuine vocabulary bridges → kept.
	raw := `{"purpose":"Authenticates a user and starts a session.","aliases":["LOGIN","auth","signin","auth"]}`
	e, err := parseEnrichment(raw, "loginUser")
	if err != nil {
		t.Fatal(err)
	}
	if e.Purpose == "" {
		t.Error("purpose should be parsed")
	}
	for _, a := range e.Aliases {
		if a == "login" {
			t.Error("alias 'login' is a substring of the name and must be filtered")
		}
	}
	if len(e.Aliases) != 2 { // auth, signin (dup auth removed)
		t.Errorf("expected 2 deduped aliases, got %v", e.Aliases)
	}
}

func TestParseEnrichment_ToleratesProseAndFence(t *testing.T) {
	raw := "Sure! Here is the JSON:\n```json\n{\"purpose\":\"Closes the store.\",\"aliases\":[\"shutdown\"]}\n```\nHope that helps."
	e, err := parseEnrichment(raw, "Close")
	if err != nil {
		t.Fatalf("should extract JSON from wrapped output: %v", err)
	}
	if e.Purpose != "Closes the store." || len(e.Aliases) != 1 || e.Aliases[0] != "shutdown" {
		t.Errorf("unexpected parse: %+v", e)
	}
}

func TestEnrich_StampsHashAndModel(t *testing.T) {
	chat := &fakeChat{reply: `{"purpose":"Does a thing.","aliases":["widget"]}`}
	g := Generator{Chat: chat}
	s := sym("sym:1", "DoThing", "func DoThing() error")
	e, err := g.Enrich(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if e.SymbolID != "sym:1" || e.Model != "fake-7b" {
		t.Errorf("id/model not stamped: %+v", e)
	}
	if e.Hash != ContentHash(s) {
		t.Errorf("hash mismatch: %q vs %q", e.Hash, ContentHash(s))
	}
	if got := e.SearchText(); got != "Does a thing. widget" {
		t.Errorf("SearchText = %q", got)
	}
}

func TestEnrichBatch_CachesUnchangedSymbols(t *testing.T) {
	chat := &fakeChat{reply: `{"purpose":"p","aliases":["x"]}`}
	g := Generator{Chat: chat}
	store, err := OpenStore(filepath.Join(t.TempDir(), "enrichment.json"))
	if err != nil {
		t.Fatal(err)
	}
	syms := []types.Symbol{sym("a", "A", "func A()"), sym("b", "B", "func B()")}

	r1, err := EnrichBatch(context.Background(), g, syms, store)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Generated != 2 || r1.Cached != 0 {
		t.Fatalf("first run should generate 2, got %+v", r1)
	}

	// Re-run with identical surfaces → all cached, zero new model calls.
	callsBefore := chat.calls
	r2, err := EnrichBatch(context.Background(), g, syms, store)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Cached != 2 || r2.Generated != 0 {
		t.Errorf("second run should be fully cached, got %+v", r2)
	}
	if chat.calls != callsBefore {
		t.Errorf("cached run must make no model calls: %d → %d", callsBefore, chat.calls)
	}

	// Changing a symbol's signature changes its hash → it (only) regenerates.
	syms[0].Signature = "func A(x int)"
	r3, err := EnrichBatch(context.Background(), g, syms, store)
	if err != nil {
		t.Fatal(err)
	}
	if r3.Generated != 1 || r3.Cached != 1 {
		t.Errorf("only the changed symbol should regenerate, got %+v", r3)
	}
}

func TestEnrichBatch_BestEffortOnModelError(t *testing.T) {
	chat := &fakeChat{err: context.DeadlineExceeded}
	g := Generator{Chat: chat}
	store, _ := OpenStore(filepath.Join(t.TempDir(), "e.json"))
	r, err := EnrichBatch(context.Background(), g, []types.Symbol{sym("a", "A", "")}, store)
	if err != nil {
		t.Fatalf("batch must not fail on a per-symbol model error: %v", err)
	}
	if r.Failed != 1 || r.Generated != 0 {
		t.Errorf("model error should count as Failed, not block: %+v", r)
	}
}

func TestStore_RoundTripsAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enrichment.json")
	s1, _ := OpenStore(path)
	s1.Put(Enrichment{SymbolID: "x", Hash: "h", Purpose: "p", Aliases: []string{"a"}})
	if err := s1.Flush(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Get("x")
	if !ok || got.Purpose != "p" || got.Hash != "h" {
		t.Errorf("did not persist/reload: %+v ok=%v", got, ok)
	}
}
