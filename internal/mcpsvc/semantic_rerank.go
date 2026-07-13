package mcpsvc

import (
	"github.com/VeyrForge/codehelper/internal/retrieval"
)

// semanticRerankStatus tells the agent — and the human reading the trace — whether
// the OPT-IN local multilingual model (CODEHELPER_EMBED_URL) actually handled this
// query, and reminds it that semantic hits are *conceptual* matches to verify, not
// exact lexical ones. The rerank itself happens once, inside QueryHybridWithOptions;
// this only reports on it. Empty string when the feature is off (the default), so a
// pure-lexical setup shows nothing extra.
func semanticRerankStatus(hits []retrieval.RankedSymbol) string {
	if !retrieval.SemanticEnabled() {
		return "" // off by default — no embedding endpoint configured
	}
	for _, h := range hits {
		for _, r := range h.Reasons {
			if r == "semantic" {
				return "active — re-ranked by your local multilingual embedding model, so non-English / slang / typo'd phrasing still finds the right code. These are CONCEPTUAL matches: confirm with `context` or `read_workspace_file` before you act on one."
			}
		}
	}
	// Configured but the rerank didn't apply: server unreachable/slow, or too few
	// candidates to reorder. Say so plainly rather than silently falling back.
	return "configured but inactive for this query (embed server unreachable, timed out, or too few hits) — results are pure lexical ranking. Check the model server at CODEHELPER_EMBED_URL if you expected multilingual matching."
}
