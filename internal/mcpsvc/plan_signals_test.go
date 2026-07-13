package mcpsvc

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func anyHasSub(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestDetectDomains_FromTaskAndPaths(t *testing.T) {
	names := func(rs []domainRule) []string {
		var o []string
		for _, r := range rs {
			o = append(o, r.name)
		}
		return o
	}
	// From task text.
	if got := names(detectDomains("add login session with a jwt token", nil)); !anyHasSub(got, "auth") {
		t.Errorf("expected auth domain, got %v", got)
	}
	// Newbie phrasings: spaced "log in" / "sign up" must also trigger auth.
	if got := names(detectDomains("add a way for users to log in", nil)); !anyHasSub(got, "auth") {
		t.Errorf("expected auth domain for 'log in', got %v", got)
	}
	if got := names(detectDomains("let visitors sign up for an account", nil)); !anyHasSub(got, "auth") {
		t.Errorf("expected auth domain for 'sign up', got %v", got)
	}
	if got := names(detectDomains("validate the checkout payment amount", nil)); !anyHasSub(got, "payments") {
		t.Errorf("expected payments domain, got %v", got)
	}
	if got := names(detectDomains("run a shell subprocess", nil)); !anyHasSub(got, "command-exec") {
		t.Errorf("expected command-exec domain, got %v", got)
	}
	// From candidate path even when the task wording is neutral.
	if got := names(detectDomains("make it faster", []string{"internal/auth/login.go:12"})); !anyHasSub(got, "auth") {
		t.Errorf("expected auth domain from path, got %v", got)
	}
	// No false positive: "db" inside "adblock" must not trigger database.
	if got := names(detectDomains("improve the adblock list rendering", nil)); anyHasSub(got, "database") {
		t.Errorf("false positive database domain: %v", got)
	}
}

func TestDeriveDecisionPoints_GroundedInSignals(t *testing.T) {
	top := &reuseCandidate{Name: "Cache", Loc: "internal/cache/cache.go:10", Callers: 23}
	sig := taskSignals{
		Top: top, RiskTier: "high", Dependents: 30, TestsOnTop: 0, PkgsSpanned: 4,
		Domains: detectDomains("add auth caching", nil),
	}
	dp := deriveDecisionPoints("feature", sig)
	if !anyHasSub(dp, "23 callers") {
		t.Errorf("expected caller count in decision points: %v", dp)
	}
	if !anyHasSub(dp, "risk: high") {
		t.Errorf("expected risk tier in decision points: %v", dp)
	}
	if !anyHasSub(dp, "No tests cover") {
		t.Errorf("expected missing-test warning: %v", dp)
	}
	if !anyHasSub(dp, "4 packages") {
		t.Errorf("expected cross-package warning: %v", dp)
	}
	if !anyHasSub(dp, "AUTH") {
		t.Errorf("expected auth question: %v", dp)
	}
}

func TestDeriveDecisionPoints_NoTopStillHasRoleQuestions(t *testing.T) {
	dp := deriveDecisionPoints("security", taskSignals{})
	if len(dp) == 0 {
		t.Fatal("expected role decision points even with no top candidate")
	}
}

func TestDerivePlacement_DumpingGroundVsCohesive(t *testing.T) {
	dump := derivePlacement(taskSignals{Top: &reuseCandidate{Name: "Helper", Loc: "internal/util/helper.go:3"}})
	if !anyHasSub(dump, "catch-all") {
		t.Errorf("expected catch-all warning for util package: %v", dump)
	}
	good := derivePlacement(taskSignals{Top: &reuseCandidate{Name: "Cache", Loc: "internal/cache/cache.go:3"}})
	if !anyHasSub(good, "Co-locate") {
		t.Errorf("expected co-locate suggestion for cohesive package: %v", good)
	}
	none := derivePlacement(taskSignals{})
	if !anyHasSub(none, "focused package") {
		t.Errorf("expected new-package guidance when no match: %v", none)
	}
}

func TestDeriveDuplication_FlagsSubjectNameOverlap(t *testing.T) {
	cands := []reuseCandidate{
		{Name: "NewCache", Loc: "internal/cache/cache.go:1", Callers: 5},
		{Name: "unrelatedThing", Loc: "x.go:1"},
	}
	dup := deriveDuplication("add an LRU cache layer", cands)
	if !anyHasSub(dup, "NewCache") || !anyHasSub(dup, "cache") {
		t.Errorf("expected duplication flag naming NewCache/cache: %v", dup)
	}
	// No overlap -> no flag.
	if d := deriveDuplication("render the dashboard", cands); len(d) != 0 {
		t.Errorf("expected no duplication flag, got %v", d)
	}
}

func TestPkgOfLocAndDistinctPkgs(t *testing.T) {
	if got := pkgOfLoc("internal/cache/cache.go:42"); got != "internal/cache" {
		t.Errorf("pkgOfLoc: got %q", got)
	}
	nodes := []types.ImpactNode{
		{Path: "internal/a/x.go"}, {Path: "internal/a/y.go"}, {Path: "internal/b/z.go"},
	}
	if got := distinctPkgs(nodes); got != 2 {
		t.Errorf("distinctPkgs: got %d want 2", got)
	}
}

func TestRelevantDocs_MatchesDependencyInTask(t *testing.T) {
	deps := []string{"golang.org/x/time@v0.5.0", "github.com/spf13/cobra@v1.10.2"}
	got := relevantDocs("add rate limiting using cobra commands", deps)
	if !anyHasSub(got, "cobra") {
		t.Errorf("expected cobra in relevant docs: %v", got)
	}
	// Unrelated task -> no docs.
	if d := relevantDocs("rename a variable", deps); len(d) != 0 {
		t.Errorf("expected no relevant docs, got %v", d)
	}
}
