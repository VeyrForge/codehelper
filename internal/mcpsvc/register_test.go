package mcpsvc

import (
	"encoding/json"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestMarshalQueryToolResponse_HitsAlwaysArray(t *testing.T) {
	b, err := marshalQueryToolResponse(queryToolResponse{})
	if err != nil {
		t.Fatalf("marshalQueryToolResponse returned error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	hits, ok := out["hits"].([]any)
	if !ok {
		t.Fatalf("hits is not a JSON array: %T", out["hits"])
	}
	if len(hits) != 0 {
		t.Fatalf("expected empty hits array, got %d entries", len(hits))
	}
}

func TestMarshalQueryToolResponse_PreservesAdditiveFields(t *testing.T) {
	b, err := marshalQueryToolResponse(queryToolResponse{
		Intent: "debug",
	})
	if err != nil {
		t.Fatalf("marshalQueryToolResponse returned error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	if out["intent"] != "debug" {
		t.Fatalf("expected intent=debug, got %#v", out["intent"])
	}
	if _, ok := out["hits"].([]any); !ok {
		t.Fatalf("hits must remain an array for compatibility")
	}
}

func TestResolveCrossRepoCandidates_SortedUnique(t *testing.T) {
	reg := &registry.Registry{
		Entries: map[string]registry.Entry{
			"zeta":  {Name: "zeta", ImportRoots: []string{"github.com/acme/zeta"}},
			"alpha": {Name: "alpha", ImportRoots: []string{"github.com/acme/alpha"}},
		},
	}
	got := resolveCrossRepoCandidates(reg, "github.com/acme/zeta/pkg github.com/acme/alpha/lib")
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("expected sorted names [alpha,zeta], got [%s,%s]", got[0].Name, got[1].Name)
	}
}

func TestFilterImpactNodesExcludeTests(t *testing.T) {
	in := []types.ImpactNode{
		{Path: "internal/mcpsvc/register.go", Name: "register"},
		{Path: "internal/mcpsvc/register_test.go", Name: "register_test"},
		{Path: "pkg/foo/bar_spec.go", Name: "bar_spec"},
	}
	got := filterImpactNodesExcludeTests(in)
	if len(got) != 1 {
		t.Fatalf("expected 1 non-test node, got %d", len(got))
	}
	if got[0].Path != "internal/mcpsvc/register.go" {
		t.Fatalf("unexpected node retained: %s", got[0].Path)
	}
}

func TestLikelyPublicSymbol(t *testing.T) {
	if !likelyPublicSymbol(types.Symbol{Name: "ExportedThing", Path: "internal/x.go"}) {
		t.Fatalf("expected exported symbol to be considered public")
	}
	if !likelyPublicSymbol(types.Symbol{Name: "thing", Path: "pkg/api/thing.go"}) {
		t.Fatalf("expected pkg path symbol to be considered public")
	}
	if likelyPublicSymbol(types.Symbol{Name: "thing", Path: "internal/x.go"}) {
		t.Fatalf("unexpected internal lowercase symbol considered public")
	}
}
