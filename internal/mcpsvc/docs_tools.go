package mcpsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/docs"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/research"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// docsCacheTTL bounds how long fetched docs are trusted before refetch.
const docsCacheTTL = 24 * time.Hour

// docsHandler serves the `docs` MCP tool: version-correct official docs for a
// library, local-first, preferring llms.txt/llms-full.txt over HTML.
func docsHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		library := strings.TrimSpace(argString(args, "library"))
		if library == "" {
			return mcp.NewToolResultError("library is required"), nil
		}
		topic := strings.TrimSpace(argString(args, "topic"))
		version := strings.TrimSpace(argString(args, "version"))
		maxTokens := int(mcp.ParseInt64(req, "max_tokens", 5000))
		approveNetwork := argBool(args, "approve_network", false)
		noCache := argBool(args, "no_cache", false)

		// Resolve the workspace repo for manifest version detection + cache
		// scoping. docs is useful even without a registered repo, so fall back
		// to no-root (library-only) resolution on error.
		repoRoot := ""
		var cacheDir string
		if e, err := resolveRepo(ctx, reg, argString(args, "repo")); err == nil {
			repoRoot = e.RootPath
			cacheDir = filepath.Join(paths.RepoIndexDir(e.RootPath), "docs-cache")
		}

		if version == "" && repoRoot != "" {
			if v, _ := docs.ResolveVersion(repoRoot, library); v != "" {
				version = v
			}
		}

		// Network is gated: allowed if the project enabled research, the caller
		// approved this call, or the operator set the env override.
		network := approveNetwork ||
			(repoRoot != "" && research.NetworkEnabled(repoRoot)) ||
			os.Getenv("CODEHELPER_DOCS_NETWORK") == "1"

		// Allow any public HTTPS host: docs are resolved dynamically (user
		// overrides, registry metadata, direct URLs, llms.txt discovery), so a
		// host allowlist computed up-front would block exactly those paths — and
		// did, silently, for user-added sources. SSRF safety comes from netguard
		// inside the fetcher (loopback, RFC1918, and cloud-metadata are denied at
		// dial time) plus the HTTPS-only and body-size caps.
		eng := &docs.Engine{
			Fetcher: docs.NewHTTPFetcher(12*time.Second, nil),
		}
		if cacheDir != "" {
			eng.Cache = docs.NewCache(cacheDir, docsCacheTTL)
		}
		// Validate returned doc links over the network so the model never gets a
		// 404'd/hallucinated URL from a stale llms.txt.
		if network {
			eng.Validator = docs.NewHTTPValidator(6*time.Second, docsCacheTTL)
		}

		res, err := eng.Lookup(ctx, docs.LookupOptions{
			RepoRoot:      repoRoot,
			Library:       library,
			Version:       version,
			Topic:         topic,
			MaxTokens:     maxTokens,
			Network:       network,
			FollowLinks:   true,
			ValidateLinks: true,
			NoCache:       noCache,
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mustToolResultFormatted(res, resolveFormat(args))
	}
}

// docsAddHandler registers a documentation source the curated index is missing,
// so a later `docs` call resolves to it. Lets an agent fix a doc gap in-session
// (e.g. an internal framework or an API reference) instead of failing the lookup.
func docsAddHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		name := strings.TrimSpace(argString(args, "name"))
		docBase := strings.TrimSpace(argString(args, "doc_base"))
		if name == "" || docBase == "" {
			return mcp.NewToolResultError("name and doc_base are required"), nil
		}
		match := append([]string{name}, argStringSlice(args, "aliases")...)
		o := docs.Override{
			Match:     match,
			DocBase:   docBase,
			LLMSTxt:   strings.TrimSpace(argString(args, "llms_txt")),
			LLMSFull:  strings.TrimSpace(argString(args, "llms_full")),
			Trust:     int(mcp.ParseInt64(req, "trust", 0)),
			Ecosystem: strings.TrimSpace(argString(args, "ecosystem")),
		}
		project := strings.EqualFold(argString(args, "scope"), "project")
		var path string
		var err error
		if project {
			repoRoot := ""
			if e, rerr := resolveRepo(ctx, reg, argString(args, "repo")); rerr == nil {
				repoRoot = e.RootPath
			}
			path, err = docs.ProjectOverridePath(repoRoot)
		} else {
			path, err = docs.GlobalOverridePath()
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := docs.AddOverride(path, o); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		scope := "global"
		if project {
			scope = "project"
		}
		out := map[string]any{
			"added":    name,
			"doc_base": strings.TrimRight(docBase, "/"),
			"scope":    scope,
			"catalog":  path,
			"note":     "registered. Call docs with library=\"" + name + "\" to fetch it (network gate still applies).",
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}
