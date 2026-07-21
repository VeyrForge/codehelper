package mcpsvc

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
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

// deadCodeCandidate is shaped for LLM consumption: path + symbol + reason +
// confidence, so agents can verify high-confidence rows first without parsing
// Loc strings or inferring why a row was flagged.
type deadCodeCandidate struct {
	Symbol     string  `json:"symbol"`
	Path       string  `json:"path"`
	Line       int     `json:"line"`
	Kind       string  `json:"kind"`
	Reason     string  `json:"reason"`
	Confidence string  `json:"confidence"` // high | medium | low
	Exported   bool    `json:"exported"`
	Loc        string  `json:"loc"` // path:line — kept for older clients
	Recv       string  `json:"recv,omitempty"`
	Signature  string  `json:"signature,omitempty"`
	Caveat     string  `json:"caveat,omitempty"` // alias of Reason for older clients

	rank int // lower = more confident; not serialized
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

// syntheticRouteName matches indexer-invented route/entry symbols (Laravel
// route_get_N, FastAPI fastapi_get_N, Express express_post_N, …) that are
// never called via the graph by design.
var syntheticRouteName = regexp.MustCompile(`(?i)^(route|fastapi|express|flask|fiber|gin|nest|axum|ktor)_(get|post|put|patch|delete|head|options|any)_\d+$`)

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
			Safety: "OVER-approximation of dead code: these symbols have no inbound call/read edge the indexer resolved. The call graph misses dynamic dispatch, reflection, build tags, route/handler registration, generated code, and cross-repo callers. VERIFY each before deleting — run `impact` (upstream) on it and a textual search for its name across the repo. Prefer confidence=high rows.",
		}
		var all []deadCodeCandidate
		for _, sym := range syms {
			if review.IsTestPath(sym.Path) && !includeTests {
				continue
			}
			if review.IsSecondaryNoisePath(sym.Path) {
				continue
			}
			if looksRuntimeInvoked(sym) || looksSyntheticOrNoise(sym) {
				continue
			}
			out.Scanned++
			exported := likelyPublicSymbol(sym)
			if exported && !includeExported {
				continue
			}
			cand := classifyDeadCandidate(sym, exported)
			all = append(all, cand)
		}
		sort.SliceStable(all, func(i, j int) bool {
			if all[i].rank != all[j].rank {
				return all[i].rank < all[j].rank
			}
			if all[i].Path != all[j].Path {
				return all[i].Path < all[j].Path
			}
			return all[i].Line < all[j].Line
		})
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
			out.Note = "every unreferenced symbol was excluded as exported, runtime-invoked, synthetic, or noise. Pass include_exported=true to also surface exported-but-unused API (higher false-positive rate)."
		default:
			out.Note = "Start with confidence=high (unexported, non-framework names in production paths). Confirm with impact(upstream) + a name search before removing."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func classifyDeadCandidate(sym types.Symbol, exported bool) deadCodeCandidate {
	suspect := dynamicDispatchSuspect(sym)
	reasonParts := []string{"no inbound calls/reads in the index"}
	if suspect != "" {
		reasonParts = append(reasonParts, suspect)
	}
	if exported {
		reasonParts = append(reasonParts, "exported/public: may be called from outside this repo")
	}
	reason := strings.Join(reasonParts, "; ")
	conf, rank := deadCodeConfidence(exported, suspect)
	loc := fmt.Sprintf("%s:%d", sym.Path, sym.LineStart)
	return deadCodeCandidate{
		Symbol:     sym.Name,
		Path:       sym.Path,
		Line:       sym.LineStart,
		Kind:       string(sym.Kind),
		Reason:     reason,
		Confidence: conf,
		Exported:   exported,
		Loc:        loc,
		Recv:       sym.ParentID,
		Signature:  sym.Signature,
		Caveat:     reason,
		rank:       rank,
	}
}

