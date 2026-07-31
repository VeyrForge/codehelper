package toon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalScalarsAndObject(t *testing.T) {
	in := map[string]any{"name": "alpha", "count": 3, "ok": true, "note": ""}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// Keys are sorted by json.Marshal for maps; check content.
	for _, want := range []string{"name: alpha", "count: 3", "ok: true", `note: ""`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMarshalTabularArray(t *testing.T) {
	type row struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	in := struct {
		Hits []row `json:"hits"`
	}{Hits: []row{{1, "Foo", "func"}, {2, "Bar", "method"}}}

	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hits[2]{id,name,kind}:") {
		t.Errorf("expected tabular header, got:\n%s", out)
	}
	if !strings.Contains(out, "1,Foo,func") || !strings.Contains(out, "2,Bar,method") {
		t.Errorf("expected CSV rows, got:\n%s", out)
	}
	// Struct field order must be preserved (id,name,kind), not alphabetized.
	if strings.Index(out, "id,name,kind") < 0 {
		t.Errorf("field order not preserved:\n%s", out)
	}
}

func TestMarshalInlineScalarArray(t *testing.T) {
	in := map[string]any{"tags": []any{"a", "b", "c"}}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tags[3]: a,b,c") {
		t.Errorf("expected inline list, got:\n%s", out)
	}
}

func TestQuotingRules(t *testing.T) {
	in := map[string]any{
		"a": "has, comma",
		"b": "true",  // would be mistaken for bool
		"c": "123",   // would be mistaken for number
		"d": "plain", // bare
		"e": "with\nnewline",
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`a: "has, comma"`, `b: "true"`, `c: "123"`, "d: plain"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `e: "with\nnewline"`) {
		t.Errorf("newline not escaped:\n%s", out)
	}
}

func TestNonUniformArrayFallback(t *testing.T) {
	// Rows with a nested object cannot be tabular; must fall back to list form.
	in := map[string]any{
		"items": []any{
			map[string]any{"id": 1, "meta": map[string]any{"x": 1}},
			map[string]any{"id": 2, "meta": map[string]any{"x": 2}},
		},
	}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "items[2]{") {
		t.Errorf("should not be tabular (nested object cell):\n%s", out)
	}
	if !strings.Contains(out, "items[2]:") || !strings.Contains(out, "- id: 1") {
		t.Errorf("expected list fallback, got:\n%s", out)
	}
}

func TestEmptyArray(t *testing.T) {
	in := map[string]any{"xs": []any{}}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "xs[0]:") {
		t.Errorf("expected empty array marker, got:\n%s", out)
	}
}

func TestNestedObject(t *testing.T) {
	in := map[string]any{"outer": map[string]any{"inner": "v", "n": 5}}
	out, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "outer:\n") || !strings.Contains(out, "  inner: v") {
		t.Errorf("expected nested indentation, got:\n%s", out)
	}
}

// Token-savings sanity: TOON of a realistic tabular payload should be shorter
// than its JSON.
func TestTokenSavings(t *testing.T) {
	type hit struct {
		ID    int     `json:"id"`
		Name  string  `json:"name"`
		Path  string  `json:"path"`
		Score float64 `json:"score"`
	}
	var hits []hit
	for i := 0; i < 25; i++ {
		hits = append(hits, hit{i, "Symbol", "internal/pkg/file.go", 0.5})
	}
	payload := map[string]any{"hits": hits}
	toonOut, _ := Marshal(payload)
	jsonLen := len(mustJSON(t, payload))
	if len(toonOut) >= jsonLen {
		t.Errorf("TOON (%d) not smaller than JSON (%d)", len(toonOut), jsonLen)
	}
	t.Logf("JSON=%d bytes TOON=%d bytes (%.0f%% smaller)", jsonLen, len(toonOut),
		100*(1-float64(len(toonOut))/float64(jsonLen)))
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
