package mcpsvc

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// This file turns plan/kickoff from STATIC role checklists into TASK-SPECIFIC
// guidance derived from real index signals: the risk tier & caller count of the
// closest match, the security domain the task touches, whether tests cover the
// target, whether the blast radius crosses packages, where the code should live,
// and whether it would duplicate something that already exists. The point is to
// "help the LLM think" with the questions a senior reviewer would actually ask
// for THIS change — not a generic list.

// domainRule maps a sensitive domain to the pointed question and the extra
// consideration to surface when the task (or the closest existing code) touches
// it. Keywords are matched against the task text AND the candidate paths/names,
// so "edit the checkout total" and a hit in internal/billing both trigger it.
type domainRule struct {
	name     string
	keywords []string
	question string
	consider string
}

var domainRules = []domainRule{
	{"auth", []string{"auth", "login", "log in", "logout", "log out", "sign in", "signin", "sign up", "signup", "register", "session", "token", "jwt", "password", "credential", "permission", "role", "oauth", "rbac", "authz", "authn", "2fa", "mfa"},
		"Touches AUTH: where is the trust boundary, and is the authorization check enforced server-side (not just in the UI)?",
		"AuthZ enforced server-side; fail closed; don't leak whether a user/resource exists."},
	{"payments", []string{"payment", "billing", "charge", "invoice", "checkout", "stripe", "paypal", "refund", "subscription", "price", "money", "currency"},
		"Touches PAYMENTS: is the amount/currency validated server-side and the operation idempotent (no double-charge on retry)?",
		"Payments: idempotency keys; validate amounts server-side; never trust client totals; audit-log money movement."},
	{"database", []string{"sql", "db", "database", "migration", "migrate", "schema", "table", "orm", "rows", "transaction"},
		"Touches the DATABASE: are all queries parameterized, and does any schema/migration change stay reversible and backfill-safe?",
		"Parameterize every query; migrations reversible + backfill-safe; bound result sets."},
	{"command-exec", []string{"exec", "shell", "subprocess", "spawn", "bash", "system", "popen", "argv"},
		"Runs EXTERNAL COMMANDS: is the argv built WITHOUT shell interpolation of untrusted input (no command injection)?",
		"Use argv (no shell string); never interpolate untrusted input; allowlist binaries."},
	{"crypto/secrets", []string{"crypto", "encrypt", "decrypt", "secret", "apikey", "hash", "sign", "signature", "cert", "tls", "ssl", "nonce"},
		"Touches CRYPTO/SECRETS: using a vetted library (not hand-rolled), and are secrets kept out of code and logs?",
		"Use standard crypto libs; crypto/rand for security randomness; never log or commit secrets."},
	{"io/network", []string{"http", "https", "request", "url", "upload", "download", "filepath", "socket", "fetch", "webhook", "ssrf"},
		"Does external I/O: are paths/URLs/sizes validated and bounded (no path traversal, SSRF, or unbounded reads)?",
		"Validate/bound external inputs; prevent path traversal & SSRF; set timeouts; cap sizes."},
	{"concurrency", []string{"goroutine", "concurrent", "parallel", "mutex", "lock", "atomic", "race", "async", "channel", "worker"},
		"Is CONCURRENT: what shared state is touched, and is every access synchronized (does it pass -race)?",
		"Protect shared state; avoid deadlocks; verify with the race detector."},
}

