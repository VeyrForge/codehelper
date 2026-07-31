// Package enrich is the OPT-IN, INDEX-TIME LLM enrichment layer. It is the one
// LLM role that pays off for a frontier-agent caller: the calling agent issues
// clean queries, so query-time rewrite/guidance is redundant (see
// docs/LOCAL_LLM_BENCH.md Runs 1-6) — but an offline model can add retrieval
// signal the lexical engine fundamentally lacks: a natural-language PURPOSE
// (bridges intent-phrased queries to code) and domain ALIASES (the words a
// searcher uses that the symbol name does NOT contain). This is the EnrichIndex
// pattern (arXiv:2504.03598): generate enrichment OFFLINE, store it as SEPARATE
// fields, and keep the identifier/signature field pristine and dominant.
//
// Why purpose+aliases only (not summary/QA pairs): Run 7 showed naive generated
// PROSE blended near the identifier HURTS retrieval (it dilutes the strong name
// signal). Purpose is one tight sentence kept in its OWN field; aliases are bare
// terms — both add orthogonal vocabulary without polluting the exact-match field.
//
// Design constraints (mirroring internal/semantic so it never surprises a user):
//   - OFF by default. Active only when CODEHELPER_ENRICH_URL is configured.
//   - OFFLINE / index-time only — never on the query hot path, so zero added query
//     latency and no per-query model dependency.
//   - CONTENT-HASH CACHED: a symbol is re-enriched only when its surface
//     (name+signature+kind) changes, so steady-state cost is ~zero.
//   - BEST-EFFORT: any model error skips that symbol; the deterministic index is
//     never blocked or corrupted by enrichment.
package enrich

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Chat is the minimal chat-completion surface enrich needs. Implementations:
// HTTPChat (a local OpenAI-compatible model server) or a test fake — so unit
// tests never depend on a running model (project rule: never depend on Ollama).
type Chat interface {
	Complete(ctx context.Context, system, user string) (string, error)
	Model() string
}

// FromEnv returns a Chat when CODEHELPER_ENRICH_URL is set, else nil (feature off
// → indexing stays pure-deterministic). It speaks the OpenAI-compatible
// /v1/chat/completions API that Ollama, llama.cpp server, and LM Studio expose, so
// the user points it at any local model. The 7B-Q4 class is the validated sweet
// spot for these short grounded tasks (docs/LOCAL_LLM_BENCH.md Run 5).
func FromEnv() Chat {
	url := strings.TrimSpace(os.Getenv("CODEHELPER_ENRICH_URL"))
	if url == "" {
		return nil
	}
	model := strings.TrimSpace(os.Getenv("CODEHELPER_ENRICH_MODEL"))
	if model == "" {
		model = "qwen2.5-coder:7b"
	}
	return &HTTPChat{BaseURL: strings.TrimRight(url, "/"), ModelName: model, Client: &http.Client{Timeout: 60 * time.Second}}
}

// HTTPChat calls POST {BaseURL}/v1/chat/completions (OpenAI-compatible), the same
// endpoint shape Ollama / llama.cpp / LM Studio serve locally.
type HTTPChat struct {
	BaseURL   string
	ModelName string
	Client    *http.Client
}

func (h *HTTPChat) Model() string { return h.ModelName }

