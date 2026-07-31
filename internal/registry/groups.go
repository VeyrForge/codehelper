package registry

import (
	"fmt"
	"sort"
	"strings"
)

// UpsertGroup creates or replaces a workspace group and syncs member GroupIDs.
func (r *Registry) UpsertGroup(g WorkspaceGroup) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := strings.TrimSpace(g.ID)
	if id == "" {
		return fmt.Errorf("workspace group id is required")
	}
	name := strings.TrimSpace(g.Name)
	if name == "" {
		name = id
	}
	members := uniqueNonEmpty(g.Members)
	sort.Strings(members)
	if r.Groups == nil {
		r.Groups = map[string]WorkspaceGroup{}
	}
	prev, hadPrev := r.Groups[id]
	g = WorkspaceGroup{
		ID:          id,
		Name:        name,
		Members:     members,
		Description: strings.TrimSpace(g.Description),
	}
	r.Groups[id] = g

	prevSet := map[string]struct{}{}
	if hadPrev {
		for _, m := range prev.Members {
			prevSet[m] = struct{}{}
		}
	}
	curSet := map[string]struct{}{}
	for _, m := range members {
		curSet[m] = struct{}{}
		r.addGroupIDLocked(m, id)
	}
	for m := range prevSet {
		if _, ok := curSet[m]; !ok {
			r.removeGroupIDLocked(m, id)
		}
	}
	return nil
}

// RemoveGroup deletes a workspace group and clears membership from entries.
func (r *Registry) RemoveGroup(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" || r.Groups == nil {
		return
	}
	g, ok := r.Groups[id]
	if !ok {
		return
	}
	for _, m := range g.Members {
		r.removeGroupIDLocked(m, id)
	}
	delete(r.Groups, id)
}

// GetGroup returns a workspace group by id.
func (r *Registry) GetGroup(id string) (WorkspaceGroup, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.Groups == nil {
		return WorkspaceGroup{}, false
	}
	g, ok := r.Groups[strings.TrimSpace(id)]
	return g, ok
}

// ListGroups returns all workspace groups sorted by id.
func (r *Registry) ListGroups() []WorkspaceGroup {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]WorkspaceGroup, 0, len(r.Groups))
	for _, g := range r.Groups {
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SiblingEntries returns other registry entries that share a workspace group with name.
func (r *Registry) SiblingEntries(name string) []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.Entries[name]
	if !ok || len(e.GroupIDs) == 0 {
		return nil
	}
	seen := map[string]struct{}{name: {}}
	var out []Entry
	for _, gid := range e.GroupIDs {
		g, ok := r.Groups[gid]
		if !ok {
			continue
		}
		for _, m := range g.Members {
			if _, dup := seen[m]; dup {
				continue
			}
			sib, ok := r.Entries[m]
			if !ok {
				continue
			}
			seen[m] = struct{}{}
			out = append(out, sib)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SharesWorkspaceGroup reports whether a and b are both members of at least one
// common workspace group (used by MCP to allow context repo=<sibling>).
func (r *Registry) SharesWorkspaceGroup(a, b string) bool {
	if r == nil {
		return false
	}
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" || a == b {
		return a != "" && a == b
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ea, oka := r.Entries[a]
	eb, okb := r.Entries[b]
	if !oka || !okb || len(ea.GroupIDs) == 0 || len(eb.GroupIDs) == 0 {
		return false
	}
	set := map[string]struct{}{}
	for _, g := range ea.GroupIDs {
		set[g] = struct{}{}
	}
	for _, g := range eb.GroupIDs {
		if _, ok := set[g]; ok {
			return true
		}
	}
	return false
}

func (r *Registry) addGroupIDLocked(repoName, groupID string) {
	if r.Entries == nil {
		return
	}
	e, ok := r.Entries[repoName]
	if !ok {
		return
	}
	for _, g := range e.GroupIDs {
		if g == groupID {
			return
		}
	}
	e.GroupIDs = append(e.GroupIDs, groupID)
	sort.Strings(e.GroupIDs)
	r.Entries[repoName] = e
}

func (r *Registry) removeGroupIDLocked(repoName, groupID string) {
	if r.Entries == nil {
		return
	}
	e, ok := r.Entries[repoName]
	if !ok {
		return
	}
	var kept []string
	for _, g := range e.GroupIDs {
		if g != groupID {
			kept = append(kept, g)
		}
	}
	e.GroupIDs = kept
	r.Entries[repoName] = e
}
