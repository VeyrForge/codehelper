// Package cochange mines "evolutionary coupling" from git history: files that
// repeatedly change in the SAME commit. This surfaces architectural dependencies
// the call graph cannot see — e.g. "registering a parser in registry_init.go also
// requires editing the walker in walk.go", even though neither calls the other.
//
// It is the deterministic answer to the documented AI-coding pitfall "the agent
// forgets to update a related file": before editing X, codehelper can say "in this
// repo, Y almost always changes with X." No model, no embeddings — just `git log`.
package cochange

import (
	"sort"

	"github.com/VeyrForge/codehelper/internal/gitutil"
)

// Rule is a directional coupling: when File changes, the queried file also tends to
// change. Support is the number of commits where both changed; Confidence is
// Support / (commits where the QUERIED file changed) — i.e. P(File | queried).
type Rule struct {
	File       string  `json:"file"`
	Support    int     `json:"support"`
	Confidence float64 `json:"confidence"`
}

// Options tunes the mining. Zero value uses sensible defaults.
type Options struct {
	MaxCommits        int     // history depth to scan (default 2000)
	MinSupport        int     // ignore pairs seen fewer times (default 3)
	MinConfidence     float64 // ignore weak rules (default 0.5)
	MaxFilesPerCommit int     // skip sweeping commits — format/rename/mass-refactor (default 25)
	TopN              int     // cap rules returned (default 6)
}

func (o Options) withDefaults() Options {
	if o.MaxCommits == 0 {
		o.MaxCommits = 2000
	}
	if o.MinSupport == 0 {
		o.MinSupport = 3
	}
	if o.MinConfidence == 0 {
		o.MinConfidence = 0.5
	}
	if o.MaxFilesPerCommit == 0 {
		o.MaxFilesPerCommit = 25
	}
	if o.TopN == 0 {
		o.TopN = 6
	}
	return o
}

// ForFile returns the files most coupled to target in git history, best-first.
// Best-effort: returns nil (not an error) when there is no usable history, so the
// caller can attach couplings when present and omit them otherwise.
func ForFile(root, target string, opts Options) []Rule {
	opts = opts.withDefaults()
	return fromCommits(commitsFromGit(root, opts.MaxCommits), target, opts)
}

func commitsFromGit(root string, maxCommits int) [][]string {
	commits, err := gitutil.LogNameOnly(root, maxCommits)
	if err != nil {
		return nil
	}
	return commits
}

// fromCommits is the pure core (testable without a git repo): given per-commit file
// lists, count co-changes with target and return confident, well-supported rules.
func fromCommits(commits [][]string, target string, opts Options) []Rule {
	opts = opts.withDefaults()
	targetChanged := 0
	co := map[string]int{}
	for _, files := range commits {
		if len(files) == 0 || len(files) > opts.MaxFilesPerCommit {
			continue
		}
		seen := map[string]bool{}
		hasTarget := false
		for _, f := range files {
			if seen[f] {
				continue
			}
			seen[f] = true
			if f == target {
				hasTarget = true
			}
		}
		if !hasTarget {
			continue
		}
		targetChanged++
		for f := range seen {
			if f != target {
				co[f]++
			}
		}
	}
	if targetChanged < opts.MinSupport {
		return nil
	}
	var rules []Rule
	for f, s := range co {
		if s < opts.MinSupport {
			continue
		}
		conf := float64(s) / float64(targetChanged)
		if conf < opts.MinConfidence {
			continue
		}
		rules = append(rules, Rule{File: f, Support: s, Confidence: conf})
	}
	// Deterministic order: confidence desc, then support desc, then path asc.
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Confidence != rules[j].Confidence {
			return rules[i].Confidence > rules[j].Confidence
		}
		if rules[i].Support != rules[j].Support {
			return rules[i].Support > rules[j].Support
		}
		return rules[i].File < rules[j].File
	})
	if len(rules) > opts.TopN {
		rules = rules[:opts.TopN]
	}
	return rules
}
