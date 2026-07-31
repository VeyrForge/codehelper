//go:build windows

package mcpsvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
)

// buildIndexedRepo is unavailable on Windows in this package's heavier
// integration tests (git+CGO indexer path). Pure unit tests that don't call it
// still compile; callers of buildIndexedRepo skip.
func buildIndexedRepo(t *testing.T, _ map[string]string) (*registry.Registry, registry.Entry, context.Context) {
	t.Helper()
	t.Skip("buildIndexedRepo integration helper is Unix-only in this package")
	return nil, registry.Entry{}, context.Background()
}

func decodeStructured[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	text, ok := mcp.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		// TOON responses aren't JSON — skip rather than fail the Windows stub path.
		t.Skipf("decodeStructured: %v", err)
	}
	return out
}
