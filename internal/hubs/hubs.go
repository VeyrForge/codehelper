// Package hubs precomputes and stores the call-graph "what's linked" summary at
// index time — the most-referenced symbols (symbol hubs) and the most-depended-on
// packages (package hubs) — so project_context reads a small JSON file instead of
// scanning the graph on every request. It is a derived context artifact, like the
// architecture summary and project profile.
package hubs

import (
	"context"
	"encoding/json"
	"os"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/paths"
)

const (
	symbolHubLimit  = 8
	packageHubLimit = 6
)

// Data is the persisted hubs artifact.
type Data struct {
	RepoID      string             `json:"repo_id"`
	SymbolHubs  []graph.Hub        `json:"symbol_hubs,omitempty"`
	PackageHubs []graph.PackageHub `json:"package_hubs,omitempty"`
}

// Write computes the hubs from the (fully built, symref-resolved) graph and
// persists them. Best-effort at the call site — indexing must not fail on this.
func Write(ctx context.Context, indexRoot, repoID string, st *graph.Store) error {
	d := Data{RepoID: repoID}
	if h, err := st.TopHubs(ctx, repoID, symbolHubLimit); err == nil {
		d.SymbolHubs = h
	}
	if p, err := st.TopPackages(ctx, repoID, packageHubLimit); err == nil {
		d.PackageHubs = p
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(paths.HubsPath(indexRoot), b, 0o644)
}

// Read loads the precomputed hubs, or an error if absent/unreadable (the caller
// falls back to computing at runtime for indexes built before this artifact).
func Read(indexRoot string) (*Data, error) {
	b, err := os.ReadFile(paths.HubsPath(indexRoot))
	if err != nil {
		return nil, err
	}
	var d Data
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
