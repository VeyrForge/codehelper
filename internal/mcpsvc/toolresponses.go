package mcpsvc

import (
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/secrets"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// ambiguityGuard returns a one-line caution when the top two hits are near-tied, so
// the agent does NOT silently assume the #1 result — the most-documented AI-coding
// pitfall is over-trusting the first answer (devs review *less* when confident). It
// is DETERMINISTIC (no model, no latency) and emits ONLY when genuinely ambiguous,
// so the common clear-winner case stays token-free (no context bloat). Empty string
// means "clear enough — say nothing".
func ambiguityGuard(hits []retrieval.RankedSymbol) string {
	if len(hits) < 2 {
		return ""
	}
	top, second := hits[0].Score, hits[1].Score
	if top <= 0 {
		return ""
	}
	if (top-second)/top < 0.08 && !strings.EqualFold(hits[0].Symbol.Name, hits[1].Symbol.Name) {
		return fmt.Sprintf("ambiguous: top 2 hits (%s, %s) score within 8%% — don't assume the first; confirm with `context` before acting on one.",
			hits[0].Symbol.Name, hits[1].Symbol.Name)
	}
	return ""
}

type contextPackRetrievalMeta struct {
	RankedSymbolHits int    `json:"ranked_symbol_hits"`
	ContextPackLimit int    `json:"context_pack_limit"`
	CandidatePool    int    `json:"candidate_pool"`
	Note             string `json:"note,omitempty"`
}

// contextPackMCPResponse is structuredContent for context_pack.
type contextPackMCPResponse struct {
	ContextPack          *retrieval.ContextPack   `json:"context_pack"`
	Budgeted             *retrieval.ContextPackV2 `json:"budgeted,omitempty"`
	Freshness            freshness.Report         `json:"freshness"`
	CrossRepoCandidates  []registry.Entry         `json:"cross_repo_candidates,omitempty"`
	RetrievalMeta        contextPackRetrievalMeta `json:"retrieval_meta"`
	Warning              string                   `json:"warning,omitempty"`
	RecommendedNextTools []string                 `json:"recommended_next_tools,omitempty"`
	Warnings             []string                 `json:"warnings,omitempty"`
	EvidencePaths        []string                 `json:"evidence_paths,omitempty"`
	Confidence           float64                  `json:"confidence,omitempty"`
}

// impactMCPResponse is structuredContent for impact.
type impactMCPResponse struct {
	Impact               *types.ImpactResult `json:"impact"`
	Freshness            freshness.Report    `json:"freshness"`
	Warning              string              `json:"warning,omitempty"`
	Note                 string              `json:"note,omitempty"`
	RecommendedNextTools []string            `json:"recommended_next_tools,omitempty"`
	Warnings             []string            `json:"warnings,omitempty"`
	EvidencePaths        []string            `json:"evidence_paths,omitempty"`
	Confidence           float64             `json:"confidence,omitempty"`
}

// indexStatsBrief is a compact index footprint for bootstrap (from meta.json).
type indexStatsBrief struct {
	Files   int `json:"files,omitempty"`
	Symbols int `json:"symbols,omitempty"`
	Edges   int `json:"edges,omitempty"`
}

// dbConnBrief / sshHostBrief / connectionsBrief are the NON-SECRET view of a
// project's configured database and SSH profiles, surfaced in project_context so
// an agent knows what external systems the project talks to. Secrets (password
// refs) are intentionally omitted.
type dbConnBrief struct {
	Name      string `json:"name"`
	Driver    string `json:"driver"`
	Host      string `json:"host,omitempty"`
	Database  string `json:"database,omitempty"`
	SSHTunnel string `json:"ssh_tunnel,omitempty"`
	ReadOnly  bool   `json:"read_only,omitempty"`
	Enabled   bool   `json:"enabled"`
	HasSecret bool   `json:"has_secret"` // whether a password is configured — NOT the password
}

type sshHostBrief struct {
	Name            string   `json:"name"`
	Hostname        string   `json:"hostname"`
	JumpHost        string   `json:"jump_host,omitempty"`
	Enabled         bool     `json:"enabled"`
	AllowedCommands []string `json:"allowed_commands,omitempty"`
	Recipes         []string `json:"recipes,omitempty"`
}

type aliasBrief struct {
	Name             string `json:"name"`
	Local            bool   `json:"local,omitempty"`
	RemoteHost       string `json:"remote_host,omitempty"`
	RemoteRecipe     string `json:"remote_recipe,omitempty"`
	RequiresApproval bool   `json:"requires_approval,omitempty"`
}

type githubBrief struct {
	Repo     string `json:"repo,omitempty"`
	HasToken bool   `json:"has_token,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

type logSourceBrief struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	SSHHost string `json:"ssh_host,omitempty"`
	Enabled bool   `json:"enabled"`
}

type policyBrief struct {
	AgentTrust      string       `json:"agent_trust"`
	VerifyAllowlist []string     `json:"verify_allowlist,omitempty"`
	AllowGit        bool         `json:"allow_git,omitempty"`
	GitHub          *githubBrief `json:"github,omitempty"`
}

type connectionsBrief struct {
	Databases  []dbConnBrief    `json:"databases,omitempty"`
	SSHHosts   []sshHostBrief   `json:"ssh_hosts,omitempty"`
	LogSources []logSourceBrief `json:"log_sources,omitempty"`
	Aliases    []aliasBrief     `json:"aliases,omitempty"`
	Policy     *policyBrief     `json:"policy,omitempty"`
	Note       string           `json:"note,omitempty"`
}

// connectionsBriefFor loads the project's connection profiles and returns a
// secret-free brief, or nil when none are configured.
func connectionsBriefFor(repoRoot string) *connectionsBrief {
	cfg, err := connections.Load(repoRoot)
	if err != nil || cfg.Empty() {
		return nil
	}
	out := &connectionsBrief{Note: "connection profiles (read-only via MCP). Secrets, SSH users, and identity files are never shown. Manage with `codehelper connections` CLI only — the agent cannot add/remove profiles or allowlists unless agent_trust=allowlist_edits (future propose/approve flow). Ops tools: remote_list, remote_exec, log_read, db_query, db_schema, run_alias, env_context, ci_status."}
	for _, d := range cfg.Databases {
		hasSecret := strings.HasPrefix(strings.TrimSpace(d.PasswordRef), "env:") ||
			(d.UsesSecretStore() && secrets.Has(repoRoot, d.Name))
		out.Databases = append(out.Databases, dbConnBrief{
			Name: d.Name, Driver: d.Driver, Host: d.Host, Database: d.Database,
			SSHTunnel: d.SSHTunnel, ReadOnly: d.ReadOnly,
			Enabled: d.Enabled(), HasSecret: hasSecret,
		})
	}
	for _, h := range cfg.SSHHosts {
		var recipes []string
		for _, r := range h.Recipes {
			recipes = append(recipes, r.Name)
		}
		out.SSHHosts = append(out.SSHHosts, sshHostBrief{
			Name: h.Name, Hostname: h.Hostname, JumpHost: h.JumpHost,
			Enabled: h.Enabled(), AllowedCommands: h.AllowedCommands, Recipes: recipes,
		})
	}
	for _, l := range cfg.LogSources {
		out.LogSources = append(out.LogSources, logSourceBrief{
			Name: l.Name, Kind: l.Kind, Path: l.Path, SSHHost: l.SSHHost,
			Enabled: !l.Disabled,
		})
	}
	for _, a := range cfg.Aliases {
		ab := aliasBrief{Name: a.Name, RemoteHost: a.RemoteHost, RemoteRecipe: a.RemoteRecipe, RequiresApproval: a.RequiresApproval}
		ab.Local = a.RemoteHost == "" && len(a.Argv) > 0
		out.Aliases = append(out.Aliases, ab)
	}
	if cfg.Policy.IsConfigured() {
		p := connections.NormalizePolicy(cfg.Policy)
		pb := &policyBrief{
			AgentTrust: p.AgentTrust, VerifyAllowlist: p.VerifyAllowlist, AllowGit: p.AllowGit,
		}
		if p.GitHub != nil {
			pb.GitHub = &githubBrief{
				Repo: p.GitHub.Repo, HasToken: strings.HasPrefix(strings.TrimSpace(p.GitHub.TokenRef), "env:"),
				Disabled: p.GitHub.Disabled,
			}
		}
		out.Policy = pb
	}
	return out
}

// projectContextMCPResponse is structuredContent for project_context.
type projectContextMCPResponse struct {
	CodehelperVersion       string                 `json:"codehelper_version,omitempty"`
	Repo                    string                 `json:"repo"`
	RepoRoot                string                 `json:"repo_root"`
	Initialized             bool                   `json:"initialized"`
	SelectionReason         string                 `json:"selection_reason,omitempty"`
	ProjectType             string                 `json:"project_type"`
	Framework               string                 `json:"framework,omitempty"`
	ProjectVersion          string                 `json:"project_version,omitempty"`
	Versions                map[string]string      `json:"versions,omitempty"`
	Dependencies            []profile.Dependency   `json:"dependencies,omitempty"`
	SubProjects             []profile.SubProject   `json:"sub_projects,omitempty"`
	Gotchas                 []string               `json:"gotchas,omitempty"`
	LearnedHints            []string               `json:"learned_hints,omitempty"`
	Freshness               freshness.Report       `json:"freshness"`
	IndexStatus             string                 `json:"index_status"`
	IndexStats              *indexStatsBrief       `json:"index_stats,omitempty"`
	KeyEntrypoints          []string               `json:"key_entrypoints,omitempty"`
	Architecture            []string               `json:"architecture,omitempty"`
	Hubs                    []string               `json:"hubs,omitempty"`
	PackageHubs             []string               `json:"package_hubs,omitempty"`
	TopLevelDirectories     []string               `json:"top_level_directories,omitempty"`
	TopLevelFiles           []string               `json:"top_level_files,omitempty"`
	LikelyEntrypointFiles   []string               `json:"likely_entrypoint_files,omitempty"`
	PrimaryLanguage         string                 `json:"primary_language,omitempty"`
	Languages               []string               `json:"languages,omitempty"`
	LanguageStats           []profile.LanguageStat `json:"language_stats,omitempty"`
	Frameworks              []string               `json:"frameworks,omitempty"`
	KeyDependencies         []string               `json:"key_dependencies,omitempty"`
	Summary                 string                 `json:"summary,omitempty"`
	OS                      string                 `json:"os,omitempty"`
	Git                     *gitFacts              `json:"git,omitempty"`
	Surfaces                []string               `json:"surfaces,omitempty"`
	Scripts                 []string               `json:"scripts,omitempty"`
	SuggestedVerifyCommands []string               `json:"suggested_verify_commands,omitempty"`
	MCPToolCount            int                    `json:"mcp_tool_count,omitempty"`
	MCPMainTools            []string               `json:"mcp_main_tools,omitempty"`
	MCPParamKeys            string                 `json:"mcp_param_keys,omitempty"`
	ToolContractPath        string                 `json:"tool_contract_path,omitempty"`
	ToolsReferencePath      string                 `json:"tools_reference_path,omitempty"`
	MCPToolsByGroup         map[string][]string    `json:"mcp_tools_by_group,omitempty"`
	CLIONlyTools            []string               `json:"cli_only_tools,omitempty"`
	MCPToolsMode            string                 `json:"mcp_tools_mode,omitempty"`
	Connections             *connectionsBrief      `json:"connections,omitempty"`
	AgentInstructionFiles   []string               `json:"agent_instruction_files,omitempty"`
	MCPToolsEnabled         *bool                  `json:"mcp_tools_enabled,omitempty"`
	RecommendedNextTools    []string               `json:"recommended_next_tools,omitempty"`
	NextStep                string                 `json:"next_step,omitempty"`
	Warnings                []string               `json:"warnings,omitempty"`
	Confidence              float64                `json:"confidence,omitempty"`
}

// projectContextDetailed is true when verbosity=detailed; default is short.
func projectContextDetailed(args map[string]any) bool {
	return strings.EqualFold(argString(args, "verbosity"), "detailed")
}

// compactProjectContext keeps bootstrap essentials: repo identity, index health,
// stack summary, gotchas/hints, verify commands, and next-step guidance. The
// detailed response adds layout listings, dependency tables, git/scripts/surfaces.
func compactProjectContext(out projectContextMCPResponse) projectContextMCPResponse {
	return projectContextMCPResponse{
		CodehelperVersion:       out.CodehelperVersion,
		Repo:                    out.Repo,
		RepoRoot:                out.RepoRoot,
		Initialized:             out.Initialized,
		ProjectType:             out.ProjectType,
		Framework:               out.Framework,
		ProjectVersion:          out.ProjectVersion,
		Versions:                out.Versions,
		Gotchas:                 out.Gotchas,
		LearnedHints:            out.LearnedHints,
		Freshness:               out.Freshness,
		IndexStatus:             out.IndexStatus,
		IndexStats:              out.IndexStats,
		KeyEntrypoints:          out.KeyEntrypoints,
		Architecture:            out.Architecture,
		PrimaryLanguage:         out.PrimaryLanguage,
		Summary:                 out.Summary,
		Connections:             out.Connections,
		SuggestedVerifyCommands: out.SuggestedVerifyCommands,
		MCPToolCount:            out.MCPToolCount,
		MCPMainTools:            out.MCPMainTools,
		MCPParamKeys:            out.MCPParamKeys,
		ToolContractPath:        out.ToolContractPath,
		ToolsReferencePath:      out.ToolsReferencePath,
		AgentInstructionFiles:   out.AgentInstructionFiles,
		MCPToolsEnabled:         out.MCPToolsEnabled,
		RecommendedNextTools:    out.RecommendedNextTools,
		NextStep:                out.NextStep,
		Warnings:                out.Warnings,
		Confidence:              out.Confidence,
	}
}

// applyMCPCatalogFields copies bootstrap MCP metadata onto a project_context response.
func applyMCPCatalogFields(out *projectContextMCPResponse, includeToolGroups bool) {
	cat := MCPToolCatalogBrief()
	out.MCPToolCount = cat.Count
	out.MCPMainTools = cat.Main
	out.MCPParamKeys = cat.ParamKeys
	out.ToolContractPath = cat.ContractPath
	out.ToolsReferencePath = cat.ReferencePath
	if includeToolGroups {
		full := MCPToolCatalogFull()
		out.MCPToolsByGroup = full.ByGroup
		out.CLIONlyTools = full.CLIONly
	}
}

// nextToolsForQuery picks the CHEAPEST correct next move from the result state, so
// the agent doesn't waste a turn (or tokens) guessing. It replaces a static list —
// no extra field, no bloat — and adapts: nothing found → go structural; one clear
// winner → go deep; several near-ties → disambiguate / look for reuse.
func nextToolsForQuery(hits []retrieval.RankedSymbol) []string {
	switch {
	case len(hits) == 0:
		// Lexical+semantic found nothing — escalate to a structural match or open a
		// known file rather than re-querying with synonyms (which already ran).
		return []string{"ast_query", "read_workspace_file"}
	case len(hits) == 1 || ambiguityGuard(hits) == "":
		return []string{"context", "similar"}
	default:
		return []string{"context", "scout", "similar"}
	}
}

// dedupePathsCap returns the distinct non-empty paths in first-seen order, up to
// max. Evidence paths are drawn from ranked hits that frequently share a file, so
// deduping turns a redundant repeat of each hit's loc into a useful distinct-file
// list — and drops the duplicate tokens that shipped on every response.
func dedupePathsCap(paths []string, max int) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= max {
			break
		}
	}
	return out
}

func enrichQueryToolResponse(out *queryToolResponse, hits []retrieval.RankedSymbol) {
	out.RecommendedNextTools = nextToolsForQuery(hits)
	if len(out.FileSnippets) > 0 && len(hits) < 3 {
		out.RecommendedNextTools = append(out.RecommendedNextTools, "read_workspace_file")
	}
	if out.Freshness.Stale || out.Freshness.ActionRequired != nil {
		out.RecommendedNextTools = append([]string{"project_context"}, out.RecommendedNextTools...)
	}
	paths := make([]string, 0, len(hits))
	for _, h := range hits {
		paths = append(paths, h.Symbol.Path)
	}
	out.EvidencePaths = dedupePathsCap(paths, 8)
	if out.Warning != "" {
		out.Warnings = append(out.Warnings, out.Warning)
	}
	if out.Freshness.Stale && out.Freshness.StaleReason != "" {
		out.Warnings = append(out.Warnings, "stale index: "+out.Freshness.StaleReason)
	}
	if amb := ambiguityGuard(hits); amb != "" {
		out.Warnings = append(out.Warnings, amb)
	}
	out.Confidence = 0.72
	if len(hits) > 0 {
		out.Confidence = 0.86
	}
}

func enrichContextPackResponse(out *contextPackMCPResponse, hits []retrieval.RankedSymbol) {
	out.RecommendedNextTools = []string{"context", "read_workspace_file"}
	if out.Freshness.Stale || out.Freshness.ActionRequired != nil {
		out.RecommendedNextTools = append([]string{"project_context"}, out.RecommendedNextTools...)
	}
	paths := make([]string, 0, len(hits))
	for _, h := range hits {
		paths = append(paths, h.Symbol.Path)
	}
	out.EvidencePaths = dedupePathsCap(paths, 8)
	if out.Warning != "" {
		out.Warnings = append(out.Warnings, out.Warning)
	}
	if out.Freshness.Stale && out.Freshness.StaleReason != "" {
		out.Warnings = append(out.Warnings, "stale index: "+out.Freshness.StaleReason)
	}
	out.Confidence = 0.74
	if out.ContextPack != nil && len(out.ContextPack.ContextPack) > 0 {
		out.Confidence = 0.88
	}
}

func enrichImpactResponse(out *impactMCPResponse, res *types.ImpactResult) {
	out.RecommendedNextTools = []string{"context", "detect_changes", "read_workspace_file"}
	if out.Freshness.Stale || out.Freshness.ActionRequired != nil {
		out.RecommendedNextTools = append([]string{"project_context"}, out.RecommendedNextTools...)
	}
	if res != nil {
		paths := make([]string, 0, len(res.MustUpdateCandidates))
		for _, n := range res.MustUpdateCandidates {
			paths = append(paths, n.Path)
		}
		out.EvidencePaths = dedupePathsCap(paths, 12)
	}
	if out.Warning != "" {
		out.Warnings = append(out.Warnings, out.Warning)
	}
	if out.Freshness.Stale && out.Freshness.StaleReason != "" {
		out.Warnings = append(out.Warnings, "stale index: "+out.Freshness.StaleReason)
	}
	out.Confidence = 0.8
	if res != nil && len(res.Nodes) > 1 {
		out.Confidence = 0.9
	}
}
