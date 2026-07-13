package parser

import (
	"context"
	"strings"
	"testing"
)

// callTargets returns the symref names of every `calls` edge in the result.
func callTargets(res *ParseResult) []string {
	var out []string
	for _, e := range res.Edges {
		if e.Kind == "calls" {
			if i := strings.LastIndexByte(e.TargetID, ':'); i >= 0 {
				// symref:repo:path:Name -> trailing segment is the (possibly
				// dotted) call name.
				out = append(out, e.TargetID[i+1:])
			}
		}
	}
	return out
}

func hasTarget(res *ParseResult, name string) bool {
	for _, t := range callTargets(res) {
		if t == name {
			return true
		}
	}
	return false
}

// TestGoConstructorReturnTypeQualifiesCall verifies that a variable bound from a
// same-file constructor (`s := NewStore()`) is typed, so its method call is
// emitted as the type-qualified `Store.Get` symref rather than a bare `Get` —
// which is what lets the resolver disambiguate it via the recv_type strategy.
func TestGoConstructorReturnTypeQualifiesCall(t *testing.T) {
	ctx := context.Background()
	src := []byte(`package p

type Store struct{}

func NewStore() *Store { return &Store{} }

func (s *Store) Get() {}

func run() {
	s := NewStore()
	s.Get()
}
`)
	res, err := ParseGo(ctx, "repo", "p.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !hasTarget(res, "Store.Get") {
		t.Fatalf("expected type-qualified call Store.Get, got %v", callTargets(res))
	}
	if hasTarget(res, "Get") {
		t.Errorf("call should not be emitted as a bare Get, got %v", callTargets(res))
	}
}

// TestGoMultiValueReturnTypesVariable verifies the `s, err := New()` form types
// `s` from the first result, so `s.Get()` is still qualified.
func TestGoMultiValueReturnTypesVariable(t *testing.T) {
	ctx := context.Background()
	src := []byte(`package p

type Store struct{}

func OpenStore() (*Store, error) { return &Store{}, nil }

func (s *Store) Get() {}

func run() {
	s, err := OpenStore()
	_ = err
	s.Get()
}
`)
	res, err := ParseGo(ctx, "repo", "p.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if !hasTarget(res, "Store.Get") {
		t.Fatalf("expected Store.Get from multi-value return, got %v", callTargets(res))
	}
}
