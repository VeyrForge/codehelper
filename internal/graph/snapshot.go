package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// SnapshotFormatVersion bumps when the portable snapshot JSON shape changes.
const SnapshotFormatVersion = 1

// Snapshot is a team-shareable, read-only summary of one repo's graph index.
// It does not replace graph.db — full DB transfer / verified shared indexes are
// deferred (see docs/GRAPH_SNAPSHOT.md).
type Snapshot struct {
	FormatVersion int             `json:"format_version"`
	RepoID        string          `json:"repo_id"`
	ExportedAt    string          `json:"exported_at"`
	SchemaVersion int             `json:"schema_version"`
	Symbols       int             `json:"symbols"`
	Edges         int             `json:"edges"`
	Files         int             `json:"files"`
	ImportRoots   []string        `json:"import_roots,omitempty"`
	GroupIDs      []string        `json:"group_ids,omitempty"`
	Processes     []types.Process `json:"processes,omitempty"`
	Clusters      []types.Cluster `json:"clusters,omitempty"`
	Note          string          `json:"note,omitempty"`
}

// ExportOptions controls Snapshot contents.
type ExportOptions struct {
	RepoID        string
	SchemaVersion int
	ImportRoots   []string
	GroupIDs      []string
	IncludeProcs  bool
	IncludeClus   bool
}

// BuildSnapshot collects portable graph summary fields from an open Store.
func BuildSnapshot(ctx context.Context, st *Store, opts ExportOptions) (Snapshot, error) {
	if st == nil {
		return Snapshot{}, fmt.Errorf("graph store is nil")
	}
	repoID := strings.TrimSpace(opts.RepoID)
	if repoID == "" {
		return Snapshot{}, fmt.Errorf("repo_id is required")
	}
	syms, edges, files, err := st.Counts(ctx, repoID)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		FormatVersion: SnapshotFormatVersion,
		RepoID:        repoID,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion: opts.SchemaVersion,
		Symbols:       syms,
		Edges:         edges,
		Files:         files,
		ImportRoots:   append([]string(nil), opts.ImportRoots...),
		GroupIDs:      append([]string(nil), opts.GroupIDs...),
		Note:          "summary export only; includes request-flow processes/clusters when present; full graph.db sharing is deferred",
	}
	if opts.IncludeProcs {
		procs, err := st.ListProcesses(ctx, repoID)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Processes = procs
	}
	if opts.IncludeClus {
		clusters, err := st.ListClusters(ctx, repoID)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Clusters = clusters
	}
	return snap, nil
}

// WriteSnapshotJSON writes snap to path (creates parent dirs).
func WriteSnapshotJSON(path string, snap Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
