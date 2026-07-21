package retrieval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/green"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// Phase-2 semantic re-rank (opt-in via CODEHELPER_EMBED_URL).
//
// The deterministic layer (synonyms, abbreviations, plurals, trigram) is English-
// and hand-curated; it can't cover Slovenian, Spanish, slang, or every typo. A
// multilingual embedding model can. This wires codehelper to an external OpenAI-
// compatible /v1/embeddings server (e.g. green-engine's `ge embed serve`, Granite
// 97M, CPU) and re-ranks ONLY the lexical top-N by query↔symbol similarity.
//
// Performance/safety:
//   - Bounded cost: one batched embed call per query over ≤semanticTopN candidates.
//     We never embed the whole repo at query time or ANN-search millions of vectors.
//   - Fail-safe: any error / unset URL → the untouched fast lexical result. The
//     default (no env) build is byte-for-byte the old pure-Go path.
//   - Re-rank, not replace: semantics nudge (40%); a strong lexical hit still wins.

// Embedder turns a batch of texts into vectors. nil = disabled (default).
type Embedder interface {
	EmbedBatch(texts []string) ([][]float32, error)
}

var activeEmbedder Embedder

// SetEmbedder installs a semantic backend (tests / alternative wirings).
func SetEmbedder(e Embedder) { activeEmbedder = e }

// SemanticEnabled reports whether the opt-in re-rank layer is active.
func SemanticEnabled() bool { return activeEmbedder != nil }

func init() {
	if u := strings.TrimSpace(os.Getenv("CODEHELPER_EMBED_URL")); u != "" {
		activeEmbedder = newHTTPEmbedder(u)
	}
}

var defaultEmbedProbeURLs = []string{
	"http://127.0.0.1:8766",
	"http://127.0.0.1:8780",
}

func probeLocalEmbedServer() string {
	client := &http.Client{Timeout: 400 * time.Millisecond}
	for _, base := range defaultEmbedProbeURLs {
		u := strings.TrimRight(base, "/") + "/v1/models"
		resp, err := client.Get(u)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return base
		}
	}
	return ""
}

// EnsureEmbedder lazily wires the opt-in semantic backend from green.json or a
// localhost probe. Called from MCP query paths only — not from QueryHybridWithOptions
// directly — so unit tests stay deterministic.
func EnsureEmbedder() {
	if activeEmbedder != nil {
		return
	}
	if u := strings.TrimSpace(os.Getenv("CODEHELPER_EMBED_URL")); u != "" {
		activeEmbedder = newHTTPEmbedder(u)
		return
	}
	if _, err := green.LoadAndExport(); err == nil {
		if u := strings.TrimSpace(os.Getenv("CODEHELPER_EMBED_URL")); u != "" {
			activeEmbedder = newHTTPEmbedder(u)
			return
		}
	}
	if u := probeLocalEmbedServer(); u != "" {
		activeEmbedder = newHTTPEmbedder(u)
	}
}

// httpEmbedder speaks the OpenAI /v1/embeddings API.
type httpEmbedder struct {
	endpoint string
	model    string
	client   *http.Client
}

