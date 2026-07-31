package registry

import (
	"sort"
	"strings"
)

// CrossRepoEdge is an import→owner hint used by cross-query and merged group
// snapshots. It is not a full merged call-graph edge in sqlite.
type CrossRepoEdge struct {
	ImportPath string `json:"import_path"`
	OwnerName  string `json:"owner_name"`
	OwnerRoot  string `json:"owner_root"`
	ViaRoot    string `json:"via_root"`
	SameGroup  bool   `json:"same_group"`
}

// ResolveCrossRepoEdges maps import paths to registry owners, optionally
// preferring siblings in the same workspace group as fromRepo.
func (r *Registry) ResolveCrossRepoEdges(fromRepo string, importPaths []string) []CrossRepoEdge {
	if r == nil || len(importPaths) == 0 {
		return nil
	}
	fromRepo = strings.TrimSpace(fromRepo)
	sib := map[string]struct{}{}
	if fromRepo != "" {
		for _, e := range r.SiblingEntries(fromRepo) {
			sib[e.Name] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	var out []CrossRepoEdge
	for _, raw := range importPaths {
		imp := strings.TrimSpace(raw)
		if imp == "" {
			continue
		}
		owners := r.ResolveImportOwners(imp)
		for _, o := range owners {
			if o.Name == fromRepo {
				continue
			}
			key := imp + "\x00" + o.Name
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			via := ""
			roots := o.ImportRoots
			if len(roots) == 0 {
				roots = []string{o.Name}
			}
			lower := strings.ToLower(imp)
			for _, root := range roots {
				root = strings.TrimSpace(root)
				if root != "" && strings.HasPrefix(lower, strings.ToLower(root)) {
					via = root
					break
				}
			}
			_, same := sib[o.Name]
			out = append(out, CrossRepoEdge{
				ImportPath: imp,
				OwnerName:  o.Name,
				OwnerRoot:  o.RootPath,
				ViaRoot:    via,
				SameGroup:  same,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ImportPath != out[j].ImportPath {
			return out[i].ImportPath < out[j].ImportPath
		}
		return out[i].OwnerName < out[j].OwnerName
	})
	return out
}