// detectDomains returns the domain rules whose keywords appear in the task text
// or any supplied path/name. Deterministic, order-stable (declaration order).
func detectDomains(task string, paths []string) []domainRule {
	hay := " " + strings.ToLower(task) + " "
	for _, p := range paths {
		hay += " " + strings.ToLower(p) + " "
	}
	var out []domainRule
	for _, r := range domainRules {
		for _, kw := range r.keywords {
			// word-ish containment: surround with separators to avoid "db" in "adblock".
			if strings.Contains(hay, " "+kw) || strings.Contains(hay, kw+" ") || strings.Contains(hay, "/"+kw) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// taskSignals is everything derived once and shared by decision_points,
// considerations, placement, and duplication.
type taskSignals struct {
	Top         *reuseCandidate // closest existing match (nil if none)
	RiskTier    string          // risk tier of Top's blast radius
	Dependents  int             // how many symbols depend on Top
	TestsOnTop  int             // tests covering Top (0 = untested)
	PkgsSpanned int             // distinct packages in Top's blast radius
	Domains     []domainRule    // sensitive domains the task/candidates touch
}

// deriveDecisionPoints builds the "this or that" the LLM/user must resolve,
// task-specific signals FIRST, then the role-generic ones. Each is grounded in a
// real number so it reads like a reviewer who has seen the code, not a template.
func deriveDecisionPoints(role string, sig taskSignals) []string {
	var dp []string
	if t := sig.Top; t != nil {
		risk := sig.RiskTier
		if risk == "" {
			risk = "unknown"
		}
		dp = append(dp, fmt.Sprintf(
			"Extend `%s` (%d callers, risk: %s) or add a new symbol? Extending changes behavior for its %d dependents; new code avoids that but risks duplicating logic.",
			t.Name, t.Callers, risk, sig.Dependents))
		if t.Callers > 0 {
			dp = append(dp, fmt.Sprintf(
				"`%s` has %d callers — is backward compatibility required, or is a breaking change acceptable (and all %d sites updated)?",
				t.Name, t.Callers, t.Callers))
		}
		if sig.TestsOnTop == 0 {
			dp = append(dp, fmt.Sprintf(
				"No tests cover `%s` — add characterization tests BEFORE changing its behavior, or is this purely additive?",
				t.Name))
		}
		if sig.PkgsSpanned >= 3 {
			dp = append(dp, fmt.Sprintf(
				"Changing `%s` ripples across %d packages — acceptable, or isolate the change behind a seam?",
				t.Name, sig.PkgsSpanned))
		}
	}
	for _, d := range sig.Domains {
		dp = append(dp, d.question)
	}
	dp = append(dp, roleDecisionPoints(role)...)
	return dedupeStrings(dp)
}

// deriveConsiderations augments the role checklist with the domain-specific
// considerations the task actually warrants.
func deriveConsiderations(role string, sig taskSignals) []string {
	out := roleConsiderations(role)
	for _, d := range sig.Domains {
		out = append(out, d.consider)
	}
	return dedupeStrings(out)
}

// dumpingGroundDirs are package names that tend to accumulate unrelated symbols;
// adding more to them worsens cohesion, so plan steers new code elsewhere.
var dumpingGroundDirs = map[string]bool{
	"util": true, "utils": true, "helper": true, "helpers": true,
	"common": true, "misc": true, "shared": true, "lib": true, "base": true,
}

// derivePlacement suggests WHERE new code should live, grounded in the closest
// match's package and the layout — component-based, dependency-inward guidance.
func derivePlacement(sig taskSignals) []string {
	var out []string
	if t := sig.Top; t != nil {
		pkg := pkgOfLoc(t.Loc)
		if pkg != "" {
			base := filepath.Base(pkg)
			if dumpingGroundDirs[strings.ToLower(base)] {
				out = append(out, fmt.Sprintf(
					"`%s` lives in `%s` — a catch-all package. Prefer a focused package named for the concern over piling onto %s/.",
					t.Name, pkg, base))
			} else {
				out = append(out, fmt.Sprintf(
					"Co-locate with `%s` in `%s` if extending it; if it's a genuinely new concern, a sibling package keeps cohesion.",
					t.Name, pkg))
			}
		}
	} else {
		out = append(out, "No close match — create a focused package owned by the layer responsible for this concern; keep the dependency direction inward (inner layers must not import outer).")
	}
	return out
}

// deriveDuplication flags reuse candidates whose name strongly overlaps the task
// subject — the concrete "you're about to rebuild something that exists" signal.
func deriveDuplication(task string, cands []reuseCandidate) []string {
	subj := taskSubjectTokens(task)
	if len(subj) == 0 {
		return nil
	}
	var out []string
	for i, c := range cands {
		if i >= 3 {
			break
		}
		nm := strings.ToLower(c.Name)
		var matched []string
		for _, s := range subj {
			// Require a reasonably distinctive token (>=5 chars) and skip generic
			// product nouns so "my site" doesn't flag DemoSiteSeeder as a duplicate.
			if len(s) >= 5 && !genericNoun[s] && strings.Contains(nm, s) {
				matched = append(matched, s)
			}
		}
		if len(matched) > 0 {
			out = append(out, fmt.Sprintf(
				"`%s` (%d callers) already names the task subject (%s) — extend/reuse it instead of adding a near-duplicate.",
				c.Name, c.Callers, strings.Join(matched, ", ")))
		}
	}
	return out
}

// genericNoun are vague product nouns that are too common to signal a real
// duplicate ("site", "website", "stuff"). Distinct from retrieval stopwords so
// it only affects the duplication heuristic, not ranking.
var genericNoun = map[string]bool{
	"site": true, "website": true, "stuff": true, "thing": true, "things": true,
	"system": true, "feature": true, "module": true, "section": true,
}

// pkgOfLoc extracts the directory (package) from a "path:line" loc string.
func pkgOfLoc(loc string) string {
	p := loc
	if i := strings.LastIndex(loc, ":"); i > 0 {
		p = loc[:i]
	}
	d := filepath.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

// distinctPkgs counts the distinct directories among impact nodes (a proxy for
// how many packages a change to the target would ripple through).
func distinctPkgs(nodes []types.ImpactNode) int {
	seen := map[string]struct{}{}
	for _, n := range nodes {
		seen[filepath.Dir(n.Path)] = struct{}{}
	}
	return len(seen)
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
