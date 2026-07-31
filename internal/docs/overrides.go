package docs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// Override is a user- or project-supplied documentation source that takes
// precedence over the built-in curated index. It mirrors libEntry but is loaded
// from optional JSON files so the catalog is extensible without recompiling.
type Override struct {
	Match     []string `json:"match"`
	DocBase   string   `json:"doc_base"`
	LLMSTxt   string   `json:"llms_txt,omitempty"`
	LLMSFull  string   `json:"llms_full,omitempty"`
	Trust     int      `json:"trust,omitempty"`
	Ecosystem string   `json:"ecosystem,omitempty"`
}

// entry converts an Override into a libEntry for matching/source derivation.
func (o Override) entry() libEntry {
	match := make([]string, 0, len(o.Match))
	for _, m := range o.Match {
		if m = strings.ToLower(strings.TrimSpace(m)); m != "" {
			match = append(match, m)
		}
	}
	return libEntry{
		match:     match,
		docBase:   strings.TrimRight(strings.TrimSpace(o.DocBase), "/"),
		llmsTxt:   strings.TrimSpace(o.LLMSTxt),
		llmsFull:  strings.TrimSpace(o.LLMSFull),
		trust:     o.Trust,
		ecosystem: strings.ToLower(strings.TrimSpace(o.Ecosystem)),
	}
}

// overrideCache memoizes parsed override files keyed by the registry/project
// dirs so repeated resolves don't re-read disk. Loads are best-effort: missing
// or invalid files yield no entries and never error a resolve.
var (
	overrideMu    sync.Mutex
	overrideCache = map[string][]libEntry{}
)

// LoadOverrides reads the optional global and project override files, merging
// them (project entries first so a project can override a global entry, which in
// turn overrides the built-in curated list). repoRoot may be empty to skip the
// project file. Results are cached per (global, project) key.
func LoadOverrides(repoRoot string) []libEntry {
	var global, project string
	if d, err := paths.RegistryDir(); err == nil {
		global = filepath.Join(d, "docs-registry.json")
	}
	if strings.TrimSpace(repoRoot) != "" {
		project = filepath.Join(paths.RepoIndexDir(repoRoot), "docs-overrides.json")
	}

	key := global + "\x00" + project
	overrideMu.Lock()
	defer overrideMu.Unlock()
	if cached, ok := overrideCache[key]; ok {
		return cached
	}
	// Project file first: earlier entries win in matchesEntry order.
	var out []libEntry
	out = append(out, readOverrideFile(project)...)
	out = append(out, readOverrideFile(global)...)
	overrideCache[key] = out
	return out
}

// readOverrideFile parses one override JSON file. Any read/parse error or a
// malformed entry (no docBase or no usable match) is silently ignored.
func readOverrideFile(path string) []libEntry {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw []Override
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	var out []libEntry
	for _, o := range raw {
		e := o.entry()
		if e.docBase == "" || len(e.match) == 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}
