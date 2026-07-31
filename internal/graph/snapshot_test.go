package graph

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestBuildAndWriteSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	repoID := "demo"
	if err := st.UpsertProcess(ctx, types.Process{
		ID: "proc:demo:1", RepoID: repoID, Name: "flow:main.go", EntrySymbol: "sym:1", StepSymbols: []string{"sym:1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertCluster(ctx, types.Cluster{
		ID: "clu:demo:1", RepoID: repoID, Name: "internal", Members: []string{"sym:1", "sym:2"}, Cohesion: 0.5,
	}); err != nil {
		t.Fatal(err)
	}

	snap, err := BuildSnapshot(ctx, st, ExportOptions{
		RepoID:        repoID,
		SchemaVersion: 1,
		ImportRoots:   []string{"example.com/demo"},
		GroupIDs:      []string{"platform"},
		IncludeProcs:  true,
		IncludeClus:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.FormatVersion != SnapshotFormatVersion || snap.RepoID != repoID {
		t.Fatalf("snap=%#v", snap)
	}
	if len(snap.Processes) != 1 || len(snap.Clusters) != 1 {
		t.Fatalf("procs/clusters missing: %#v", snap)
	}

	out := filepath.Join(t.TempDir(), "out", "graph_snapshot.json")
	if err := WriteSnapshotJSON(out, snap); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var round Snapshot
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatal(err)
	}
	if round.RepoID != repoID || round.ImportRoots[0] != "example.com/demo" {
		t.Fatalf("roundtrip=%#v", round)
	}
}
