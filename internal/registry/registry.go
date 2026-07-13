package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// Entry describes one indexed repository.
type Entry struct {
	Name        string    `json:"name"`
	RootPath    string    `json:"root_path"`
	LastCommit  string    `json:"last_commit,omitempty"`
	IndexedAt   time.Time `json:"indexed_at"`
	SchemaVer   int       `json:"schema_version"`
	ImportRoots []string  `json:"import_roots,omitempty"`
}

// Registry is the global ~/.codehelper/registry.json.
type Registry struct {
	mu      sync.RWMutex
	path    string
	Entries map[string]Entry `json:"repos"`
}

// Load reads registry from disk or returns empty.
func Load() (*Registry, error) {
	p, err := paths.RegistryFile()
	if err != nil {
		return nil, err
	}
	r := &Registry{path: p, Entries: map[string]Entry{}}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	var wrapper struct {
		Repos map[string]Entry `json:"repos"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}
	if wrapper.Repos != nil {
		r.Entries = wrapper.Repos
	}
	return r, nil
}

func (r *Registry) ensureDir() error {
	dir := filepath.Dir(r.path)
	return os.MkdirAll(dir, 0o755)
}

// Save persists registry.
func (r *Registry) Save() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureDir(); err != nil {
		return err
	}
	wrapper := struct {
		Repos map[string]Entry `json:"repos"`
	}{Repos: r.Entries}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// Upsert adds or updates a repo entry by name.
func (r *Registry) Upsert(name, root, commit string, schemaVer int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Entries == nil {
		r.Entries = map[string]Entry{}
	}
	r.Entries[name] = Entry{
		Name:        name,
		RootPath:    filepath.Clean(root),
		LastCommit:  commit,
		IndexedAt:   time.Now().UTC(),
		SchemaVer:   schemaVer,
		ImportRoots: []string{name},
	}
	return nil
}

// Remove deletes a repo entry by name; a no-op if absent. Callers must Save.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.Entries, name)
}

// Get returns entry by name.
func (r *Registry) Get(name string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.Entries[name]
	return e, ok
}

// List returns all entries.
func (r *Registry) List() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry, 0, len(r.Entries))
	for _, e := range r.Entries {
		out = append(out, e)
	}
	return out
}

// ResolveName picks repo name from param or the current workspace (never lists all repos).
func (r *Registry) ResolveName(repoParam string) (string, error) {
	return r.ResolveNameInWorkspace(repoParam, "")
}

// ResolveNameInWorkspace resolves repo for an explicit name or the workspace root path.
func (r *Registry) ResolveNameInWorkspace(repoParam, workspaceRoot string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if repoParam != "" {
		if _, ok := r.Entries[repoParam]; !ok {
			return "", os.ErrNotExist
		}
		return repoParam, nil
	}
	ws := strings.TrimSpace(workspaceRoot)
	if ws != "" {
		if e, ok := r.entryForWorkspaceLocked(ws); ok {
			return e.Name, nil
		}
	}
	if len(r.Entries) == 1 {
		for n := range r.Entries {
			return n, nil
		}
	}
	if len(r.Entries) == 0 {
		return "", os.ErrNotExist
	}
	return "", ErrAmbiguousRepo
}

// EntryForWorkspace returns the registry entry for the open project at workspaceRoot.
func (r *Registry) EntryForWorkspace(workspaceRoot string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entryForWorkspaceLocked(workspaceRoot)
}

func (r *Registry) entryForWorkspaceLocked(workspaceRoot string) (Entry, bool) {
	ws := cleanRegistryPath(workspaceRoot)
	if ws == "" || r.Entries == nil {
		return Entry{}, false
	}
	var exact Entry
	var exactOK bool
	var parent Entry
	var parentOK bool
	var parentLen int
	var children []Entry
	for _, e := range r.Entries {
		rp := cleanRegistryPath(e.RootPath)
		if rp == "" {
			continue
		}
		if rp == ws {
			exact = e
			exactOK = true
			break
		}
		if registryPathContains(rp, ws) {
			if !parentOK || len(rp) > parentLen {
				parent = e
				parentOK = true
				parentLen = len(rp)
			}
		}
		if registryPathContains(ws, rp) {
			children = append(children, e)
		}
	}
	if exactOK {
		return exact, true
	}
	if parentOK {
		return parent, true
	}
	if len(children) == 1 {
		return children[0], true
	}
	return Entry{}, false
}

func cleanRegistryPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func registryPathContains(base, sub string) bool {
	base = strings.TrimSuffix(base, string(filepath.Separator))
	sub = strings.TrimSuffix(sub, string(filepath.Separator))
	if base == sub {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(sub, base+sep)
}

// ErrAmbiguousRepo is returned when repo param is required.
var ErrAmbiguousRepo = errAmbiguous{}

type errAmbiguous struct{}

func (errAmbiguous) Error() string {
	return "multiple repos indexed on this machine; run from your project root or pass --repo"
}

// ResolveImportOwners returns registry entries that likely own an import path.
func (r *Registry) ResolveImportOwners(importPath string) []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	importPath = strings.TrimSpace(strings.ToLower(importPath))
	if importPath == "" {
		return nil
	}
	var out []Entry
	for _, e := range r.Entries {
		roots := e.ImportRoots
		if len(roots) == 0 {
			roots = []string{e.Name}
		}
		for _, root := range roots {
			root = strings.ToLower(strings.TrimSpace(root))
			if root == "" {
				continue
			}
			if strings.HasPrefix(importPath, root) {
				out = append(out, e)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
