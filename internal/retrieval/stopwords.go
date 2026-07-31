package retrieval

import "strings"

// commonWords are high-frequency English tokens that carry no code-identity
// signal: grammatical filler, imperative task verbs ("add", "create"), and —
// critically — vague modifiers ("hot", "fast", "simple"). The modifiers are the
// dangerous case: they are RARE in a code corpus, so BM25 hands them an inflated
// IDF and a single incidental match drives the whole ranking. "add an LRU cache
// for hot searches" ranked a snapshot helper #1 because "hot" trigrams into
// "snap-shot". These words are DEMOTED, never removed — a symbol genuinely named
// HotPath still matches on its other, meaningful tokens; they just can't be the
// word that decides the ranking.
//
// This is the single source of truth for "non-discriminating word" across the
// retrieval layer AND the plan/scout subject extractor (via IsCommonWord), so
// every tool parses a task the same way.
var commonWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		a an the this that these those it its
		i we you my our us me your
		way ways able lets allow let
		where how why when what which who whom whose whats hows
		stuff thing things thingy somehow someway whatever lol pls plz
		broke broken help doesnt dont cant wont
		add create implement build make made making write writing new feature
		support enable disable improve change update fix refactor remove delete
		want wanna wanna need needs should could would can please lets let
		to for of in on at by with into from up out off as so and or but if
		is are be been being do does did done use using used via able
		hot cold warm fast slow quick quickly simple simply easy easily hard
		basic main full real proper properly clean nice better best good bad
		big small large tiny huge smart robust seamless lightweight efficient
		efficiently optimal optimized cleanly nicely fancy modern legacy
	`) {
		m[w] = true
	}
	return m
}()

// IsCommonWord reports whether w is a non-discriminating English word (filler,
// imperative verb, or vague modifier). Exported so the plan/scout subject
// extractor strips the same set the retrieval ranker demotes — one definition,
// consistent parsing across query/scout/plan.
func IsCommonWord(w string) bool {
	return commonWords[strings.ToLower(strings.TrimSpace(w))]
}

// meaningfulQueryTokens keeps only the distinctive tokens — the nouns/verbs that
// actually identify the target — by dropping common words and tokens under 3
// chars, de-duplicated in order. Used for coverage scoring, name-field matching,
// and trigram generation so incidental filler can never drive a result.
func meaningfulQueryTokens(toks []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range toks {
		t = strings.ToLower(strings.TrimSpace(t))
		if len(t) < 3 || commonWords[t] || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}
