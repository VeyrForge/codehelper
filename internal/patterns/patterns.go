// Package patterns loads feature pattern packs for expand_request (deterministic).
package patterns

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/questiongate"
)

//go:embed packs/*.json
var bundled embed.FS

// Pack is a reusable engineering checklist (modal, ajax form, etc.).
type Pack struct {
	ID           string   `json:"id"`
	Triggers     []string `json:"triggers"`
	Requirements []string `json:"requirements"`
	Security     []string `json:"security,omitempty"`
	Performance  []string `json:"performance,omitempty"`
	Verify       []string `json:"verify,omitempty"`
}

// ExpandInput is CLI/MCP input for expansion.
type ExpandInput struct {
	Request     string `json:"request"`
	ProjectType string `json:"project_type,omitempty"`
	ChangedArea string `json:"changed_area,omitempty"` // frontend|backend|fullstack
}

// ExpandOutput is the structured expansion for LLM/orchestrator prompts.
type ExpandOutput struct {
	Intent                  string   `json:"intent"`
	FeatureType             string   `json:"feature_type"`
	PatternID               string   `json:"pattern_id,omitempty"`
	InferredRequirements    []string `json:"inferred_requirements"`
	RequiredContext         []string `json:"required_context"`
	RequiredTools           []string `json:"required_tools"`
	RiskChecks              []string `json:"risk_checks"`
	AskUser                 bool     `json:"ask_user"`
	AskReason               string   `json:"ask_reason,omitempty"`
	PerformanceHints        []string `json:"performance_hints,omitempty"`
	VerificationSuggestions []string `json:"verification_suggestions,omitempty"`
}

// LoadAll returns bundled packs merged with repo .codehelper/patterns/*.json (later overrides same id).
func LoadAll(repoRoot string) ([]Pack, error) {
	byID := map[string]Pack{}
	if err := loadEmbedded(byID); err != nil {
		return nil, err
	}
	dir := filepath.Join(paths.RepoIndexDir(repoRoot), "patterns")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return sortPacks(byID), nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var p Pack
		if json.Unmarshal(b, &p) != nil || strings.TrimSpace(p.ID) == "" {
			continue
		}
		byID[p.ID] = p
	}
	return sortPacks(byID), nil
}

func loadEmbedded(byID map[string]Pack) error {
	return fs.WalkDir(bundled, "packs", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := bundled.ReadFile(path)
		if err != nil {
			return err
		}
		var p Pack
		if json.Unmarshal(b, &p) != nil || strings.TrimSpace(p.ID) == "" {
			return nil
		}
		if _, ok := byID[p.ID]; !ok {
			byID[p.ID] = p
		}
		return nil
	})
}

func sortPacks(byID map[string]Pack) []Pack {
	keys := make([]string, 0, len(byID))
	for k := range byID {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Pack, 0, len(keys))
	for _, k := range keys {
		out = append(out, byID[k])
	}
	return out
}

// BundledPackJSON returns raw JSON for install command.
func BundledPackJSON(id string) ([]byte, error) {
	name := strings.TrimSuffix(strings.TrimSpace(id), ".json")
	return bundled.ReadFile("packs/" + name + ".json")
}

// SelectPattern picks the highest-scoring pack for the request.
func SelectPattern(request string, projectType string, packs []Pack) (Pack, float64) {
	if len(packs) == 0 {
		return Pack{}, 0
	}
	req := strings.ToLower(strings.TrimSpace(request))
	var best Pack
	var bestScore float64
	for _, p := range packs {
		sc := scorePack(req, projectType, p)
		if sc > bestScore {
			bestScore = sc
			best = p
		}
	}
	return best, bestScore
}

func scorePack(reqLower string, projectType string, p Pack) float64 {
	var score float64
	for _, t := range p.Triggers {
		tl := strings.ToLower(strings.TrimSpace(t))
		if tl == "" {
			continue
		}
		if strings.Contains(reqLower, tl) {
			score += float64(len(tl)) / 10.0
			if len(tl) >= 4 {
				score += 0.5
			}
		}
	}
	if score == 0 && len(p.Triggers) > 0 {
		toks := strings.Fields(reqLower)
		for _, tok := range toks {
			for _, t := range p.Triggers {
				if strings.Contains(strings.ToLower(t), tok) && len(tok) >= 4 {
					score += 0.2
				}
			}
		}
	}
	_ = projectType
	return score
}

