package registry

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/meta"
)

// ProjectSummary describes one registry entry and its index readiness.
type ProjectSummary struct {
	Name          string `json:"name"`
	RootPath      string `json:"root_path"`
	Initialized   bool   `json:"initialized"`
	IndexStatus   string `json:"index_status"` // fresh | stale | missing
	LastCommit    string `json:"last_commit,omitempty"`
	SchemaVersion int    `json:"schema_version,omitempty"`
}

// ListProjectSummaries returns all registered projects with init/freshness status.
func (r *Registry) ListProjectSummaries() []ProjectSummary {
	if r == nil {
		return nil
	}
	entries := r.List()
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	out := make([]ProjectSummary, 0, len(entries))
	for _, e := range entries {
		out = append(out, SummarizeEntry(e))
	}
	return out
}

// SummarizeEntry builds a summary for one registry entry.
func SummarizeEntry(e Entry) ProjectSummary {
	s := ProjectSummary{
		Name:          e.Name,
		RootPath:      filepath.Clean(e.RootPath),
		LastCommit:    e.LastCommit,
		SchemaVersion: e.SchemaVer,
		IndexStatus:   "missing",
	}
	m, err := meta.Read(e.RootPath)
	if err != nil || m == nil || m.SymbolCount == 0 {
		return s
	}
	s.Initialized = true
	fresh := freshness.Inspect(e.RootPath)
	if fresh.Stale {
		s.IndexStatus = "stale"
	} else {
		s.IndexStatus = "fresh"
	}
	return s
}

// InitStatus reports whether a directory has a usable index.
func InitStatus(rootPath string) (initialized bool, indexStatus string) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return false, "missing"
	}
	m, err := meta.Read(rootPath)
	if err != nil || m == nil || m.SymbolCount == 0 {
		return false, "missing"
	}
	fresh := freshness.Inspect(rootPath)
	if fresh.Stale {
		return true, "stale"
	}
	return true, "fresh"
}

// ErrNotInitialized is returned when tools require an indexed workspace.
var ErrNotInitialized = fmt.Errorf("project is not initialized")

// RequireInitialized returns an error when rootPath has no usable index.
func RequireInitialized(rootPath string) error {
	ok, status := InitStatus(rootPath)
	if ok {
		return nil
	}
	return fmt.Errorf("%w (index %s); run `codehelper init` in the project root", ErrNotInitialized, status)
}