func (h *HTTPChat) Complete(ctx context.Context, system, user string) (string, error) {
	payload := map[string]any{
		"model":       h.ModelName,
		"temperature": 0, // deterministic: same symbol → same enrichment
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if k := os.Getenv("CODEHELPER_LLM_API_KEY"); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("enrich endpoint status %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("enrich endpoint returned no choices")
	}
	return out.Choices[0].Message.Content, nil
}

// Enrichment is the per-symbol index-time enrichment: SEPARATE retrieval fields
// that never touch the identifier field. Purpose is a one-sentence intent bridge;
// Aliases are domain/synonym terms absent from the name. Hash gates regeneration.
type Enrichment struct {
	SymbolID string   `json:"symbol_id"`
	Hash     string   `json:"hash"`
	Purpose  string   `json:"purpose,omitempty"`
	Aliases  []string `json:"aliases,omitempty"`
	Model    string   `json:"model,omitempty"`
}

// SearchText is the text a retrieval layer would index as a SEPARATE, lower-
// weighted field (never merged into the name/signature field). Empty when there is
// nothing useful to add, so the caller indexes nothing rather than noise.
func (e Enrichment) SearchText() string {
	parts := make([]string, 0, len(e.Aliases)+1)
	if e.Purpose != "" {
		parts = append(parts, e.Purpose)
	}
	parts = append(parts, e.Aliases...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

// ContentHash fingerprints the symbol surface that the enrichment is derived from.
// When this is unchanged, the cached enrichment is still valid — so re-indexing an
// unchanged repo makes zero model calls (the Roo-Code "aux-hash" pattern).
func ContentHash(sym types.Symbol) string {
	h := sha256.Sum256([]byte(sym.Name + "\x00" + sym.Signature + "\x00" + string(sym.Kind)))
	return hex.EncodeToString(h[:8])
}

const systemPrompt = `You enrich a code symbol for SEARCH. Reply with ONLY a JSON object:
{"purpose": "<one short sentence: what this symbol does and why it exists>", "aliases": ["<domain or synonym term a developer might search that is NOT already in the symbol name>", ...]}
Rules: purpose is one sentence, grounded ONLY in the given signature/doc — do not invent behavior. aliases: 0-5 lowercase terms, each a real synonym/domain word a searcher would use, none a substring already present in the name. If you cannot ground a field, return it empty. No prose outside the JSON.`

// Generator turns symbols into Enrichment via a Chat model. Best-effort and
// grounded: it prompts only with the symbol's own name/signature/doc, so it
// describes what is there rather than hallucinating behavior.
type Generator struct {
	Chat Chat
}

// Enrich generates one symbol's enrichment. Returns the Enrichment (with its
// content hash stamped) or an error the caller treats as "skip this symbol".
func (g Generator) Enrich(ctx context.Context, sym types.Symbol) (Enrichment, error) {
	user := fmt.Sprintf("name: %s\nkind: %s\nlanguage: %s\nsignature/doc: %s",
		sym.Name, sym.Kind, sym.Language, strings.TrimSpace(sym.Signature))
	raw, err := g.Chat.Complete(ctx, systemPrompt, user)
	if err != nil {
		return Enrichment{}, err
	}
	e, err := parseEnrichment(raw, sym.Name)
	if err != nil {
		return Enrichment{}, err
	}
	e.SymbolID = sym.ID
	e.Hash = ContentHash(sym)
	e.Model = g.Chat.Model()
	return e, nil
}

// parseEnrichment tolerantly extracts the JSON object even when a small model wraps
// it in prose or a ```json fence, then sanitizes: aliases are lowercased, deduped,
// stripped of any term already a substring of the name (those add no recall), and
// capped at 5. This is the guard that keeps generated noise out of the index.
func parseEnrichment(raw, symbolName string) (Enrichment, error) {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return Enrichment{}, fmt.Errorf("enrich: no JSON object in model output")
	}
	var parsed struct {
		Purpose string   `json:"purpose"`
		Aliases []string `json:"aliases"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		return Enrichment{}, fmt.Errorf("enrich: bad JSON from model: %w", err)
	}
	lowerName := strings.ToLower(symbolName)
	seen := map[string]struct{}{}
	var aliases []string
	for _, a := range parsed.Aliases {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if strings.Contains(lowerName, a) { // already findable lexically — adds nothing
			continue
		}
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		aliases = append(aliases, a)
		if len(aliases) >= 5 {
			break
		}
	}
	return Enrichment{Purpose: strings.TrimSpace(parsed.Purpose), Aliases: aliases}, nil
}

// BatchResult reports what an EnrichBatch run did, for telemetry and CLI output.
type BatchResult struct {
	Generated int // model calls made (new or changed symbols)
	Cached    int // skipped because the content hash was unchanged
	Failed    int // model/parse errors — symbol left un-enriched, index unaffected
}

// EnrichBatch enriches symbols whose surface changed since the last run, skipping
// any whose content hash already matches the store (so steady-state re-index cost
// is ~zero), and persisting results. Best-effort: a per-symbol model error
// increments Failed and moves on — enrichment never blocks or corrupts indexing.
func EnrichBatch(ctx context.Context, gen Generator, syms []types.Symbol, store *Store) (BatchResult, error) {
	var res BatchResult
	for _, sym := range syms {
		if existing, ok := store.Get(sym.ID); ok && existing.Hash == ContentHash(sym) {
			res.Cached++
			continue
		}
		e, err := gen.Enrich(ctx, sym)
		if err != nil {
			res.Failed++
			continue
		}
		store.Put(e)
		res.Generated++
	}
	if err := store.Flush(); err != nil {
		return res, err
	}
	return res, nil
}
