package retrieval

import (
	"strings"

	"github.com/VeyrForge/codehelper/internal/vocab"
)

// vocabExpansionWeight discounts project-vocabulary expansions below literally-typed
// query terms but above generic synonym noise — the vocab seed is project-specific
// signal mined from the symbol graph.
const vocabExpansionWeight = 0.35

// expandVocabTerms returns extra search tokens drawn from the index-time vocabulary
// seed (.codehelper/vocab.json). When a query token fuzzy-matches a frequent project
// term or identifier sub-word, that term is added at vocabExpansionWeight so domain
// jargon (TP_Plugin, WooCommerce hooks, internal acronyms) surfaces symbols the
// generic synonym clusters miss.
func expandVocabTerms(repoRoot string, toks []string) map[string]float64 {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return nil
	}
	v, err := vocab.Load(repoRoot)
	if err != nil || (len(v.Terms) == 0 && len(v.Identifiers) == 0) {
		return nil
	}
	out := map[string]float64{}
	for _, t := range toks {
		lt := strings.ToLower(strings.TrimSpace(t))
		if len(lt) < 3 || IsCommonWord(lt) {
			continue
		}
		added := 0
		for _, tc := range v.Terms {
			if !vocabTermMatches(lt, tc.Term) {
				continue
			}
			if _, ok := out[tc.Term]; !ok {
				out[tc.Term] = vocabExpansionWeight
				added++
			}
			if added >= 4 {
				break
			}
		}
		for _, id := range v.Identifiers {
			for _, sub := range vocab.SplitIdentifier(id.Text) {
				if vocabTermMatches(lt, sub) {
					if _, ok := out[sub]; !ok {
						out[sub] = vocabExpansionWeight
					}
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func vocabTermMatches(queryTok, vocabTerm string) bool {
	queryTok = strings.ToLower(strings.TrimSpace(queryTok))
	vocabTerm = strings.ToLower(strings.TrimSpace(vocabTerm))
	if len(queryTok) < 3 || vocabTerm == "" {
		return false
	}
	if queryTok == vocabTerm {
		return true
	}
	if strings.Contains(vocabTerm, queryTok) {
		return true
	}
	if len(vocabTerm) >= 3 && strings.Contains(queryTok, vocabTerm) {
		return true
	}
	return false
}

func mergeTokenExpansions(searchToks *[]string, weights map[string]float64, extra map[string]float64) {
	for term, w := range extra {
		term = strings.ToLower(term)
		if term == "" {
			continue
		}
		if cur, ok := weights[term]; ok {
			if w > cur {
				weights[term] = w
			}
			continue
		}
		weights[term] = w
		*searchToks = append(*searchToks, term)
	}
}
