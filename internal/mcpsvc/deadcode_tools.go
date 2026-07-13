package mcpsvc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- dead_code -------------------------------------------------------------

type deadCodeCandidate struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Loc       string `json:"loc"`
	Recv      string `json:"recv,omitempty"`
	Signature string `json:"signature,omitempty"`
	Exported  bool   `json:"exported"`
	Caveat    string `json:"caveat,omitempty"`

	confident bool // unexported, non-handler function: ranked first; not serialized
}

type deadCodeResponse struct {
	Candidates []deadCodeCandidate `json:"candidates"`
	Scanned    int                 `json:"scanned"`
	Truncated  int                 `json:"truncated,omitempty"`
	Freshness  string              `json:"freshness,omitempty"`
	Safety     string              `json:"safety"`
	Note       string              `json:"note,omitempty"`
}

const maxDeadCode = 50

// deadCodeHandler answers "what looks unused?" by listing symbols with no inbound
// resolved call/read edge, then dropping the things that are invoked by a runtime
// rather than by code the indexer can see (entrypoints, test functions, HTTP
// handlers). The result is deliberately framed as candidates to verify: the call
// graph is an under-approximation, so a clean list here is an OVER-approximation
// of dead code. Deleting blindly off this list would remove live code reached via
// reflection, dynamic dispatch, build tags, or cross-repo callers.
func deadCodeHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		kinds := parseKinds(argString(args, "kinds"))
		includeExported := argBool(args, "include_exported", false)
		includeTests := argBool(args, "include_tests", false)
		topK := int(mcp.ParseInt64(req, "top_k", 0))
		if topK <= 0 {
			topK = maxDeadCode
		}

		syms, err := st.UnreferencedSymbols(ctx, repo.Name, kinds)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		out := deadCodeResponse{
			Safety: "OVER-approximation of dead code: these symbols have no inbound call/read edge the indexer resolved. The call graph misses dynamic dispatch, reflection, build tags, route/handler registration, generated code, and cross-repo callers. VERIFY each before deleting — run `impact` (upstream) on it and a textual search for its name across the repo.",
		}
		// Build the full candidate list first, then rank confident rows (unexported,
		// non-handler functions) ahead of dynamically-invoked-looking ones, so the
		// most-likely-truly-dead symbols survive the top_k cap instead of being
		// pushed out by a flood of router handlers that only look unreferenced.
		var all []deadCodeCandidate
		for _, sym := range syms {
			if review.IsTestPath(sym.Path) && !includeTests {
				continue
			}
			if looksRuntimeInvoked(sym.Name) {
				continue
			}
			out.Scanned++
			exported := likelyPublicSymbol(sym)
			if exported && !includeExported {
				continue
			}
			suspect := dynamicDispatchSuspect(sym)
			all = append(all, deadCodeCandidate{
				Name:      sym.Name,
				Kind:      string(sym.Kind),
				Loc:       fmt.Sprintf("%s:%d", sym.Path, sym.LineStart),
				Recv:      sym.ParentID,
				Signature: sym.Signature,
				Exported:  exported,
				Caveat:    joinCaveats(suspect, exportedCaveat(exported)),
				confident: suspect == "" && !exported,
			})
		}
		sort.SliceStable(all, func(i, j int) bool { return all[i].confident && !all[j].confident })
		if len(all) > topK {
			out.Truncated = len(all) - topK
			all = all[:topK]
		}
		out.Candidates = all

		fresh := freshness.Inspect(repo.RootPath)
		if fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — re-run analyze for trustworthy dead-code detection"
		}
		switch {
		case len(out.Candidates) == 0 && out.Scanned == 0:
			out.Note = "nothing unreferenced for the requested kinds. If you expected results, the index may lack call-graph edges (run codehelper analyze --force)."
		case len(out.Candidates) == 0:
			out.Note = "every unreferenced symbol was excluded as exported or runtime-invoked. Pass include_exported=true to also surface exported-but-unused API (higher false-positive rate)."
		default:
			out.Note = "Start with the highest-confidence rows: unexported functions in non-test files. Confirm with impact(upstream) + a name search before removing."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// parseKinds turns a comma list into validated symbol kinds, defaulting to
// functions and methods (the kinds where "no caller" most cleanly means unused).
func parseKinds(raw string) []string {
	allowed := map[string]bool{
		string(types.SymbolKindFunction):  true,
		string(types.SymbolKindMethod):    true,
		string(types.SymbolKindClass):     true,
		string(types.SymbolKindVariable):  true,
		string(types.SymbolKindTypeAlias): true,
		string(types.SymbolKindEnum):      true,
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		if allowed[p] {
			out = append(out, p)
		}
	}
	return out
}

// looksRuntimeInvoked excludes symbols a runtime calls instead of project code:
// program/package entrypoints, the test runner's functions, and the std HTTP
// handler method. These have no inbound graph edge by nature, not because they
// are dead.
func looksRuntimeInvoked(name string) bool {
	switch name {
	case "main", "init", "ServeHTTP", "setUp", "tearDown", "setup", "teardown", "SetUp", "TearDown":
		return true
	}
	for _, p := range []string{"Test", "Benchmark", "Example", "Fuzz"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// dynamicDispatchSuspect flags symbols that are commonly invoked by a framework
// or runtime rather than by code the indexer can see — the dominant source of
// false positives. Returns a human caveat, or "" when the symbol looks like a
// plain, directly-called function.
func dynamicDispatchSuspect(sym types.Symbol) string {
	n := sym.Name
	switch {
	case strings.HasPrefix(n, "handle") || strings.HasPrefix(n, "Handle") || strings.HasSuffix(n, "Handler"):
		return "name looks like an HTTP/route handler — may be registered with a router, not called directly"
	case (strings.HasPrefix(n, "On") || strings.HasPrefix(n, "on")) && len(n) > 2 && unicode.IsUpper([]rune(n)[2]):
		return "name looks like an event callback — may be invoked dynamically"
	case sym.Kind == types.SymbolKindMethod:
		return "method: may satisfy an interface — dynamic dispatch is not fully resolved"
	}
	return ""
}

func exportedCaveat(exported bool) string {
	if exported {
		return "exported: may be called from outside this repo"
	}
	return ""
}

func joinCaveats(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "; ")
}
