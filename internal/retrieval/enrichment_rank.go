package retrieval

import (
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/VeyrForge/codehelper/internal/enrich"
	"github.com/VeyrForge/codehelper/internal/profile"
)

// DefaultEnrichmentWeight scales the separate index-time enrichment field in
// ranking. Purpose + aliases live in their own field (never merged into the
// identifier), so this stays below name_field (0.12/hit) and exact_name (1.0):
// enrichment sharpens vocabulary-bridge queries without overriding lexical signal.
const DefaultEnrichmentWeight = 0.08

type enrichCacheEntry struct {
	modTime int64
	texts   map[string]string
}

var (
	enrichCacheMu sync.RWMutex
	enrichCache   = map[string]enrichCacheEntry{}
)

// resolveEnrichmentTexts loads symbol-id → search-text from the offline enrichment
// store when present. Returns nil when the store is absent or empty, so the default
// retrieval path is byte-for-byte unchanged until `codehelper enrich` has run.
func resolveEnrichmentTexts(repoRoot string) map[string]string {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return nil
	}
	path := enrich.DefaultPath(repoRoot)
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	enrichCacheMu.RLock()
	if ent, ok := enrichCache[repoRoot]; ok && ent.modTime == st.ModTime().UnixNano() {
		texts := ent.texts
		enrichCacheMu.RUnlock()
		return texts
	}
	enrichCacheMu.RUnlock()

	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return nil
	}
	var raw map[string]enrich.Enrichment
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil
	}
	texts := make(map[string]string, len(raw))
	for id, e := range raw {
		if t := e.SearchText(); t != "" {
			texts[id] = t
		}
	}
	if len(texts) == 0 {
		return nil
	}
	enrichCacheMu.Lock()
	enrichCache[repoRoot] = enrichCacheEntry{modTime: st.ModTime().UnixNano(), texts: texts}
	enrichCacheMu.Unlock()
	return texts
}

// MCPQueryOptions returns the standard MCP retrieval options, including enrichment
// when an offline store exists for repoRoot.
func MCPQueryOptions(repoRoot, intent string, queryTokens []string, changed map[string]struct{}) QueryOptions {
	return MCPQueryOptionsWithProfile(repoRoot, intent, queryTokens, changed)
}

func MCPQueryOptionsWithProfile(repoRoot, intent string, queryTokens []string, changed map[string]struct{}) QueryOptions {
	opts := QueryOptions{
		ChangedSymbolIDs: changed,
		Intent:           intent,
		QueryTokens:      queryTokens,
		CentralityWeight: DefaultCentralityWeight,
		RepoRoot:         repoRoot,
	}
	if p, err := profile.Read(repoRoot); err == nil && p != nil {
		opts.PrimaryLanguage = strings.TrimSpace(p.PrimaryLanguage)
	}
	if repoRoot != "" {
		opts.LikelyEntrypointFiles = discoverLikelyEntrypoints(repoRoot)
	}
	return opts
}