// ExpandRequest builds the orchestrator payload from a pattern + heuristics.
func ExpandRequest(in ExpandInput, packs []Pack) ExpandOutput {
	p, score := SelectPattern(in.Request, in.ProjectType, packs)
	featureType := classifyFeatureType(in.Request)
	out := ExpandOutput{
		Intent:               classifyIntent(in.Request),
		FeatureType:          featureType,
		RiskChecks:           []string{"accessibility", "security", "state_management", "performance", "style_consistency"},
		RequiredTools:        []string{"query", "context", "impact"},
		RequiredContext:      defaultRequiredContext(in),
		InferredRequirements: []string{},
	}
	if score <= 0 || p.ID == "" {
		out.InferredRequirements = fallbackRequirements(featureType)
		qg := questiongate.Evaluate(questiongate.Input{
			Task:              in.Request,
			ProposedQuestions: []string{"What stack or deployment constraints apply?"},
		}, nil)
		out.AskUser = qg.AskUser
		out.AskReason = qg.Reason
		return out
	}
	out.PatternID = p.ID
	out.FeatureType = p.ID
	var inf []string
	inf = append(inf, p.Requirements...)
	inf = append(inf, prefixAll("security: ", p.Security)...)
	inf = append(inf, prefixAll("performance: ", p.Performance)...)
	out.InferredRequirements = dedupeStrings(inf)
	out.PerformanceHints = append(out.PerformanceHints, p.Performance...)
	out.VerificationSuggestions = append(out.VerificationSuggestions, p.Verify...)
	out.RequiredTools = dedupeStrings(append(out.RequiredTools,
		"context", "impact", "review_diff", "security_context", "architecture_lint"))
	if strings.Contains(strings.ToLower(in.ChangedArea), "front") || strings.Contains(strings.ToLower(in.Request), "modal") {
		out.RequiredContext = dedupeStrings(append(out.RequiredContext,
			"existing modal components",
			"theme JS entrypoints",
			"CSS conventions",
		))
	}
	if strings.Contains(strings.ToLower(in.ProjectType), "wordpress") {
		out.RequiredContext = dedupeStrings(append(out.RequiredContext,
			"form/AJAX handlers",
			"translation functions",
		))
		out.RequiredTools = dedupeStrings(append(out.RequiredTools, "contract_guard"))
	}
	out.AskUser = false
	out.AskReason = "pattern pack supplies checklist; use question_gate for extra user prompts"
	return out
}

func classifyIntent(req string) string {
	l := strings.ToLower(req)
	switch {
	case strings.Contains(l, "auth"), strings.Contains(l, "login"), strings.Contains(l, "oauth"), strings.Contains(l, "session"):
		return "change_auth"
	case strings.Contains(l, "popup"), strings.Contains(l, "modal"), strings.Contains(l, "dialog"):
		return "add_ui_feature"
	case strings.Contains(l, "api"), strings.Contains(l, "endpoint"), strings.Contains(l, "rest"), strings.Contains(l, "webhook"):
		return "add_api"
	case strings.Contains(l, "migration"), strings.Contains(l, "schema"), strings.Contains(l, "alter table"):
		return "data_migration"
	case strings.Contains(l, "payment"), strings.Contains(l, "billing"), strings.Contains(l, "checkout"), strings.Contains(l, "invoice"), strings.Contains(l, "stripe"), strings.Contains(l, "subscription"):
		return "change_payment"
	case strings.Contains(l, "queue"), strings.Contains(l, "worker"), strings.Contains(l, "cron"), strings.Contains(l, "background"), strings.Contains(l, " job"):
		return "background_job"
	case strings.Contains(l, "cache"), strings.Contains(l, "caching"), strings.Contains(l, "redis"), strings.Contains(l, "memcached"):
		return "change_caching"
	default:
		return "engineering_change"
	}
}

