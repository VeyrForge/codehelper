package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// MergedSnapshotFormatVersion bumps when the merged group snapshot shape changes.
const MergedSnapshotFormatVersion = 1

// CrossRepoEdgeSummary is a portable import→owner edge for merged snapshots.
// It mirrors registry.CrossRepoEdge without importing registry (graph stays leaf-ish).
type CrossRepoEdgeSummary struct {
	ImportPath string `json:"import_path"`
	FromRepo   string `json:"from_repo"`
	OwnerName  string `json:"owner_name"`
	OwnerRoot  string `json:"owner_root,omitempty"`
	ViaRoot    string `json:"via_root,omitempty"`
	SameGroup  bool   `json:"same_group"`
}

// MergedGroupSnapshot combines per-repo summary snapshots plus cross-repo import
// ownership edges for a workspace group. It is still summary-only — not a shared
// verified graph.db (see docs/GRAPH_SNAPSHOT.md).
type MergedGroupSnapshot struct {
	FormatVersion  int                    `json:"format_version"`
	GroupID        string                 `json:"group_id"`
	GroupName      string                 `json:"group_name,omitempty"`
	ExportedAt     string                 `json:"exported_at"`
	Members        []Snapshot             `json:"members"`
	CrossRepoEdges []CrossRepoEdgeSummary `json:"cross_repo_edges,omitempty"`
	Processes      []types.Process        `json:"processes,omitempty"`
	Clusters       []types.Cluster        `json:"clusters,omitempty"`
	Symbols        int                    `json:"symbols"`
	Edges          int                    `json:"edges"`
	Files          int                    `json:"files"`
	Note           string                 `json:"note,omitempty"`
}

// ResolveCrossRepoEdgeFunc maps (fromRepo, importPaths) → ownership edges.
// Callers typically pass registry.Registry.ResolveCrossRepoEdges adapted to
// CrossRepoEdgeSummary.
type ResolveCrossRepoEdgeFunc func(fromRepo string, importPaths []string) []CrossRepoEdgeSummary

// MergeGroupSnapshots builds a merged workspace-group snapshot from member
// snapshots and optional cross-repo edge resolution.
func MergeGroupSnapshots(groupID, groupName string, members []Snapshot, edges []CrossRepoEdgeSummary) MergedGroupSnapshot {
	groupID = strings.TrimSpace(groupID)
	out := MergedGroupSnapshot{
		FormatVersion:  MergedSnapshotFormatVersion,
		GroupID:        groupID,
		GroupName:      strings.TrimSpace(groupName),
		ExportedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Members:        append([]Snapshot(nil), members...),
		CrossRepoEdges: append([]CrossRepoEdgeSummary(nil), edges...),
		Note:           "merged group summary; not a shared verified graph.db",
	}
	var procs []types.Process
	var clusters []types.Cluster
	for _, m := range members {
		out.Symbols += m.Symbols
		out.Edges += m.Edges
		out.Files += m.Files
		for _, p := range m.Processes {
			cp := p
			if cp.RepoID == "" {
				cp.RepoID = m.RepoID
			}
			procs = append(procs, cp)
		}
		for _, c := range m.Clusters {
			cc := c
			if cc.RepoID == "" {
				cc.RepoID = m.RepoID
			}
			clusters = append(clusters, cc)
		}
	}
	sort.SliceStable(procs, func(i, j int) bool {
		if procs[i].RepoID != procs[j].RepoID {
			return procs[i].RepoID < procs[j].RepoID
		}
		return procs[i].Name < procs[j].Name
	})
	sort.SliceStable(clusters, func(i, j int) bool {
		if clusters[i].RepoID != clusters[j].RepoID {
			return clusters[i].RepoID < clusters[j].RepoID
		}
		return clusters[i].Name < clusters[j].Name
	})
	sort.SliceStable(out.CrossRepoEdges, func(i, j int) bool {
		if out.CrossRepoEdges[i].FromRepo != out.CrossRepoEdges[j].FromRepo {
			return out.CrossRepoEdges[i].FromRepo < out.CrossRepoEdges[j].FromRepo
		}
		if out.CrossRepoEdges[i].ImportPath != out.CrossRepoEdges[j].ImportPath {
			return out.CrossRepoEdges[i].ImportPath < out.CrossRepoEdges[j].ImportPath
		}
		return out.CrossRepoEdges[i].OwnerName < out.CrossRepoEdges[j].OwnerName
	})
	out.Processes = procs
	out.Clusters = clusters
	return out
}

// DistinctImportModulePaths returns unique import module strings from edges
// (mod:<repo>:<path> destinations), for cross-repo owner resolution.
func (s *Store) DistinctImportModulePaths(ctx context.Context, repoID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 2000
	}
	prefix := "mod:" + repoID + ":"
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT dst_id FROM edges
WHERE repo_id=? AND kind=? AND dst_id LIKE ?
ORDER BY dst_id
LIMIT ?`, repoID, string(types.RefKindImports), prefix+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var dst string
		if err := rows.Scan(&dst); err != nil {
			return nil, err
		}
		mod := strings.TrimPrefix(dst, prefix)
		mod = strings.TrimSpace(mod)
		if mod == "" || strings.HasPrefix(mod, ".") || strings.HasPrefix(mod, "/") {
			continue
		}
		out = append(out, mod)
	}
	return out, rows.Err()
}

// BuildMergedGroupSnapshot opens each member store path, builds per-repo
// snapshots, resolves cross-repo import owners, and merges them.
func BuildMergedGroupSnapshot(
	ctx context.Context,
	groupID, groupName string,
	memberSnaps []Snapshot,
	memberImportPaths map[string][]string,
	resolve ResolveCrossRepoEdgeFunc,
) MergedGroupSnapshot {
	var edges []CrossRepoEdgeSummary
	seen := map[string]struct{}{}
	if resolve != nil {
		repos := make([]string, 0, len(memberImportPaths))
		for repo := range memberImportPaths {
			repos = append(repos, repo)
		}
		sort.Strings(repos)
		for _, repo := range repos {
			imps := memberImportPaths[repo]
			for _, e := range resolve(repo, imps) {
				key := e.FromRepo + "\x00" + e.ImportPath + "\x00" + e.OwnerName
				if e.FromRepo == "" {
					e.FromRepo = repo
					key = e.FromRepo + "\x00" + e.ImportPath + "\x00" + e.OwnerName
				}
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				edges = append(edges, e)
			}
		}
	}
	return MergeGroupSnapshots(groupID, groupName, memberSnaps, edges)
}

// WriteMergedSnapshotJSON writes snap to path (creates parent dirs).
func WriteMergedSnapshotJSON(path string, snap MergedGroupSnapshot) error {
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

// AdaptRegistryEdges converts registry-shaped edges into CrossRepoEdgeSummary.
func AdaptRegistryEdges(fromRepo string, importPath, ownerName, ownerRoot, viaRoot string, sameGroup bool) CrossRepoEdgeSummary {
	return CrossRepoEdgeSummary{
		ImportPath: importPath,
		FromRepo:   fromRepo,
		OwnerName:  ownerName,
		OwnerRoot:  ownerRoot,
		ViaRoot:    viaRoot,
		SameGroup:  sameGroup,
	}
}

// ErrMergedEmpty is returned when a group has no buildable member snapshots.
var ErrMergedEmpty = fmt.Errorf("no member snapshots to merge")
