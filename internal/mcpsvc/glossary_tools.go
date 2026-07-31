package mcpsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/memory"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/vocab"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterGlossaryTools wires the `glossary` tool: review the index-time
// vocabulary seed (frequent identifiers + sub-words, cross-referenced against the
// symbol graph so each candidate term shows WHAT IT CONNECTS TO), and promote the
// meaningful ones into the shared, committable project glossary
// (project_memory.json). This turns raw frequency into reviewed project knowledge
// that every collaborator's agent then reads.
func RegisterGlossaryTools(s *server.MCPServer, reg *registry.Registry) {
	s.AddTool(mcp.NewTool("glossary",
		mcp.WithDescription("Review the project vocabulary seed and promote terms into the shared glossary. action=review lists frequent candidate terms enriched with the symbols they connect to; action=promote writes a term+definition into the committable project_memory.json; action=list shows the approved glossary."),
		mcp.WithString("action", mcp.Required(), mcp.Description("review|promote|list")),
		mcp.WithNumber("limit", mcp.Description("review: how many candidate terms to return"), mcp.DefaultNumber(25)),
		mcp.WithString("term", mcp.Description("promote: the term to add to the glossary")),
		mcp.WithString("definition", mcp.Description("promote: the canonical meaning of the term")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		annotTaskMutate(),
	), timedTool("glossary", glossaryHandler(reg)))
}

// glossaryCandidate is one reviewable term plus its graph linkage.
type glossaryCandidate struct {
	Term          string         `json:"term"`
	Count         int            `json:"count"`
	InGlossary    bool           `json:"in_glossary"`
	SymbolMatches int            `json:"symbol_matches"`
	ConnectsTo    []connectedSym `json:"connects_to,omitempty"`
}

type connectedSym struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

func glossaryHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
		ms := memory.Open(repo.RootPath)

		switch action {
		case "review":
			v, lerr := vocab.Load(repo.RootPath)
			if lerr != nil {
				return mcp.NewToolResultError(lerr.Error()), nil
			}
			if len(v.Terms) == 0 {
				return mcp.NewToolResultText(`{"candidates":[],"note":"no vocabulary seed yet — reindex this project to generate .codehelper/vocab.json"}`), nil
			}
			inGloss := glossaryKeys(ms)
			limit := int(mcp.ParseInt64(req, "limit", 25))
			if limit <= 0 {
				limit = 25
			}
			st, gerr := openGraph(repo.RootPath)
			if gerr != nil {
				return mcp.NewToolResultError(gerr.Error()), nil
			}
			defer st.Close()

			cands := make([]glossaryCandidate, 0, limit)
			for _, t := range v.Terms {
				if len(cands) >= limit {
					break
				}
				c := glossaryCandidate{Term: t.Term, Count: t.Count, InGlossary: inGloss[t.Term]}
				// Cross-reference the term against the symbol graph: what code does it
				// actually name? This is the "what it connects to" signal that lets a
				// reviewer judge whether a frequent token is real project vocabulary.
				if syms, serr := st.SearchSymbolsFTS(ctx, repo.Name, []string{t.Term}, 50); serr == nil {
					c.SymbolMatches = len(syms)
					for i, sym := range syms {
						if i >= 5 {
							break
						}
						c.ConnectsTo = append(c.ConnectsTo, connectedSym{Name: sym.Name, Kind: string(sym.Kind), Path: sym.Path})
					}
				}
				cands = append(cands, c)
			}
			out := map[string]any{
				"candidates": cands,
				"reviewed":   len(cands),
				"languages":  v.Languages,
				"note":       "Promote the meaningful terms with action=promote, term=<term>, definition=<meaning>. Skip generic tokens with no distinctive connects_to.",
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return mcp.NewToolResultText(string(b)), nil

		case "promote":
			term := strings.TrimSpace(argString(args, "term"))
			def := strings.TrimSpace(argString(args, "definition"))
			if term == "" || def == "" {
				return mcp.NewToolResultError("both term and definition are required for promote"), nil
			}
			// Attach the symbols the term connects to, so the committed glossary entry
			// is self-contained: a reader (human or agent) sees the meaning AND the code
			// it maps to, which is what makes the glossary an aid against breaking things.
			var connects []connectedSym
			if st, gerr := openGraph(repo.RootPath); gerr == nil {
				defer st.Close()
				if syms, serr := st.SearchSymbolsFTS(ctx, repo.Name, []string{term}, 5); serr == nil {
					for _, sym := range syms {
						connects = append(connects, connectedSym{Name: sym.Name, Kind: string(sym.Kind), Path: sym.Path})
					}
				}
			}
			value := def
			if len(connects) > 0 {
				names := make([]string, 0, len(connects))
				for _, c := range connects {
					names = append(names, c.Name)
				}
				value = fmt.Sprintf("%s | connects_to: %s", def, strings.Join(names, ", "))
			}
			if err := ms.AddFact(term, value); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out := map[string]any{
				"ok":          true,
				"term":        term,
				"connects_to": connects,
				"note":        "written to project_memory.json (the committable shared glossary) — review the git diff before committing",
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return mcp.NewToolResultText(string(b)), nil

		case "list":
			facts, ferr := ms.Facts()
			if ferr != nil {
				return mcp.NewToolResultError(ferr.Error()), nil
			}
			type entry struct {
				Term       string `json:"term"`
				Definition string `json:"definition"`
			}
			entries := make([]entry, 0, len(facts))
			for _, f := range facts {
				entries = append(entries, entry{Term: f.Key, Definition: f.Value})
			}
			b, _ := json.MarshalIndent(map[string]any{"glossary": entries, "count": len(entries)}, "", "  ")
			return mcp.NewToolResultText(string(b)), nil

		default:
			return mcp.NewToolResultError("action must be review|promote|list"), nil
		}
	}
}

// glossaryKeys returns the set of terms already promoted into the glossary.
func glossaryKeys(ms *memory.Store) map[string]bool {
	keys := map[string]bool{}
	if facts, err := ms.Facts(); err == nil {
		for _, f := range facts {
			keys[f.Key] = true
		}
	}
	return keys
}
