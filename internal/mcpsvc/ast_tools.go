package mcpsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/parser"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- ast_query -------------------------------------------------------------

type astQueryResponse struct {
	Language     string            `json:"language"`
	Pattern      string            `json:"pattern"`
	Matches      []parser.ASTMatch `json:"matches"`
	FilesScanned int               `json:"files_scanned"`
	Truncated    bool              `json:"truncated,omitempty"`
	Note         string            `json:"note"`
}

const (
	maxASTResults     = 200 // hard cap on returned matches (token budget)
	defaultASTResults = 50
)

// astQueryHandler runs a tree-sitter S-expression query over the repo's source
// files of one language and returns the captured nodes with file:line. Unlike
// the graph tools it reads files live from disk, so results are never stale —
// this is the precise, structural complement to lexical `query`: "find every
// node shaped like X" rather than "find symbols whose text matches X".
//
// The pattern is tree-sitter's native query syntax, e.g.
//
//	(function_declaration name: (identifier) @name)
//	(call_expression function: (selector_expression field: (field_identifier) @m) (#eq? @m "Lock"))
//
// Capture names (@name) are returned per match so the agent can tell which part
// of the shape each result corresponds to.
func astQueryHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		language := argString(args, "language")
		pattern := argString(args, "pattern")
		if language == "" || pattern == "" {
			return mcp.NewToolResultError("both 'language' and 'pattern' are required; pattern is a tree-sitter S-expression query, e.g. (function_declaration name: (identifier) @name)"), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		exts := parser.ExtensionsForASTLanguage(language)
		if exts == nil {
			return mcp.NewToolResultError("unsupported language: " + language), nil
		}
		extSet := map[string]struct{}{}
		for _, e := range exts {
			extSet[e] = struct{}{}
		}

		maxResults := int(mcp.ParseInt64(req, "max_results", 0))
		if maxResults <= 0 {
			maxResults = defaultASTResults
		}
		if maxResults > maxASTResults {
			maxResults = maxASTResults
		}
		pathGlob := strings.ToLower(strings.TrimSpace(argString(args, "path_glob")))

		// Respect .gitignore so we don't scan vendored/generated trees on a big
		// repo (mirrors what `analyze` indexes). Best-effort: a missing git root
		// just means no gitignore filtering.
		var giSkip func(string) bool
		if gitRoot, gerr := gitutil.FindGitRoot(repo.RootPath); gerr == nil {
			if gi, ierr := indexer.LoadLayeredGitIgnoreMatcher(gitRoot); ierr == nil && gi != nil {
				giSkip = indexer.GitIgnoreSkipFunc(gitRoot, repo.RootPath, gi)
			}
		}
		skip := func(rel string) bool {
			if _, ok := extSet[strings.ToLower(filepath.Ext(rel))]; !ok {
				return true
			}
			if pathGlob != "" && !strings.Contains(strings.ToLower(rel), pathGlob) {
				return true
			}
			if giSkip != nil && giSkip(rel) {
				return true
			}
			return false
		}
		// Prune gitignored directories from the walk (exclusion only) — never the
		// path_glob, which is an inclusion filter and would prune the wanted subtree.
		var dirSkip func(string) bool
		if giSkip != nil {
			dirSkip = func(rel string) bool { return giSkip(rel) || giSkip(rel+"/") }
		}
		files, err := indexer.WalkSourceFiles(repo.RootPath, dirSkip, skip)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		out := astQueryResponse{Language: language, Pattern: pattern}
		// Scan in parallel across cores (the ast-grep technique) so 100k+ file
		// repos stay fast; early-exit at maxResults+1 keeps matching queries cheap
		// regardless of repo size, and ctx cancellation stops a runaway full scan.
		read := func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(repo.RootPath, rel)) }
		res, serr := parser.ScanFiles(ctx, language, pattern, files, read, maxResults+1, 0)
		if ctx.Err() != nil {
			return mcp.NewToolResultError("ast_query cancelled: " + ctx.Err().Error()), nil
		}
		if serr != nil {
			return mcp.NewToolResultError(serr.Error()), nil
		}
		out.FilesScanned = res.Scanned
		matches := res.Matches
		if len(matches) > maxResults {
			out.Truncated = true
			matches = matches[:maxResults]
		}
		out.Matches = matches

		switch {
		case len(matches) == 0:
			out.Note = "no nodes matched. Check the pattern against the grammar's node types (e.g. Go uses function_declaration / method_declaration / call_expression); narrow with path_glob if scanning a large tree."
		case out.Truncated:
			out.Note = "results truncated at max_results — refine the pattern or set path_glob to a subdirectory to see the rest."
		default:
			out.Note = "Structural matches read live from disk (never stale). Each row's 'capture' names which part of the pattern it bound; 'loc' is path:line."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}