func deadCodeConfidence(exported bool, suspect string) (string, int) {
	switch {
	case !exported && suspect == "":
		return "high", 0
	case !exported && suspect != "":
		return "medium", 1
	case exported && suspect == "":
		return "medium", 2
	default:
		return "low", 3
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

// looksRuntimeInvoked excludes symbols a runtime/framework calls instead of
// project code: program entrypoints, test runners, HTTP handlers, DI hooks.
func looksRuntimeInvoked(sym types.Symbol) bool {
	name := sym.Name
	switch name {
	case "main", "init", "ServeHTTP", "setUp", "tearDown", "setup", "teardown",
		"SetUp", "TearDown", "setUpClass", "tearDownClass",
		"__construct", "__destruct", "__invoke", "__call", "__callStatic",
		"__get", "__set", "__isset", "__unset", "__toString", "__clone",
		"handle", "Handle", "middleware", "boot", "register", "Register",
		"configure", "Configure", "onModuleInit", "onModuleDestroy",
		"onApplicationBootstrap", "beforeApplicationShutdown",
		"ngOnInit", "ngOnDestroy", "ngAfterViewInit",
		"componentDidMount", "componentWillUnmount",
		"get", "post", "put", "patch", "delete", "head", "options",
		"Get", "Post", "Put", "Patch", "Delete",
		"index", "show", "store", "update", "destroy", "create", "edit",
		"Index", "Show", "Store", "Update", "Destroy", "Create", "Edit",
		"up", "down", "definition", "run", "seed":
		return true
	}
	for _, p := range []string{"Test", "Benchmark", "Example", "Fuzz", "test_"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	if strings.HasSuffix(name, "Test") || strings.HasSuffix(name, "Tests") {
		return true
	}
	return false
}

// looksSyntheticOrNoise drops indexer-invented and non-code symbols that flood
// dead_code on framework/tutorial repos (synthetic routes, CSS keyframes).
func looksSyntheticOrNoise(sym types.Symbol) bool {
	n := sym.Name
	if syntheticRouteName.MatchString(n) {
		return true
	}
	if strings.HasPrefix(n, "@keyframes") || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "#") {
		return true
	}
	if strings.HasPrefix(n, "--") { // CSS custom properties
		return true
	}
	ext := strings.ToLower(filepath.Ext(sym.Path))
	switch ext {
	case ".css", ".scss", ".sass", ".less", ".md", ".mdx", ".html", ".htm", ".json", ".yaml", ".yml", ".toml":
		return true
	}
	lang := strings.ToLower(sym.Language)
	if lang == "css" || lang == "scss" {
		return true
	}
	return false
}

// dynamicDispatchSuspect flags symbols commonly invoked by a framework or
// runtime rather than by code the indexer can see. Returns a human caveat, or
// "" when the symbol looks like a plain, directly-called function.
func dynamicDispatchSuspect(sym types.Symbol) string {
	n := sym.Name
	switch {
	case strings.HasPrefix(n, "handle") || strings.HasPrefix(n, "Handle") || strings.HasSuffix(n, "Handler") ||
		strings.HasSuffix(n, "Controller") || strings.HasSuffix(n, "Middleware"):
		return "name looks like an HTTP/route handler — may be registered with a router, not called directly"
	case (strings.HasPrefix(n, "On") || strings.HasPrefix(n, "on")) && len(n) > 2 && unicode.IsUpper([]rune(n)[2]):
		return "name looks like an event callback — may be invoked dynamically"
	case strings.HasPrefix(n, "before_") || strings.HasPrefix(n, "after_"):
		return "name looks like a lifecycle/hook callback"
	case strings.HasPrefix(n, "before") && len(n) > 6 && unicode.IsUpper(rune(n[6])):
		return "name looks like a lifecycle/hook callback"
	case strings.HasPrefix(n, "after") && len(n) > 5 && unicode.IsUpper(rune(n[5])):
		return "name looks like a lifecycle/hook callback"
	case strings.HasSuffix(n, "Listener") || strings.HasSuffix(n, "Subscriber") || strings.HasSuffix(n, "Observer"):
		return "name looks like an event listener — may be registered dynamically"
	case sym.Kind == types.SymbolKindMethod:
		return "method: may satisfy an interface or be called via framework DI/dispatch"
	}
	return ""
}
