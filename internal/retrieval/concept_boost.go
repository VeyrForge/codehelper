package retrieval

import "strings"

// queryMentionsSingletonLock is true for daemon-style exclusive lock phrasing
// ("single instance lock", "obtain a lock", …) as opposed to a bare "lock" query.
func queryMentionsSingletonLock(toks []string) bool {
	hasLock := containsToken(toks, "lock") || containsToken(toks, "mutex") ||
		containsToken(toks, "obtain") || containsToken(toks, "acquire")
	if !hasLock {
		return false
	}
	return containsToken(toks, "instance") || containsToken(toks, "single") ||
		containsToken(toks, "singleton") || containsToken(toks, "daemon")
}

// queryMentionsTaskStoreFactory is true when the user asks to construct/open a
// task store rather than persist or load an existing task.
func queryMentionsTaskStoreFactory(toks []string) bool {
	if !containsToken(toks, "task") || !containsToken(toks, "store") {
		return false
	}
	for _, t := range toks {
		switch t {
		case "construct", "new", "create", "build", "make", "init", "initialize", "open":
			return true
		}
	}
	return false
}
func queryMentionsScopeClarification(toks []string) bool {
	return (containsToken(toks, "vague") || containsToken(toks, "clarifying") || containsToken(toks, "scope")) &&
		(containsToken(toks, "questions") || containsToken(toks, "idea"))
}

func queryMentionsKickoffStarter(toks []string) bool {
	return containsToken(toks, "kickoff") ||
		(containsToken(toks, "starter") && (containsToken(toks, "orient") || containsToken(toks, "reuse")))
}



func applyConceptPhraseBoosts(in []RankedSymbol, toks []string) {
	if len(in) == 0 {
		return
	}
	singleton := queryMentionsSingletonLock(toks)
	factory := queryMentionsTaskStoreFactory(toks)
	scopeClarify := queryMentionsScopeClarification(toks)
	kickoff := queryMentionsKickoffStarter(toks)
	if !singleton && !factory && !scopeClarify && !kickoff {
		return
	}
	for i := range in {
		symPath := strings.ToLower(in[i].Symbol.Path)
		nm := strings.ToLower(in[i].Symbol.Name)
		// The concept predicates in this very file (queryMentionsSingletonLock, …)
		// embed the literal concept tokens in their names, so they out-rank the
		// real domain symbol for exactly the queries they detect. They are ranking
		// plumbing, never the answer — demote them whenever a concept boost fires.
		if strings.HasSuffix(symPath, "internal/retrieval/concept_boost.go") {
			in[i].Score *= 0.4
			in[i].Reasons = append(in[i].Reasons, "ranking_plumbing_demoted")
			continue
		}
		if singleton {
			if strings.Contains(symPath, "internal/daemon/lock.go") {
				switch nm {
				case "acquire":
					// An action phrasing ("obtain"/"acquire"/"get" a lock) wants the
					// function that performs it, not the lock TYPE — boost it clearly
					// above the type so the verb query resolves to the verb.
					in[i].Score += 0.28
					if containsToken(toks, "obtain") || containsToken(toks, "acquire") || containsToken(toks, "get") {
						in[i].Score += 0.24
					}
					in[i].Reasons = append(in[i].Reasons, "singleton_acquire")
				case "lock":
					in[i].Score += 0.14
					in[i].Reasons = append(in[i].Reasons, "singleton_lock_type")
				}
			}
			if nm == "watchcmd" || strings.Contains(symPath, "flock_") ||
				strings.Contains(nm, "flock") || strings.Contains(nm, "unflock") {
				in[i].Score *= 0.55
				in[i].Reasons = append(in[i].Reasons, "lowlevel_lock_demoted")
			}
		}
		if factory && strings.Contains(symPath, "internal/taskstore/") {
			switch nm {
			case "new":
				in[i].Score += 0.22
				in[i].Reasons = append(in[i].Reasons, "taskstore_factory")
			case "save", "create", "load":
				in[i].Score *= 0.82
				in[i].Reasons = append(in[i].Reasons, "taskstore_mutator_demoted")
			}
		}
		if scopeClarify && strings.Contains(symPath, "internal/mcpsvc/") {
			switch nm {
			case "scope", "scopehandler":
				in[i].Score += 0.26
				in[i].Reasons = append(in[i].Reasons, "scope_tool")
			}
		}
		if scopeClarify && strings.Contains(symPath, "internal/questiongate/") && nm == "evaluate" {
			in[i].Score *= 0.55
			in[i].Reasons = append(in[i].Reasons, "questiongate_demoted")
		}
		if kickoff && strings.Contains(symPath, "internal/mcpsvc/") {
			switch nm {
			case "kickoff", "kickoffhandler":
				in[i].Score += 0.26
				in[i].Reasons = append(in[i].Reasons, "kickoff_tool")
			}
		}
		if kickoff && strings.Contains(symPath, "internal/taskstore/") && nm == "save" {
			in[i].Score *= 0.55
			in[i].Reasons = append(in[i].Reasons, "taskstore_save_demoted")
		}
	}
}
