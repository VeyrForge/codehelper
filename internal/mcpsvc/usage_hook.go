package mcpsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/usage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// usageRecorder holds per-session state (client, in-flight call timings, resolved
// repo root) so the usage hooks can turn every tool call into a persisted Event.
var usageRecorder = usage.NewRecorder()

// registerUsageHooks wires per-project tool-usage recording onto the server hook
// set. It captures the client at initialize, times each call, measures the
// response size codehelper injected into the agent's context, and appends an
// Event to the resolved project's usage log. Everything is best-effort — a
// resolution or write failure is silently dropped so recording never affects the
// tool result.
func registerUsageHooks(h *server.Hooks, reg *registry.Registry) {
	h.AddAfterInitialize(func(ctx context.Context, _ any, req *mcp.InitializeRequest, _ *mcp.InitializeResult) {
		usageRecorder.SetClient(sessionIDFromContext(ctx), canonicalClient(req.Params.ClientInfo.Name))
	})

	h.AddBeforeCallTool(func(ctx context.Context, id any, _ *mcp.CallToolRequest) {
		usageRecorder.Begin(callKey(ctx, id), time.Now())
	})

	h.AddAfterCallTool(func(ctx context.Context, id any, req *mcp.CallToolRequest, res any) {
		// Always clear the in-flight start entry (even on an early return) so the
		// recorder's timing map doesn't leak one entry per skipped call.
		latency := usageRecorder.Elapsed(callKey(ctx, id), time.Now())
		repoRoot := gateRepoRoot(ctx, reg, req)
		if repoRoot == "" {
			return
		}
		cfg := gateConfig(repoRoot)
		// Tools-disabled calls are recorded by the gate middleware (which has the
		// real shadow result); here we'd only see the redirect notice. And with
		// recording off there is nothing to do. Either way, skip.
		if !cfg.Recording() || !cfg.ToolsEnabled {
			return
		}
		recordCall(ctx, repoRoot, req, usageResultText(res), latency, callResultIsError(res), false)
	})

	h.AddOnUnregisterSession(func(_ context.Context, sess server.ClientSession) {
		usageRecorder.Forget(sess.SessionID())
	})
}

// recordCall appends one usage Event for an already-resolved project. It is the
// single place both paths converge: the AfterCallTool hook (tools enabled, the
// agent got the result) and the gate middleware (tools disabled, the result was
// shadow-recorded only). disabled distinguishes the two in the log.
func recordCall(ctx context.Context, repoRoot string, req *mcp.CallToolRequest, respText string, latency int64, isError, disabled bool) {
	session := sessionIDFromContext(ctx)
	ev := usage.Event{
		TS:         time.Now().UTC(),
		Session:    session,
		Client:     usageRecorder.Client(session),
		Tool:       req.Params.Name,
		RepoArg:    argRepo(req),
		Args:       usage.Preview(requestArgString(req), usage.MaxArgsChars),
		Snippet:    usage.Preview(respText, usage.MaxSnippetChars),
		ReqBytes:   requestArgBytes(req),
		RespBytes:  len(respText),
		RespTokens: usage.EstimateTokens(respText),
		LatencyMS:  latency,
		IsError:    isError,
		Disabled:   disabled,
	}
	_ = usage.Append(repoRoot, ev)
}

// canonicalClient folds the many client identifier spellings into the three we
// report on, so "claude-code", "Claude Code", "claude-ai" all bucket together.
func canonicalClient(raw string) string {
	l := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case l == "":
		return "unknown"
	case strings.Contains(l, "claude"):
		return "claude-code"
	case strings.Contains(l, "cursor"):
		return "cursor"
	case strings.Contains(l, "codex"):
		return "codex"
	default:
		return l
	}
}

// resultText concatenates the text content blocks of a tool result — i.e. what
// codehelper put into the agent's context. (A same-named helper exists in the
// test files; this is the production one used by the usage hook.)
func usageResultText(res any) string {
	r, ok := res.(*mcp.CallToolResult)
	if !ok || r == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func requestArgBytes(req *mcp.CallToolRequest) int {
	return len(requestArgString(req))
}

// requestArgString is a compact, identifier-first preview of the call arguments —
// the "input" side of the reviewable log (capped by the caller via usage.Preview).
//
// It deliberately does NOT use json.Marshal of the args map: that sorts keys
// alphabetically, so bulky fields like `hunks`/`content` came first and pushed the
// identifying `path`/`symbol` past the preview cap — making the command log unable
// to show WHICH file an edit touched. Here the high-signal keys are emitted first
// and large values are elided to a size, so the path/symbol/action always survive.
func requestArgString(req *mcp.CallToolRequest) string {
	args := req.GetArguments()
	if len(args) == 0 {
		return ""
	}
	priority := []string{"repo", "path", "action", "name", "symbol", "term", "query", "definition", "base_ref", "position", "line"}
	var parts []string
	seen := map[string]bool{}
	emit := func(k string) {
		v, ok := args[k]
		if !ok || seen[k] {
			return
		}
		seen[k] = true
		parts = append(parts, k+"="+previewArgValue(v))
	}
	for _, k := range priority {
		emit(k)
	}
	rest := make([]string, 0, len(args))
	for k := range args {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		emit(k)
	}
	return strings.Join(parts, " ")
}

// previewArgValue renders one argument value compactly: short scalars inline, and
// large strings / arrays / objects elided to a size so they never crowd out the
// identifying fields in the capped preview.
func previewArgValue(v any) string {
	switch val := v.(type) {
	case string:
		if len(val) > 80 {
			return fmt.Sprintf("<%d chars>", len(val))
		}
		return val
	case []any:
		return fmt.Sprintf("<%d items>", len(val))
	case map[string]any:
		return fmt.Sprintf("<%d fields>", len(val))
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(val)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