func classifyFeatureType(req string) string {
	l := strings.ToLower(req)
	switch {
	case strings.Contains(l, "auth"), strings.Contains(l, "login"), strings.Contains(l, "oauth"), strings.Contains(l, "session"), strings.Contains(l, "permission"), strings.Contains(l, "role"):
		return "auth"
	case strings.Contains(l, "payment"), strings.Contains(l, "billing"), strings.Contains(l, "checkout"), strings.Contains(l, "invoice"), strings.Contains(l, "stripe"), strings.Contains(l, "subscription"):
		return "payment"
	case strings.Contains(l, "cache"), strings.Contains(l, "caching"), strings.Contains(l, "redis"), strings.Contains(l, "memcached"):
		return "caching"
	case strings.Contains(l, "queue"), strings.Contains(l, "worker"), strings.Contains(l, "cron"), strings.Contains(l, "background"), strings.Contains(l, " job"):
		return "background_job"
	case strings.Contains(l, "api"), strings.Contains(l, "endpoint"), strings.Contains(l, "rest"), strings.Contains(l, "graphql"), strings.Contains(l, "webhook"):
		return "api"
	case strings.Contains(l, "migration"), strings.Contains(l, "schema"), strings.Contains(l, "database"), strings.Contains(l, "sql"), strings.Contains(l, "alter table"):
		return "data"
	case strings.Contains(l, "security"), strings.Contains(l, "csrf"), strings.Contains(l, "xss"), strings.Contains(l, "injection"):
		return "security"
	case strings.Contains(l, "popup"), strings.Contains(l, "modal"), strings.Contains(l, "dialog"), strings.Contains(l, " ui"), strings.Contains(l, "form"):
		return "ui"
	default:
		return "general"
	}
}

func fallbackRequirements(featureType string) []string {
	common := []string{
		"Clarify scope and affected surfaces.",
		"Preserve existing public contracts.",
		"Validate inputs; fail safely.",
	}
	switch featureType {
	case "auth":
		return append([]string{
			"Identify existing authentication/session boundary.",
			"Preserve current login/logout/session behavior unless explicitly changing it.",
			"Add authorization checks at every protected entrypoint.",
			"Hash passwords with a vetted KDF; never store reversible secrets.",
			"Add regression tests for allowed and denied access.",
		}, common...)
	case "api":
		return append([]string{
			"Identify request/response contracts and error shape.",
			"Validate inputs at the boundary; return consistent error envelopes.",
			"Preserve pagination, limits, and compatibility for existing clients.",
			"Add tests covering happy, validation-error, and unauthorized paths.",
		}, common...)
	case "data":
		return append([]string{
			"Identify schema/data ownership and rollback path.",
			"Preserve existing persisted data semantics.",
			"Wrap multi-step writes in a transaction where applicable.",
			"Avoid destructive DDL without an explicit deprecation window.",
		}, common...)
	case "security":
		return append([]string{
			"Identify trust boundaries and permission checks.",
			"Avoid exposing secrets or PII in responses/logs.",
			"Add regression coverage for denied paths.",
		}, common...)
	case "payment":
		return append([]string{
			"Treat charges and refunds as transactional, idempotent operations.",
			"Verify webhook signatures and deduplicate by provider event id.",
			"Store monetary values in minor units; never float.",
		}, common...)
	case "caching":
		return append([]string{
			"Define explicit TTL and invalidation event for every cached value.",
			"Namespace cache keys per environment/tenant.",
			"Keep the system correct without the cache.",
		}, common...)
	case "background_job":
		return append([]string{
			"Make every job idempotent and safe to retry.",
			"Bound retries with backoff and a dead-letter destination.",
			"Add timeouts; jobs must not hang the worker.",
		}, common...)
	case "ui":
		return append([]string{
			"Reuse existing component conventions and design tokens.",
			"Cover keyboard, focus, and screen-reader accessibility.",
			"Debounce or throttle expensive handlers.",
		}, common...)
	default:
		return common
	}
}

func defaultRequiredContext(in ExpandInput) []string {
	return []string{
		"files touched by related symbols",
		"tests or manual verification path",
	}
}

func prefixAll(prefix string, xs []string) []string {
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		out = append(out, prefix+x)
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
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
