package mcpsvc

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- trace -----------------------------------------------------------------

type traceStep struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Loc   string `json:"loc"`
	Depth int    `json:"depth,omitempty"`
}

type traceResponse struct {
	From      string      `json:"from"`
	To        string      `json:"to,omitempty"`
	Path      []traceStep `json:"path,omitempty"` // from → … → to (shortest call path)
	Hops      int         `json:"hops,omitempty"`
	Flow      []traceStep `json:"flow,omitempty"` // BFS call tree from `from` (no `to`)
	Truncated bool        `json:"truncated,omitempty"`
	Freshness string      `json:"freshness,omitempty"`
	Note      string      `json:"note"`
}

const (
	traceDefaultDepth = 12
	traceMaxFlow      = 60
)

// traceHandler answers the call-graph navigation question agents otherwise solve
// by hopping context→context (one tool call per hop, burning tokens): given a
// `from` symbol and a `to` symbol, it returns the SHORTEST call path between them
// in one deterministic BFS — "how does the HTTP handler reach the DB write?".
// With no `to`, it returns the call-flow tree from `from`. This is the exact
// multihop navigation that ranked retrieval can't give; it directly targets the
// "hidden/transitive dependency" failure mode (CodeCompass's Navigation Paradox).
func traceHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		from := argFirst(args, "from", "name", "symbol", "sym")
		if from == "" {
			return mcp.NewToolResultError("from is required — the entrypoint symbol name or sym: id to trace outward from (e.g. \"ServeHTTP\")"), nil
		}
		to := argFirst(args, "to", "target")
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		depth := int(mcp.ParseInt64(req, "depth", 0))
		if depth <= 0 {
			depth = traceDefaultDepth
		}

		fromCands, err := resolveTraceCandidates(ctx, st, repo.Name, from)
		if err != nil || len(fromCands) == 0 {
			return mcp.NewToolResultError(fmt.Sprintf("no indexed symbol named %q. Use `query` to find the entrypoint's exact name/sym: id, or re-index with `codehelper analyze`.", from)), nil
		}
		fromSym := &fromCands[0]

		out := traceResponse{From: fromSym.Name}
		if fresh := freshness.Inspect(repo.RootPath); fresh.Stale {
			out.Freshness = "index may be stale (" + fresh.StaleReason + ") — paths may be incomplete; re-run analyze"
		}

		if to != "" {
			toCands, terr := resolveTraceCandidates(ctx, st, repo.Name, to)
			if terr != nil || len(toCands) == 0 {
				return mcp.NewToolResultError(fmt.Sprintf("no indexed symbol named %q (the `to` target). Use `query` to find it.", to)), nil
			}
			toSym := &toCands[0]
			out.To = toSym.Name
			// A name can be ambiguous (gin.Default vs binding.Default). Try every
			// (from,to) candidate pair — ranked best-first — and return the first
			// real path, so we never report "no path" just because we picked the
			// wrong same-named symbol. Bounded to the top few of each side.
			const maxTry = 4
			var path []types.Symbol
			var pf, pt *types.Symbol
			for i := 0; i < len(fromCands) && i < maxTry && path == nil; i++ {
				for j := 0; j < len(toCands) && j < maxTry; j++ {
					if p := shortestCallPath(ctx, st, repo.Name, fromCands[i].ID, toCands[j].ID, depth); p != nil {
						path, pf, pt = p, &fromCands[i], &toCands[j]
						break
					}
				}
			}
			if path != nil {
				out.From, out.To = pf.Name, pt.Name
				out.Path = symsToSteps(path, true)
				out.Hops = len(path) - 1
				out.Note = fmt.Sprintf("shortest call path: %s reaches %s in %d hop(s). Each step is a resolved call edge — read these symbols to follow the flow.", pf.Name, pt.Name, out.Hops)
				return mustToolResultFormatted(out, resolveFormat(args))
			}
			// No forward path among any pair — try the reverse direction.
			for i := 0; i < len(fromCands) && i < maxTry; i++ {
				for j := 0; j < len(toCands) && j < maxTry; j++ {
					if rev := shortestCallPath(ctx, st, repo.Name, toCands[j].ID, fromCands[i].ID, depth); rev != nil {
						out.Note = fmt.Sprintf("no call path %s → %s, but %s reaches %s in %d hop(s) — the dependency runs the other way (see path).", fromCands[i].Name, toCands[j].Name, toCands[j].Name, fromCands[i].Name, len(rev)-1)
						out.Path = symsToSteps(rev, true)
						out.Hops = len(rev) - 1
						out.From, out.To = toCands[j].Name, fromCands[i].Name
						return mustToolResultFormatted(out, resolveFormat(args))
					}
				}
			}
			out.Note = fmt.Sprintf("no resolved call path between %s and %s within depth %d (in either direction). They may be connected only via dynamic dispatch/interfaces the static graph misses, or through a deeper chain — raise depth, or check `impact`.", fromSym.Name, toSym.Name, depth)
			return mustToolResultFormatted(out, resolveFormat(args))
		}

		// No target: trace the outward call flow (BFS tree) from `from`.
		flow, truncated := callFlow(ctx, st, repo.Name, fromSym.ID, depth, traceMaxFlow)
		out.Flow = flow
		out.Truncated = truncated
		if len(flow) == 0 {
			out.Note = fmt.Sprintf("%s makes no resolved outbound calls (a leaf, or the graph lacks its edges). For who CALLS it, use `context` or `impact` direction=upstream.", fromSym.Name)
		} else {
			out.Note = "outbound call flow (BFS, depth-tagged). Pass `to=<symbol>` to get the exact shortest path to a specific function instead. For the reverse (callers), use impact direction=upstream."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func resolveTraceSymbol(ctx context.Context, st *graph.Store, repoID, target string) (*types.Symbol, error) {
	cands, err := resolveTraceCandidates(ctx, st, repoID, target)
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	return &cands[0], nil
}

// resolveTraceCandidates returns the symbols a name could mean, RANKED so the most
// useful for tracing comes first. A name like "Default" is ambiguous (gin.Default,
// binding.Default, …); naively taking the first match traced from binding.Default
// (0 calls) and reported "no path", when gin.Default → New plainly exists. We rank
// exact-name, non-test definitions by outbound call count (the most-connected
// definition is the one worth tracing), then exact names, then the rest — and the
// caller tries them in order until a path is found.
func resolveTraceCandidates(ctx context.Context, st *graph.Store, repoID, target string) ([]types.Symbol, error) {
	if strings.HasPrefix(target, "sym:") {
		s, err := st.SymbolByID(ctx, repoID, target)
		if err != nil || s == nil {
			return nil, err
		}
		return []types.Symbol{*s}, nil
	}
	syms, err := st.SymbolsByName(ctx, repoID, target, 24)
	if err != nil || len(syms) == 0 {
		return nil, err
	}
	var exactNonTest, exact, rest []types.Symbol
	for i := range syms {
		switch {
		case strings.EqualFold(syms[i].Name, target) && !review.IsTestPath(syms[i].Path):
			exactNonTest = append(exactNonTest, syms[i])
		case strings.EqualFold(syms[i].Name, target):
			exact = append(exact, syms[i])
		default:
			rest = append(rest, syms[i])
		}
	}
	// Rank the exact, non-test definitions by how connected they are — the one with
	// the most outbound calls is the real implementation worth tracing.
	if len(exactNonTest) > 1 {
		outdeg := make(map[string]int, len(exactNonTest))
		for _, s := range exactNonTest {
			if edges, e := st.EdgesFrom(ctx, repoID, s.ID, "calls"); e == nil {
				outdeg[s.ID] = len(edges)
			}
		}
		sort.SliceStable(exactNonTest, func(i, j int) bool {
			return outdeg[exactNonTest[i].ID] > outdeg[exactNonTest[j].ID]
		})
	}
	// Return ONLY exact-name matches when any exist — the multi-candidate pathfinder
	// tries every one, and SymbolsByName's fuzzy LIKE would otherwise drag in
	// unrelated symbols (e.g. "A" also LIKE-matches "Unrelated"), causing false
	// self-paths. Fall back to fuzzy matches only when there's no exact name.
	if exactAll := append(exactNonTest, exact...); len(exactAll) > 0 {
		return exactAll, nil
	}
	return rest, nil
}

// shortestCallPath returns the shortest forward call path fromID → toID (inclusive
// of both endpoints) via BFS over resolved `calls` edges, or nil if unreachable
// within depth. BFS guarantees the path is shortest; the visited set makes cycles
// safe and keeps it O(V+E).
func shortestCallPath(ctx context.Context, st *graph.Store, repoID, fromID, toID string, depth int) []types.Symbol {
	if fromID == toID {
		if s, _ := st.SymbolByID(ctx, repoID, fromID); s != nil {
			return []types.Symbol{*s}
		}
		return nil
	}
	parent := map[string]string{fromID: ""}
	symOf := map[string]types.Symbol{}
	depthOf := map[string]int{fromID: 0}
	queue := []string{fromID}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if depthOf[cur] >= depth {
			continue
		}
		callees, err := st.CalleesOf(ctx, repoID, cur)
		if err != nil {
			return nil
		}
		for _, c := range callees {
			if _, seen := parent[c.ID]; seen {
				continue
			}
			parent[c.ID] = cur
			symOf[c.ID] = c
			depthOf[c.ID] = depthOf[cur] + 1
			if c.ID == toID {
				return reconstructPath(parent, symOf, st, ctx, repoID, fromID, toID)
			}
			queue = append(queue, c.ID)
		}
	}
	return nil
}

