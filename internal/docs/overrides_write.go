package docs

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// GlobalOverridePath returns the machine-wide extensible catalog file
// (~/.codehelper/docs-registry.json). Entries here apply to every project.
func GlobalOverridePath() (string, error) {
	d, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "docs-registry.json"), nil
}

// ProjectOverridePath returns the per-project catalog file
// (<repo>/.codehelper/docs-overrides.json), which takes precedence over the
// global file and the built-in curated list.
func ProjectOverridePath(repoRoot string) (string, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return "", fmt.Errorf("project root is required for a project-scoped doc source")
	}
	return filepath.Join(paths.RepoIndexDir(repoRoot), "docs-overrides.json"), nil
}

// ReadOverridesFile reads the raw Override list from a catalog file. A missing
// file is not an error (returns an empty list); malformed JSON is.
func ReadOverridesFile(path string) ([]Override, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(b)) == "" {
		return nil, nil
	}
	var out []Override
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

// AddOverride upserts a documentation source into the catalog file at path,
// creating the file and parent directory if needed. It is keyed by the override's
// primary match name (case-insensitive): adding the same name again replaces the
// prior entry, so re-running with a corrected URL is idempotent. The in-process
// override cache is invalidated so the change takes effect immediately.
func AddOverride(path string, o Override) error {
	o = normalizeOverride(o)
	if err := validateOverride(o); err != nil {
		return err
	}
	existing, err := ReadOverridesFile(path)
	if err != nil {
		return err
	}
	key := primaryKey(o)
	out := existing[:0:0]
	for _, e := range existing {
		if primaryKey(normalizeOverride(e)) == key {
			continue // replaced by the new entry
		}
		out = append(out, e)
	}
	out = append(out, o)
	return writeOverrides(path, out)
}

// RemoveOverride deletes every catalog entry in the file at path whose match
// aliases include name (case-insensitive). Returns how many entries were removed.
func RemoveOverride(path, name string) (int, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return 0, fmt.Errorf("name is required")
	}
	existing, err := ReadOverridesFile(path)
	if err != nil {
		return 0, err
	}
	var kept []Override
	removed := 0
	for _, e := range existing {
		if overrideMatches(e, want) {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	if removed == 0 {
		return 0, nil
	}
	if err := writeOverrides(path, kept); err != nil {
		return 0, err
	}
	return removed, nil
}

// writeOverrides serializes the catalog atomically (temp file + rename) and
// clears the override cache so subsequent resolves in this process see the edit.
func writeOverrides(path string, entries []Override) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if entries == nil {
		entries = []Override{}
	}
	sort.SliceStable(entries, func(i, j int) bool { return primaryKey(entries[i]) < primaryKey(entries[j]) })
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	invalidateOverrideCache()
	return nil
}

// invalidateOverrideCache drops the memoized override files so a write is
// visible to later resolves in the same process (the MCP server is long-lived).
func invalidateOverrideCache() {
	overrideMu.Lock()
	overrideCache = map[string][]libEntry{}
	overrideMu.Unlock()
}

func normalizeOverride(o Override) Override {
	match := make([]string, 0, len(o.Match))
	seen := map[string]struct{}{}
	for _, m := range o.Match {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		match = append(match, m)
	}
	o.Match = match
	o.DocBase = strings.TrimRight(strings.TrimSpace(o.DocBase), "/")
	o.LLMSTxt = strings.TrimSpace(o.LLMSTxt)
	o.LLMSFull = strings.TrimSpace(o.LLMSFull)
	o.Ecosystem = strings.ToLower(strings.TrimSpace(o.Ecosystem))
	if o.Trust <= 0 {
		o.Trust = 7 // user-asserted sources are trusted by default, below top curation
	}
	if o.Trust > 10 {
		o.Trust = 10
	}
	return o
}

func validateOverride(o Override) error {
	if len(o.Match) == 0 {
		return fmt.Errorf("a doc source needs at least one name to match")
	}
	if o.DocBase == "" {
		return fmt.Errorf("a doc source needs a doc_base URL")
	}
	if u, err := url.Parse(o.DocBase); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("doc_base must be an https URL, got %q", o.DocBase)
	}
	return nil
}

func primaryKey(o Override) string {
	if len(o.Match) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(o.Match[0]))
}

func overrideMatches(o Override, want string) bool {
	for _, m := range o.Match {
		if strings.ToLower(strings.TrimSpace(m)) == want {
			return true
		}
	}
	return false
}
