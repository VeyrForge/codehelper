// Package semantic is the OPT-IN multilingual/semantic rerank layer. The lexical
// engine (BM25+trigram) is strongest on concrete English code words and weakest on
// vague natural language, slang, typos, and NON-ENGLISH queries (e.g. Slovenian
// "prijava" for login) — no hardcoded synonym/abbreviation table can ever cover
// those. A multilingual embedding model can: it maps "prijava", "login", and
// "anmeldung" near the same vector as the code's `login`/`auth` symbols.
//
// Design constraints (so it never overflows a PC, even with many agents):
//   - OFF by default. Enabled only when an embedding endpoint is configured.
//   - RERANK ONLY: it re-orders the lexical top-N candidates; it never does an
//     ANN search over millions of vectors. Cost per query ≈ N hamming compares.
//   - BINARY-QUANTIZED vectors: 1 bit/dim → a 1024-dim embedding is 128 bytes
//     (8 bytes for 64-dim). 3.2M symbols ≈ 25-400MB depending on dim, vs GBs.
//   - Any-PC model: point it at a local CPU model (Ollama `bge-m3`, llama.cpp,
//     LM Studio) via an OpenAI-compatible /v1/embeddings endpoint.
package semantic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"net/http"
	"os"
	"strings"
	"time"
)

// Embedder turns text into vectors. Implementations: HTTPEmbedder (a local model
// server) or a test fake. Returns one vector per input, all same dimensionality.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
}

// FromEnv returns an Embedder when CODEHELPER_EMBED_URL is set, else nil (feature
// off → callers stay pure-lexical). CODEHELPER_EMBED_MODEL defaults to bge-m3, the
// recommended 100+-language CPU model.
func FromEnv() Embedder {
	url := strings.TrimSpace(os.Getenv("CODEHELPER_EMBED_URL"))
	if url == "" {
		return nil
	}
	model := strings.TrimSpace(os.Getenv("CODEHELPER_EMBED_MODEL"))
	if model == "" {
		model = "bge-m3"
	}
	return &HTTPEmbedder{BaseURL: strings.TrimRight(url, "/"), ModelName: model, Client: &http.Client{Timeout: 30 * time.Second}}
}

// HTTPEmbedder calls an OpenAI-compatible POST {BaseURL}/v1/embeddings — the API
// Ollama, llama.cpp server, LM Studio, and OpenAI all speak. So the user can run
// any multilingual model locally and point codehelper at it.
type HTTPEmbedder struct {
	BaseURL   string
	ModelName string
	Client    *http.Client
}

func (h *HTTPEmbedder) Model() string { return h.ModelName }

func (h *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": h.ModelName, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.BaseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if k := os.Getenv("CODEHELPER_EMBED_KEY"); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed endpoint status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}

// Quantize compresses a float vector to its sign bits (1 bit/dim). This is the
// "never overflow" lever: a 1024-dim vector becomes 128 bytes, and similarity is
// a popcount of XOR (Hamming) — integer-only, cache-friendly, fast at any scale.
func Quantize(v []float32) []byte {
	out := make([]byte, (len(v)+7)/8)
	for i, x := range v {
		if x >= 0 {
			out[i/8] |= 1 << uint(i%8)
		}
	}
	return out
}

// HammingSim returns a [0,1] similarity from two binary-quantized vectors (1 =
// identical bits). Equal length assumed.
func HammingSim(a, b []byte) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	diff := 0
	for i := range a {
		diff += bits.OnesCount8(a[i] ^ b[i])
	}
	total := len(a) * 8
	return 1 - float64(diff)/float64(total)
}

// Cosine similarity for full-precision vectors (used when not quantizing).
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Candidate is a lexical hit to be reranked: its normalized lexical score plus a
// binary-quantized embedding of its name/signature.
type Candidate struct {
	ID       string
	LexScore float64 // normalized [0,1]
	QuantVec []byte
}

// HybridRerank blends lexical and semantic similarity for the candidate set,
// returning indices sorted best-first. semanticWeight in [0,1]: 0 = pure lexical
// (semantic ignored), 0.5 = even blend. Only the candidates are scored — never a
// global vector search — so this is cheap regardless of repo size.
func HybridRerank(queryVec []byte, cands []Candidate, semanticWeight float64) []int {
	type scored struct {
		idx   int
		score float64
	}
	out := make([]scored, len(cands))
	for i, c := range cands {
		sem := HammingSim(queryVec, c.QuantVec)
		out[i] = scored{i, (1-semanticWeight)*c.LexScore + semanticWeight*sem}
	}
	// simple stable insertion sort (candidate sets are small, ≤ a few hundred)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].score > out[j-1].score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	idxs := make([]int, len(out))
	for i, s := range out {
		idxs[i] = s.idx
	}
	return idxs
}