func reconstructPath(parent map[string]string, symOf map[string]types.Symbol, st *graph.Store, ctx context.Context, repoID, fromID, toID string) []types.Symbol {
	var rev []types.Symbol
	for id := toID; id != ""; id = parent[id] {
		if s, ok := symOf[id]; ok {
			rev = append(rev, s)
		} else if s, _ := st.SymbolByID(ctx, repoID, id); s != nil {
			rev = append(rev, *s)
		}
		if id == fromID {
			break
		}
	}
	// rev is to→from; reverse to from→to.
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// callFlow returns the BFS call tree from fromID (excluding fromID itself),
// depth-tagged, capped at maxSteps.
func callFlow(ctx context.Context, st *graph.Store, repoID, fromID string, depth, maxSteps int) ([]traceStep, bool) {
	visited := map[string]bool{fromID: true}
	type qi struct {
		id string
		d  int
	}
	queue := []qi{{fromID, 0}}
	var out []traceStep
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.d >= depth {
			continue
		}
		callees, err := st.CalleesOf(ctx, repoID, cur.id)
		if err != nil {
			break
		}
		for _, c := range callees {
			if visited[c.ID] {
				continue
			}
			visited[c.ID] = true
			if len(out) >= maxSteps {
				return out, true
			}
			out = append(out, traceStep{Name: c.Name, Kind: string(c.Kind), Loc: fmt.Sprintf("%s:%d", c.Path, c.LineStart), Depth: cur.d + 1})
			queue = append(queue, qi{c.ID, cur.d + 1})
		}
	}
	return out, false
}

func symsToSteps(syms []types.Symbol, withDepth bool) []traceStep {
	out := make([]traceStep, 0, len(syms))
	for i, s := range syms {
		step := traceStep{Name: s.Name, Kind: string(s.Kind), Loc: fmt.Sprintf("%s:%d", s.Path, s.LineStart)}
		if withDepth {
			step.Depth = i
		}
		out = append(out, step)
	}
	return out
}
