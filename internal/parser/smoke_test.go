package parser

import (
	"context"
	"strings"
	"testing"
)

func TestExtractCFunction(t *testing.T) {
	ctx := context.Background()
	src := []byte("int main(void) { return 0; }\n")
	res, err := Extract(ctx, "repo", "x.c", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Symbols) == 0 {
		t.Fatal("expected symbols")
	}
	if !strings.Contains(res.Symbols[0].Name, "main") {
		t.Fatalf("unexpected %q", res.Symbols[0].Name)
	}
}
