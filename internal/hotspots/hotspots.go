// Package hotspots ranks files by architectural risk = git churn × call-graph
// centrality. The insight (from code-health research, e.g. CodeScene's
// "hotspots"): a file that is BOTH changed often AND depended on heavily is where
// defects are most likely and most costly. High churn alone is just active code;
// high centrality alone is stable infrastructure; their PRODUCT isolates the
// risky/refactor-worthy core. Everything here is pure and deterministic — the
// caller supplies the two signals (commit counts and inbound-edge counts), so the
// ranking has no I/O and is trivially unit-tested.
package hotspots

import (
	"math"
	"sort"
)

// FileScore is one ranked file: how often it changes (commits in the window), how
// load-bearing it is (summed inbound call edges to the symbols it defines), and
// the combined, repo-relative risk score in [0,1].
type FileScore struct {
	File       string  `json:"file"`
	Commits    int     `json:"commits"`
	Centrality int     `json:"centrality"`
	Score      float64 `json:"score"`
}

// Rank fuses churn (file → commits touching it) and centrality (file → summed
// inbound edges) into a risk ranking. Both axes are damped with log1p (a file
// changed 100× is not 100× riskier than one changed 10×) and normalized to [0,1]
// against the repo's own maximum, so scores are relative to THIS codebase rather
// than an absolute scale that means nothing across repos. The score is the product
// of the two normalized axes: a file scores high only when it is both churned and
// central. Files missing either signal (a config with no inbound edges, a central
// file nobody has touched in the window) score 0 and are dropped. Results are
// sorted best-first and capped at topK (topK <= 0 means no cap).
func Rank(churn, centrality map[string]int, topK int) []FileScore {
	maxChurn, maxCent := 0.0, 0.0
	for _, c := range churn {
		if l := math.Log1p(float64(c)); l > maxChurn {
			maxChurn = l
		}
	}
	for _, d := range centrality {
		if l := math.Log1p(float64(d)); l > maxCent {
			maxCent = l
		}
	}
	if maxChurn == 0 || maxCent == 0 {
		return nil // one of the signals is entirely absent — no meaningful hotspot
	}

	var out []FileScore
	for file, commits := range churn {
		cent, ok := centrality[file]
		if !ok || commits <= 0 || cent <= 0 {
			continue
		}
		nChurn := math.Log1p(float64(commits)) / maxChurn
		nCent := math.Log1p(float64(cent)) / maxCent
		score := nChurn * nCent
		if score <= 0 {
			continue
		}
		out = append(out, FileScore{File: file, Commits: commits, Centrality: cent, Score: score})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].File < out[j].File // stable tie-break for deterministic output
	})
	if topK > 0 && len(out) > topK {
		out = out[:topK]
	}
	return out
}

// ChurnFromCommits counts, for each file, how many of the given commits touched it.
// commits is the per-commit file lists from `git log --name-only` (newest first);
// the count is a simple change-frequency proxy that needs no blame/line data.
func ChurnFromCommits(commits [][]string) map[string]int {
	churn := map[string]int{}
	for _, files := range commits {
		// A file listed twice in one commit (rename edge cases) still counts once.
		seen := map[string]struct{}{}
		for _, f := range files {
			if _, dup := seen[f]; dup {
				continue
			}
			seen[f] = struct{}{}
			churn[f]++
		}
	}
	return churn
}
