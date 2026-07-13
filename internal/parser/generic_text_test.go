package parser

import (
	"context"
	"testing"
)

func TestExtract_UnknownExtensionFallsBackToGenericText(t *testing.T) {
	t.Parallel()
	src := []byte("function helper() {}\nclass Worker {}\n")
	res, err := Extract(context.Background(), "repo", "src/component.vue", src)
	if err != nil {
		t.Fatalf("extract fallback: %v", err)
	}
	if len(res.Symbols) < 2 {
		t.Fatalf("expected fallback symbols, got %d", len(res.Symbols))
	}
}