func newHTTPEmbedder(base string) *httpEmbedder {
	model := strings.TrimSpace(os.Getenv("CODEHELPER_EMBED_MODEL"))
	if model == "" {
		model = "granite-embedding"
	}
	return &httpEmbedder{
		endpoint: strings.TrimRight(base, "/") + "/v1/embeddings",
		model:    model,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (h *httpEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"input": texts, "model": h.model})
	if err != nil {
		return nil, err
	}
	resp, err := h.client.Post(h.endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed server status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	vecs := make([][]float32, len(out.Data))
	for _, d := range out.Data {
		if d.Index >= 0 && d.Index < len(vecs) {
			vecs[d.Index] = d.Embedding
		}
	}
	return vecs, nil
}

// semanticTopN bounds how many lexical candidates get embedded+re-ranked. It must
// be wide enough that a cross-lingual match (lexically buried because the query is
// in another language) still enters the window — but bounded so cost stays at one
// modest batch per query regardless of repo size.
const semanticTopN = 80

// semanticRerankQuery RRF-fuses the lexical(+graph) list with a vector-ranked
// channel when an embedder is active. One batched embed call; bounded to
// semanticTopN; fail-safe to the input list on any error / unset URL.
func semanticRerankQuery(query string, ranked []RankedSymbol) []RankedSymbol {
	vecList := semanticVectorList(query, ranked)
	if len(vecList) == 0 {
		return ranked
	}
	return FuseRRF(ranked, vecList, 60)
}

// semanticVectorList embeds the query + top-N candidates and returns them ranked
// by min-max-normalized cosine similarity (reason "vector").
func semanticVectorList(query string, ranked []RankedSymbol) []RankedSymbol {
	if activeEmbedder == nil || len(ranked) == 0 || strings.TrimSpace(query) == "" {
		return nil
	}
	n := len(ranked)
	if n > semanticTopN {
		n = semanticTopN
	}
	texts := make([]string, 0, n+1)
	texts = append(texts, query)
	for i := 0; i < n; i++ {
		texts = append(texts, candidateText(ranked[i].Symbol))
	}
	vecs, err := activeEmbedder.EmbedBatch(texts)
	if err != nil || len(vecs) < n+1 || len(vecs[0]) == 0 {
		return nil
	}
	qv := vecs[0]
	sims := make([]float64, n)
	lo, hi := math.Inf(1), math.Inf(-1)
	for i := 0; i < n; i++ {
		if cv := vecs[i+1]; len(cv) > 0 {
			s := cosine(qv, cv)
			sims[i] = s
			if s < lo {
				lo = s
			}
			if s > hi {
				hi = s
			}
		}
	}
	out := make([]RankedSymbol, 0, n)
	for i := 0; i < n; i++ {
		ns := 0.0
		if hi > lo {
			ns = (sims[i] - lo) / (hi - lo)
		}
		rs := ranked[i]
		rs.Score = ns
		rs.Reasons = []string{"vector"}
		out = append(out, rs)
	}
	sort.SliceStable(out, func(i, j int) bool { return rankedLess(out[i], out[j]) })
	return out
}

// candidateText is what we embed for a symbol. A bare identifier ("compute_expert")
// gives a text model almost nothing to bridge a natural-language or cross-lingual
// query to, and compresses every cosine into a narrow band where generic
// high-centrality names win. So we feed it (1) the identifier split into real words
// ("compute expert"), (2) the doc comment + signature when the parser captured one,
// and (3) a humanized module stem for grounding when the signature is empty.
func candidateText(s types.Symbol) string {
	parts := make([]string, 0, 4)
	if words := humanizeIdent(s.Name); words != "" && words != s.Name {
		parts = append(parts, s.Name, words)
	} else {
		parts = append(parts, s.Name)
	}
	if sig := strings.TrimSpace(s.Signature); sig != "" && !isMetaSignature(sig) {
		parts = append(parts, sig)
	} else if ctx := humanizeIdent(pathContext(s.Path)); ctx != "" {
		// No real doc/signature (terse parser, or a metadata pseudo-sig like
		// "role=state"): ground the bare identifier in its module path so a generic
		// name (`model`, `config`) doesn't float free and collide with every other
		// generically-named symbol in the embedding space.
		parts = append(parts, ctx)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// pathContext returns the last directory plus the file stem ("crates/sched/
// expert_pool.rs" -> "sched expert_pool"), so two symbols that share a generic
// name in different modules embed to different text and stop colliding.
func pathContext(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "."); i > 0 && !strings.Contains(p[i:], "/") {
		p = p[:i]
	}
	segs := strings.Split(p, "/")
	if n := len(segs); n >= 2 {
		return segs[n-2] + " " + segs[n-1]
	}
	if len(segs) == 1 {
		return segs[0]
	}
	return ""
}

// isMetaSignature reports whether a "signature" is actually a key=value annotation
// the parser attached (role=state, embeds=Foo) rather than prose or a real type
// signature. Such text is noise to a language embedding model.
func isMetaSignature(sig string) bool {
	eq := strings.IndexByte(sig, '=')
	if eq <= 0 {
		return false
	}
	// "role=state" / "embeds=Foo,Bar" — a leading bare key then '='. Real signatures
	// start with '(' or contain spaces/prose before any '='.
	key := sig[:eq]
	return !strings.ContainsAny(key, " (") && len(key) <= 12
}

// humanizeIdent splits a snake_case / camelCase / kebab identifier into space-
// separated lowercase words ("scheduleExperts" / "schedule_experts" -> "schedule
// experts") so a text embedding model sees language, not one opaque token.
func humanizeIdent(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	prevLower := false
	for _, r := range name {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ':
			b.WriteByte(' ')
			prevLower = false
		case r >= 'A' && r <= 'Z':
			if prevLower {
				b.WriteByte(' ')
			}
			b.WriteRune(r + ('a' - 'A'))
			prevLower = false
		default:
			b.WriteRune(r)
			prevLower = r >= 'a' && r <= 'z'
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
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

// --- binary-quantization helpers (for a future pre-stored, size-capped vector
// store; not used by the on-the-fly path above). 64 dims → 8 bytes/symbol. ---

func QuantizeBinary(v []float32) []uint64 {
	out := make([]uint64, (len(v)+63)/64)
	for i, x := range v {
		if x > 0 {
			out[i/64] |= 1 << uint(i%64)
		}
	}
	return out
}

func hammingSim(a, b []uint64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	diff := 0
	for i := range a {
		diff += bits.OnesCount64(a[i] ^ b[i])
	}
	return 1 - float64(diff)/float64(len(a)*64)
}
