package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
)

func TestConceptPhraseBoosts_SingletonLock(t *testing.T) {
	dbPath := filepath.Join("..", "..", ".codehelper", "graph.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skip("no index")
	}
	st, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	opts := QueryOptions{
		QueryTokens:      strings.Fields("obtain a single instance lock"),
		CentralityWeight: DefaultCentralityWeight,
	}
	hits, err := QueryHybridWithOptions(ctx, st, "codehelper", "obtain a single instance lock", 5, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	top := hits[0]
	if top.Symbol.Name != "Acquire" || !strings.Contains(top.Symbol.Path, "internal/daemon/lock.go") {
		t.Fatalf("top=%s %s, want Acquire in lock.go", top.Symbol.Name, top.Symbol.Path)
	}
}

func TestConceptPhraseBoosts_TaskStoreFactory(t *testing.T) {
	dbPath := filepath.Join("..", "..", ".codehelper", "graph.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skip("no index")
	}
	st, err := graph.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	hits, err := QueryHybridWithOptions(ctx, st, "codehelper", "construct a new task store", 5, QueryOptions{
		QueryTokens:      strings.Fields("construct a new task store"),
		CentralityWeight: DefaultCentralityWeight,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	top := hits[0]
	if top.Symbol.Name != "New" || !strings.Contains(top.Symbol.Path, "internal/taskstore/") {
		t.Fatalf("top=%s %s, want New in taskstore", top.Symbol.Name, top.Symbol.Path)
	}
}
