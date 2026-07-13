package docs

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Result is the structured outcome of a docs lookup.
type Result struct {
	Library    string         `json:"library"`
	Version    string         `json:"version,omitempty"`
	Ecosystem  string         `json:"ecosystem,omitempty"`
	Topic      string         `json:"topic,omitempty"`
	Resolved   Resolved       `json:"resolved"`
	SourceUsed string         `json:"source_used,omitempty"`
	SourceKind string         `json:"source_kind,omitempty"`
	Index      *LLMSIndex     `json:"index,omitempty"`
	Chunks     []Chunk        `json:"chunks,omitempty"`
	Tokens     int            `json:"tokens"`
	FromCache  bool           `json:"from_cache,omitempty"`
	Offline    bool           `json:"offline,omitempty"`
	Attempts   []FetchAttempt `json:"attempts,omitempty"`
	LinkChecks []LinkStatus   `json:"link_checks,omitempty"` // validation of returned doc links
	Note       string         `json:"note,omitempty"`
}

// FetchAttempt records one source fetch for transparency/debugging.
type FetchAttempt struct {
	URL    string `json:"url"`
	Kind   string `json:"kind"`
	Status int    `json:"status,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// LookupOptions controls a docs lookup.
type LookupOptions struct {
	RepoRoot      string // for manifest version detection + cache scoping
	Library       string
	Version       string // explicit override; resolved from manifest when empty
	Topic         string
	MaxTokens     int
	MaxLinks      int
	Network       bool // whether network fetch is permitted (privacy gate)
	FollowLinks   bool // for llms.txt indexes, fetch and chunk the top links
	ValidateLinks bool // probe returned llms.txt links and drop dead (4xx) ones
	NoCache       bool
}

// Engine resolves and fetches documentation. Fetcher and Cache are injectable
// so tests run offline; Now defaults to time.Now.
type Engine struct {
	Fetcher   Fetcher
	Cache     *Cache
	Validator LinkValidator // optional; validates returned doc links when set
	Now       func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// kindPriority orders source kinds best-content-first.
func kindPriority(kind string) int {
	switch kind {
	case "llms-full.txt":
		return 0
	case "llms.txt":
		return 1
	default:
		return 2
	}
}

// Lookup resolves a library to docs and (if allowed) fetches version-correct
// content, preferring llms-full.txt > llms.txt > HTML. Safe offline: with
// Network=false it returns the resolved sources and how to enable fetching.
func (e *Engine) Lookup(ctx context.Context, opts LookupOptions) (*Result, error) {
	lib := strings.TrimSpace(opts.Library)
	version := strings.TrimSpace(opts.Version)
	ecosystem := ""
	if opts.RepoRoot != "" {
		if v, eco := ResolveVersion(opts.RepoRoot, lib); v != "" {
			if version == "" {
				version = v
			}
			ecosystem = eco
		}
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 5000
	}

	if e.Cache != nil && !opts.NoCache {
		if cached, ok := e.Cache.Get(lib, version, opts.Topic, e.now()); ok {
			return cached, nil
		}
	}

	resolved := ResolveFull(lib, version, ecosystem, opts.RepoRoot)

	// When the curated index and overrides both miss, ask the ecosystem's public
	// registry for the author-declared docs URL instead of relying on host
	// guesses. This removes per-library curation for the long tail and only runs
	// when network is permitted (it is itself a network call).
	if opts.Network && resolved.Origin == "derived" && e.Fetcher != nil {
		if meta, ok := resolveFromRegistry(ctx, e.Fetcher, lib, resolved.Ecosystem); ok {
			if srcs := sourcesForDocBase(meta.DocBase); len(srcs) > 0 {
				resolved.DocBase = meta.DocBase
				resolved.Sources = srcs
				resolved.TrustScore = meta.Trust
				resolved.Origin = "registry:" + meta.Source
				resolved.Note = "resolved from " + meta.Source + " registry metadata (author-declared docs URL)"
				if resolved.Ecosystem == "" {
					resolved.Ecosystem = ecosystemFor(meta.DocBase)
				}
				if version == "" && meta.Version != "" {
					version = meta.Version
				}
			}
		}
	}

	res := &Result{
		Library:   lib,
		Version:   version,
		Ecosystem: resolved.Ecosystem,
		Topic:     opts.Topic,
		Resolved:  resolved,
	}

	if !opts.Network {
		res.Offline = true
		if len(resolved.Sources) == 0 {
			res.Note = "library not recognized and no doc host could be derived; pass an explicit doc URL or add it to the curated registry"
		} else {
			res.Note = "offline: returning resolved doc sources only. Enable fetching by setting research.enabled in .codehelper/learning.json or passing approve_network=true."
		}
		return res, nil
	}
	if len(resolved.Sources) == 0 {
		res.Note = "no documentation sources to fetch for this library"
		return res, nil
	}

	// Try sources best-content-first.
	sources := append([]Source(nil), resolved.Sources...)
	sort.SliceStable(sources, func(a, b int) bool {
		return kindPriority(sources[a].Kind) < kindPriority(sources[b].Kind)
	})

	for _, src := range sources {
		fr := e.Fetcher.Fetch(ctx, src.URL)
		att := FetchAttempt{URL: src.URL, Kind: src.Kind, Status: fr.StatusCode}
		if fr.Err != nil {
			att.Error = fr.Err.Error()
			res.Attempts = append(res.Attempts, att)
			continue
		}
		if fr.StatusCode >= 400 || strings.TrimSpace(fr.Body) == "" {
			res.Attempts = append(res.Attempts, att)
			continue
		}
		att.OK = true
		res.Attempts = append(res.Attempts, att)

		chunks, idx := e.process(ctx, src, fr.Body, opts, maxTokens, res)
		if len(chunks) == 0 && idx == nil {
			continue
		}
		res.SourceUsed = src.URL
		res.SourceKind = src.Kind
		res.Index = idx
		res.Chunks = chunks
		for _, c := range chunks {
			res.Tokens += c.Tokens
		}
		break
	}

	if res.SourceUsed == "" {
		res.Note = "all documentation sources failed or returned no usable content"
	}
	if e.Cache != nil && !opts.NoCache && res.SourceUsed != "" {
		e.Cache.Put(lib, version, opts.Topic, res, e.now())
	}
	return res, nil
}

// process converts a fetched source body into ranked chunks. For llms.txt
// indexes it optionally follows the most relevant links (same host only) and
// chunks their content.
func (e *Engine) process(ctx context.Context, src Source, body string, opts LookupOptions, maxTokens int, res *Result) ([]Chunk, *LLMSIndex) {
	switch src.Kind {
	case "llms.txt":
		idx := ParseLLMSIndex(body)
		if len(idx.Links) == 0 {
			// Some sites serve full markdown at /llms.txt; treat as content.
			return RankChunks(ChunkMarkdown(body), opts.Topic, maxTokens), nil
		}
		links := SelectLinks(idx, opts.Topic, maxLinks(opts.MaxLinks))
		// Validate the candidate links so a 404'd or hallucinated entry in a
		// stale llms.txt is never handed back to the model.
		if opts.ValidateLinks && e.Validator != nil {
			var checks []LinkStatus
			links, checks = validateLinks(ctx, e.Validator, links)
			if res != nil {
				res.LinkChecks = append(res.LinkChecks, checks...)
			}
		}
		if !opts.FollowLinks {
			return linksAsChunks(links), &idx
		}
		var chunks []Chunk
		used := 0
		host := hostOf(src.URL)
		for _, l := range links {
			if used >= maxTokens {
				break
			}
			if hostOf(l.URL) != host {
				continue // SSRF guard: only follow same-host links
			}
			fr := e.Fetcher.Fetch(ctx, l.URL)
			if fr.Err != nil || fr.StatusCode >= 400 || strings.TrimSpace(fr.Body) == "" {
				continue
			}
			text := fr.Body
			if looksHTML(text) {
				text = HTMLToText(text)
			}
			for _, c := range RankChunks(ChunkMarkdown(text), opts.Topic, maxTokens-used) {
				if c.Heading == "" {
					c.Heading = l.Title
				}
				chunks = append(chunks, c)
				used += c.Tokens
				if used >= maxTokens {
					break
				}
			}
		}
		if len(chunks) == 0 {
			return linksAsChunks(links), &idx
		}
		return chunks, &idx
	case "llms-full.txt":
		return RankChunks(ChunkMarkdown(body), opts.Topic, maxTokens), nil
	default: // html
		return RankChunks(ChunkMarkdown(HTMLToText(body)), opts.Topic, maxTokens), nil
	}
}

func linksAsChunks(links []LLMSLink) []Chunk {
	var out []Chunk
	for _, l := range links {
		text := l.Title
		if l.Desc != "" {
			text += " — " + l.Desc
		}
		text += "\n" + l.URL
		out = append(out, Chunk{Heading: l.Section, Text: text, Tokens: estimateTokens(text)})
	}
	return out
}

func maxLinks(n int) int {
	if n <= 0 {
		return 8
	}
	return n
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func looksHTML(s string) bool {
	head := strings.ToLower(s)
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype") || strings.Contains(head, "<body") || strings.Contains(head, "<div")
}
