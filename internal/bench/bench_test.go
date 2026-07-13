package bench

import "testing"

func TestScoreFiles(t *testing.T) {
	// GT: a,b,c ; Got: a,b,d  -> TP=2(a,b) FP=1(d) FN=1(c)
	m := scoreFiles("X", []string{"a", "b", "c"}, []string{"a", "b", "d"})
	if m.TP != 2 || m.FP != 1 || m.FN != 1 {
		t.Fatalf("counts wrong: %+v", m)
	}
	if !approx(m.Precision, 2.0/3.0) || !approx(m.Recall, 2.0/3.0) {
		t.Errorf("p/r wrong: %.3f %.3f", m.Precision, m.Recall)
	}

	// Perfect match.
	pm := scoreFiles("Y", []string{"a", "b"}, []string{"b", "a"})
	if !approx(pm.Precision, 1) || !approx(pm.Recall, 1) || !approx(pm.F1, 1) {
		t.Errorf("perfect match wrong: %+v", pm)
	}

	// No ground truth and no result -> zeros (handled as agreement upstream).
	em := scoreFiles("Z", nil, nil)
	if em.Precision != 0 || em.Recall != 0 || em.TP != 0 {
		t.Errorf("empty case wrong: %+v", em)
	}
}

func TestPathOfSymbolID(t *testing.T) {
	if got := pathOfSymbolID("sym:repo:internal/x/y.go:42:Foo"); got != "internal/x/y.go" {
		t.Errorf("path=%q", got)
	}
	if got := pathOfSymbolID("symref:repo:a.go:Foo"); got != "" {
		t.Errorf("symref should yield empty path, got %q", got)
	}
}

func TestPercentile(t *testing.T) {
	v := []float64{10, 20, 30, 40, 50}
	if p := percentile(v, 50); p != 30 {
		t.Errorf("p50=%v want 30", p)
	}
	if p := percentile(nil, 50); p != 0 {
		t.Errorf("empty p50=%v want 0", p)
	}
}

func TestIsDeclarationLine(t *testing.T) {
	// Fictional symbol names (not present in this repo) so these fixtures never
	// pollute the textual ground truth when `bench` is run on codehelper itself.
	decls := []struct{ line, name string }{
		{"func (f *fnExtractor) Quizzaciously() Quizzaciously { return f.q }", "Quizzaciously"}, // keyword def
		{"\tQuizzaciously() Quizzaciously", "Quizzaciously"},                                    // Go interface method decl
		{"\tFlumberize(ctx context.Context, id string) (*Wobble, error)", "Flumberize"},         // Go iface, multi-return
		{"  flibbertigibbet(): Wobble", "flibbertigibbet"},                                      // TS interface/abstract decl
		{"func Snorgleplex(ctx context.Context) error {", "Snorgleplex"},                        // plain func def
	}
	for _, d := range decls {
		if !isDeclarationLine(d.line, d.name) {
			t.Errorf("expected declaration for %q (name %q)", d.line, d.name)
		}
	}

	calls := []struct{ line, name string }{
		{"\tw, err := wobbler.Flumberize(ctx, st, id, name)", "Flumberize"}, // qualified call
		{"\tQuizzaciously()", "Quizzaciously"},                              // bare statement call
		{"\tx := Snorgleplex()", "Snorgleplex"},                             // assignment call
		{"\treturn Snorgleplex(ctx, st)", "Snorgleplex"},                    // return call
		{"\tQuizzaciously().Merge(other)", "Quizzaciously"},                 // chained call
		{"\tif Snorgleplex() {", "Snorgleplex"},                             // guard call
	}
	for _, c := range calls {
		if isDeclarationLine(c.line, c.name) {
			t.Errorf("expected call (not declaration) for %q (name %q)", c.line, c.name)
		}
	}
}

func approx(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
