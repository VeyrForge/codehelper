package mcpsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/detect"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/mcpimpact"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterRetrievalFacadeTools wires ACI facades for hybrid search + context bundles.
func RegisterRetrievalFacadeTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg
	s.AddTool(mcp.NewTool("search_hybrid",
		mcp.WithDescription("Hybrid locate: BM25/FTS → 1–2 hop call/import expand → RRF fuse (optional vector channel when CODEHELPER_EMBED_URL is set). Prefer over raw query when you need structurally related neighbors, not only lexical matches. Optional path= returns a hub-biased public API map for that package (library spine)."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Symbol name, concept, or natural-language locate task")),
		mcp.WithString("path", mcp.Description("Optional package/directory prefix for a hub-biased public_api_map (e.g. lib/ or internal/retrieval)")),
		mcp.WithNumber("top_k", mcp.Description("Max ranked hits (default 10)"), mcp.DefaultNumber(0)),
		mcp.WithString("intent", mcp.Description("Optional task intent: explore|debug|test|refactor")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("search_hybrid", searchHybridHandler(regRef)))

	s.AddTool(mcp.NewTool("context_bundle",
		mcp.WithDescription("ACI one-shot for ONE symbol: bounded SOURCE + callers + callees + imports (+ nearby tests). Prefer after search_hybrid/query instead of chaining context + read_workspace_file. Pass name or sym: id; path= disambiguates collisions."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Symbol name or sym: id (aliases: symbol, sym, target)")),
		mcp.WithString("path", mcp.Description("Definition file to disambiguate")),
		mcp.WithNumber("line", mcp.Description("Definition line to disambiguate (optional)")),
		mcp.WithNumber("max_callers", mcp.Description("Caller cap (default 24)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("max_callees", mcp.Description("Callee cap (default 24)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("max_imports", mcp.Description("Import cap (default 24)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("max_source_lines", mcp.Description("Definition source line cap (default 40; small bodies auto-expand)"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("include_tests", mcp.Description("Include nearby test callers (default true)"), mcp.DefaultBool(true)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("context_bundle", contextBundleHandler(regRef)))
}

type searchHybridResponse struct {
	Hits                 any                        `json:"hits"`
	HitsTruncated        int                        `json:"hits_truncated,omitempty"`
	PublicAPIMap         []retrieval.PublicAPIEntry `json:"public_api_map,omitempty"`
	FusionNote           string                     `json:"fusion_note,omitempty"`
	SemanticRerank       string                     `json:"semantic_rerank,omitempty"`
	Freshness            freshness.Report           `json:"freshness"`
	Warning              string                     `json:"warning,omitempty"`
	RecommendedNextTools []string                   `json:"recommended_next_tools,omitempty"`
}

func searchHybridHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		q := argQuery(args)
		if q == "" {
			return mcp.NewToolResultError("query must not be empty"), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		topK := int(mcp.ParseInt64(req, "top_k", 0))
		if topK <= 0 {
			topK = 10
		}
		intent := argString(args, "intent")
		diffSet, _ := detect.ChangedSymbolSet(ctx, repo.RootPath, repo.Name, "HEAD~1", st)
		retrieval.EnsureEmbedder()
		opts := retrieval.MCPQueryOptionsWithProfile(
			repo.RootPath, intent, strings.Fields(strings.ToLower(q)), diffSet,
		)
		opts.EnableGraphExpand = true
		hits, err := retrieval.QueryHybridWithOptions(ctx, st, repo.Name, q, topK*2, opts)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		hits, _ = demoteFixtureHits(hits)
		surfaced, truncated := capHits(hits, topK)
		fresh := freshness.Inspect(repo.RootPath)
		out := searchHybridResponse{
			Hits:                 hitsView(surfaced, false),
			HitsTruncated:        truncated,
			Freshness:            fresh,
			FusionNote:           "BM25/FTS → 1–2 hop graph expand → RRF; vectors RRF-fused when CODEHELPER_EMBED_URL is set.",
			SemanticRerank:       semanticRerankStatus(surfaced),
			RecommendedNextTools: []string{"context_bundle", "context", "impact", "trace"},
		}
		if pkg := strings.TrimSpace(argString(args, "path")); pkg != "" {
			if api, aerr := retrieval.BuildPublicAPIMap(ctx, st, repo.Name, retrieval.PublicAPIMapOptions{
				PathPrefix: pkg, Limit: 40,
			}); aerr == nil {
				out.PublicAPIMap = api
			}
		}
		if fresh.Stale {
			out.Warning = "index may be stale: " + fresh.StaleReason
		}
		if len(surfaced) == 0 {
			out.FusionNote += " No hits — try a shorter distinctive term, context_bundle on a known sym: id, or codehelper analyze."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func contextBundleHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		name := argFirst(args, "name", "symbol", "sym", "target")
		if name == "" {
			return mcp.NewToolResultError("name is required — pass name or sym: id from search_hybrid/query"), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		wantPath := strings.TrimSpace(argString(args, "path"))
		wantLine := int(argFloat(args, "line", 0))
		sym, cands, err := resolveSymbolByName(ctx, st, repo.Name, name, wantPath, wantLine)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if sym == nil {
			return mustToolResultFormatted(map[string]any{
				"ambiguous":  true,
				"name":       name,
				"candidates": cands,
				"note":       "multiple symbols share this name — pass path= or a sym: id",
			}, resolveFormat(args))
		}

		callerLim := int(mcp.ParseInt64(req, "max_callers", 0))
		calleeLim := int(mcp.ParseInt64(req, "max_callees", 0))
		importLim := int(mcp.ParseInt64(req, "max_imports", 0))
		srcLim := int(mcp.ParseInt64(req, "max_source_lines", 0))
		includeTests := true
		if v, ok := args["include_tests"].(bool); ok {
			includeTests = v
		}
		bun, err := retrieval.BuildContextBundle(ctx, st, repo.Name, sym.ID, retrieval.ContextBundleOptions{
			CallerLimit:    callerLim,
			CalleeLimit:    calleeLim,
			ImportLimit:    importLim,
			RepoRoot:       repo.RootPath,
			MaxSourceLines: srcLim,
			IncludeTests:   includeTests,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		fresh := freshness.Inspect(repo.RootPath)
		out := map[string]any{
			"bundle":                 bun,
			"freshness":              fresh,
			"recommended_next_tools": []string{"impact", "trace", "change_kit"},
		}
		if bun != nil && bun.Symbol != nil {
			if res, aerr := mcpimpact.Analyze(ctx, st, repo.Name, bun.Symbol.ID, 2, "upstream"); aerr == nil && res != nil && len(res.Nodes) > 1 {
				br := blastRadius{RiskTier: res.RiskTier, Dependents: len(res.Nodes) - 1}
				for _, n := range res.Nodes {
					if n.Depth == 0 || len(br.Top) >= 6 {
						continue
					}
					br.Top = append(br.Top, fmt.Sprintf("%s %s", n.Name, locOf(n.Path, n.SymbolID)))
				}
				out["blast_radius"] = br
			}
		}
		if bun != nil && len(bun.Callers) == 0 && len(bun.Callees) == 0 && len(bun.Imports) == 0 {
			out["note"] = "no graph edges resolved — leaf-like or unresolved edges; try impact/trace or codehelper analyze --force"
		}
		if fresh.Stale {
			out["warning"] = "index may be stale: " + fresh.StaleReason
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}
