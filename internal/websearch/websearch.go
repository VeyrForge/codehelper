// Package websearch is codehelper's pluggable web-search backend: a small
// provider abstraction (Tavily, Brave, or keyless DuckDuckGo) plus the config
// that stores which provider and API keys to use. The MCP `web_search` tool and
// the `ch config search` command both go through here.
//
// Keys live in ~/.codehelper/search.json (0600) and can be overridden by env
// vars, mirroring the LLM config. Provider choice is, in order: an explicit
// override → the configured provider → the first provider that has a key →
// DuckDuckGo (keyless, best-effort) so search always does *something*.
package websearch

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"encoding/json"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// Providers.
const (
	Tavily     = "tavily"
	Brave      = "brave"
	DuckDuckGo = "duckduckgo"
)

// Config is the persisted search configuration (~/.codehelper/search.json).
type Config struct {
	Provider  string `json:"provider,omitempty"` // tavily | brave | duckduckgo
	TavilyKey string `json:"tavily_key,omitempty"`
	BraveKey  string `json:"brave_key,omitempty"`
}

// Result is one search hit, trimmed to what an agent needs.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Response is a completed search.
type Response struct {
	Provider string   `json:"provider"`
	Query    string   `json:"query"`
	Answer   string   `json:"answer,omitempty"` // provider's synthesized answer (Tavily), if any
	Results  []Result `json:"results"`
}

// Path is ~/.codehelper/search.json.
func Path() (string, error) {
	dir, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "search.json"), nil
}

// Load reads the stored config; a missing file is not an error.
func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Save writes the config with 0600 perms (it holds API keys).
func Save(c Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Effective merges environment overrides over the stored config so a key in the
// shell wins without rewriting the file.
func Effective() Config {
	c, _ := Load()
	if v := envFirst("CODEHELPER_SEARCH_PROVIDER"); v != "" {
		c.Provider = v
	}
	if v := envFirst("CODEHELPER_TAVILY_KEY", "TAVILY_API_KEY"); v != "" {
		c.TavilyKey = v
	}
	if v := envFirst("CODEHELPER_BRAVE_KEY", "BRAVE_API_KEY", "BRAVE_SEARCH_API_KEY"); v != "" {
		c.BraveKey = v
	}
	return c
}

// ChooseProvider resolves which provider to use given an optional override.
// Unknown/empty override falls through to config, then key-presence, then the
// keyless DuckDuckGo fallback.
func ChooseProvider(c Config, override string) string {
	switch strings.ToLower(strings.TrimSpace(override)) {
	case Tavily, Brave, DuckDuckGo:
		return strings.ToLower(strings.TrimSpace(override))
	}
	switch c.Provider {
	case Tavily, Brave, DuckDuckGo:
		return c.Provider
	}
	if strings.TrimSpace(c.TavilyKey) != "" {
		return Tavily
	}
	if strings.TrimSpace(c.BraveKey) != "" {
		return Brave
	}
	return DuckDuckGo
}

// Search runs a query against the chosen provider.
func Search(ctx context.Context, query string, count int, override string) (*Response, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}
	if count <= 0 {
		count = 5
	}
	cfg := Effective()
	provider := ChooseProvider(cfg, override)
	switch provider {
	case Tavily:
		if strings.TrimSpace(cfg.TavilyKey) == "" {
			return nil, fmt.Errorf("tavily selected but no key set — run `ch config search set --provider tavily --key …` (free 1000/mo at tavily.com)")
		}
		return searchTavily(ctx, cfg.TavilyKey, query, count)
	case Brave:
		if strings.TrimSpace(cfg.BraveKey) == "" {
			return nil, fmt.Errorf("brave selected but no key set — run `ch config search set --provider brave --key …` (free 2000/mo at brave.com/search/api)")
		}
		return searchBrave(ctx, cfg.BraveKey, query, count)
	default:
		return searchDuckDuckGo(ctx, query, count)
	}
}

// httpClient is the shared client for provider calls. These hit fixed,
// public provider hosts, so no SSRF policy is needed (unlike the browser/web
// tools that take a user-supplied URL).
var httpClient = &http.Client{Timeout: 20 * time.Second}

func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// capSnippet keeps result snippets short so a multi-result response stays lean.
func capSnippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= 300 {
		return s
	}
	return string(r[:300]) + "…"
}
