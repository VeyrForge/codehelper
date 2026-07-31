package mcpsvc

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/lsp"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// lspResponse merges a language-server result with matching graph symbols so
// agents get type-precise LSP data AND codehelper symbol IDs in one payload.
type lspResponse struct {
	Action               string           `json:"action"`
	Server               string           `json:"server,omitempty"`
	Language             string           `json:"language,omitempty"`
	Hover                string           `json:"hover,omitempty"`
	Locations            []lsp.Location   `json:"locations,omitempty"`
	GraphSymbols         []lspGraphSymbol `json:"graph_symbols,omitempty"`
	OK                   bool             `json:"ok"`
	Fallback             bool             `json:"fallback,omitempty"`
	Source               string           `json:"source,omitempty"` // lsp | graph-fallback
	Note                 string           `json:"note,omitempty"`
	WhatNext             string           `json:"what_next,omitempty"`
	RecommendedNextTools []string         `json:"recommended_next_tools,omitempty"`
}

type lspGraphSymbol struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Loc  string `json:"loc"`
}

func lspHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
		switch action {
		case "hover", "definition", "def", "references", "refs", "reference",
			"rename", "implementations", "implementation", "impls", "impl":
		default:
			return mcp.NewToolResultError("action is required: hover | definition | references | rename | implementations"), nil
		}
		switch action {
		case "def":
			action = "definition"
		case "refs", "reference":
			action = "references"
		case "implementation", "impls", "impl":
			action = "implementations"
		}
		path := strings.TrimSpace(argString(args, "path"))
		if path == "" {
			return mcp.NewToolResultError("path is required (repo-relative file)"), nil
		}
		line := int(mcp.ParseInt64(req, "line", 1))
		col := int(mcp.ParseInt64(req, "col", 1))
		if col <= 0 {
			col = int(mcp.ParseInt64(req, "character", 1))
		}
		newName := argFirst(args, "new_name", "to", "newName")

		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		res, err := lsp.Query(ctx, repo.RootPath, lsp.Position{
			Path: path, Line: line, Col: col, NewName: newName,
		}, lsp.Action(action))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out := lspResponse{
			Action:    string(res.Action),
			Server:    res.Server,
			Language:  res.Language,
			Hover:     res.Hover,
			Locations: res.Locations,
			OK:        res.OK,
			Fallback:  res.Fallback,
			Note:      res.Note,
		}
		if res.OK && !res.Fallback {
			out.Source = "lsp"
		} else {
			out.Source = "graph-fallback"
		}
		// Merge graph symbol IDs at the query site and any returned locations.
		if st, gerr := openGraph(repo.RootPath); gerr == nil {
			defer st.Close()
			out.GraphSymbols = mergeLSPGraph(ctx, st, repo.Name, path, line, res.Locations)
			if res.Fallback || !res.OK {
				if out.Note == "" {
					out.Note = "LSP unavailable or empty — use graph_symbols / context / query / trace / rename_symbol / find_implementations."
				} else if !strings.Contains(out.Note, "graph") && !strings.Contains(out.Note, "rename_symbol") {
					out.Note += " Prefer graph_symbols / context / query when Fallback is true."
				}
				out.WhatNext = "Call context name=<symbol>, rename_symbol, or find_implementations — do not retry lsp with the same empty path/line"
				out.RecommendedNextTools = []string{"context", "query", "impact", "trace", "rename_symbol", "find_implementations"}
			}
		}
		if out.Note == "" && out.OK {
			out.Note = "LSP result merged with graph_symbols when indexed. Default navigation remains query/context/trace; use lsp for compiler-grade types at a file:line."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func mergeLSPGraph(ctx context.Context, st *graph.Store, repoID, path string, line int, locs []lsp.Location) []lspGraphSymbol {
	seen := map[string]bool{}
	var out []lspGraphSymbol
	addSyms := func(rel string, atLine int) {
		rel = filepath.ToSlash(rel)
		syms, err := st.SymbolsForPath(ctx, repoID, rel)
		if err != nil {
			return
		}
		var best *types.Symbol
		for i := range syms {
			s := &syms[i]
			if s.LineStart <= atLine && (s.LineEnd == 0 || s.LineEnd >= atLine) {
				if best == nil || s.LineStart > best.LineStart {
					best = s
				}
			}
		}
		if best == nil && len(syms) > 0 {
			// Closest symbol at or before the line.
			for i := range syms {
				s := &syms[i]
				if s.LineStart <= atLine && (best == nil || s.LineStart > best.LineStart) {
					best = s
				}
			}
		}
		if best == nil {
			return
		}
		if seen[best.ID] {
			return
		}
		seen[best.ID] = true
		out = append(out, lspGraphSymbol{
			ID: best.ID, Name: best.Name, Kind: string(best.Kind),
			Loc: filepath.ToSlash(best.Path) + ":" + strconv.Itoa(best.LineStart),
		})
	}
	addSyms(path, line)
	for _, l := range locs {
		if len(out) >= 8 {
			break
		}
		addSyms(l.Path, l.Line)
	}
	return out
}

// RegisterLSPTools registers the optional LSP bridge (hover/definition/references/rename/implementations).
func RegisterLSPTools(s *server.MCPServer, reg *registry.Registry) {
	s.AddTool(mcp.NewTool("lsp",
		mcp.WithDescription("Optional LSP bridge when gopls / typescript-language-server / pyright are installed: hover | definition | references | rename | implementations. Merges graph_symbols IDs with LSP results. Graceful graph fallback when binaries missing or CODEHELPER_LSP=0 (source=graph-fallback). Does NOT replace query/context/trace/rename_symbol/find_implementations — use those by default; lsp when you need compiler-grade types at a file:line."),
		mcp.WithString("action", mcp.Required(), mcp.Description("hover | definition (alias def) | references (alias refs) | rename | implementations (alias impl)")),
		mcp.WithString("path", mcp.Required(), mcp.Description("Repo-relative file path")),
		mcp.WithNumber("line", mcp.Description("1-based line (default 1)"), mcp.DefaultNumber(1)),
		mcp.WithNumber("col", mcp.Description("1-based column (default 1)"), mcp.DefaultNumber(1)),
		mcp.WithNumber("character", mcp.Description("Alias for col")),
		mcp.WithString("new_name", mcp.Description("For action=rename: proposed new identifier (aliases: to, newName)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("lsp", lspHandler(reg)))
}
