package paths

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// DotDir is the per-repository index directory name.
const DotDir = ".codehelper"

// ExternalIndexHome returns the configured out-of-repo index root, or "" for the
// default (in-repo .codehelper/). Set the CODEHELPER_INDEX_HOME environment
// variable to keep ALL index artifacts out of project repos entirely — zero
// footprint: nothing is written into the repo and no .gitignore edit is made, so
// you can index/analyze any project (or a throwaway clone) without touching it.
// Because every path below routes through RepoIndexDir, setting this one var
// transparently redirects analyze, the MCP server, the watch daemon, meta, and
// the cache — they all agree as long as the var is set for each process.
func ExternalIndexHome() string {
	return strings.TrimSpace(os.Getenv("CODEHELPER_INDEX_HOME"))
}

// repoKey derives a stable, collision-resistant, filesystem-safe directory name
// for a repo root (its base name + a short hash of the absolute path), so two
// repos both named "app" get distinct external index dirs.
func repoKey(repoRoot string) string {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		abs = repoRoot
	}
	abs = filepath.Clean(abs)
	sum := sha1.Sum([]byte(abs))
	return filepath.Base(abs) + "-" + hex.EncodeToString(sum[:])[:10]
}

// RegistryDir returns the global config directory (~/.codehelper).
func RegistryDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codehelper"), nil
}

// RegistryFile is the global multi-repo registry path.
func RegistryFile() (string, error) {
	d, err := RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "registry.json"), nil
}

// RepoIndexDir returns the index directory for a repo: an external location when
// CODEHELPER_INDEX_HOME is set (zero in-repo footprint), else .codehelper/ under
// the repo root.
func RepoIndexDir(repoRoot string) string {
	if home := ExternalIndexHome(); home != "" {
		return filepath.Join(home, repoKey(repoRoot))
	}
	return filepath.Join(repoRoot, DotDir)
}

// MetaPath returns meta.json path for a repo.
func MetaPath(repoRoot string) string {
	return filepath.Join(RepoIndexDir(repoRoot), "meta.json")
}

// DBPath returns sqlite graph db path.
func DBPath(repoRoot string) string {
	return filepath.Join(RepoIndexDir(repoRoot), "graph.db")
}

// CachePath returns badger cache directory for a repo.
func CachePath(repoRoot string) string {
	return filepath.Join(RepoIndexDir(repoRoot), "cache")
}

// HubsPath returns the precomputed call-graph "what's linked" summary (symbol +
// package hubs) written at index time so project_context reads it instantly.
func HubsPath(repoRoot string) string {
	return filepath.Join(RepoIndexDir(repoRoot), "hubs.json")
}

// EnrichmentPath returns the index-time LLM enrichment store for a repo.
func EnrichmentPath(repoRoot string) string {
	return filepath.Join(RepoIndexDir(repoRoot), "enrich", "enrichment.json")
}

// VocabPath returns the index-time project vocabulary seed for a repo. This is a
// DERIVED artifact (frequency of identifiers/sub-words across the codebase), so it
// lives in the gitignored index dir and is regenerated on each index — distinct
// from the human-reviewed, committable glossary in project_memory.json.
func VocabPath(repoRoot string) string {
	return filepath.Join(RepoIndexDir(repoRoot), "vocab.json")
}
