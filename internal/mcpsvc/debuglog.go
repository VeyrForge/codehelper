package mcpsvc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// mcpLog is the package-global MCP diagnostic logger. It is set once by Run and
// read by the request hooks and the roots-resolution code in register.go. All
// methods are nil-safe so callers never have to guard.
var mcpLog *mcpDebugLogger

// mcpDebugLogger writes a JSONL trace of the MCP session to
// ~/.codehelper/logs/mcp.log so we can see EXACTLY what a real client
// (Claude Code, Cursor, Codex) sends — protocol version, capabilities, every
// tool call, every error, and how the workspace root resolves. We log to a file
// rather than stderr on purpose: some clients treat any stderr output from an
// MCP server as a failure signal, and stderr is invisible to the user anyway.
//
// On by default; set CODEHELPER_MCP_LOG=off (or 0/false) to disable.
type mcpDebugLogger struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	pid     int
	// starts tracks per-request begin times so the *after* hook can report a
	// duration. Keyed by sessionID + "/" + request id.
	starts sync.Map
}

const mcpLogMaxSize = 8 << 20 // 8 MiB, then rotate to mcp.log.1

// newMCPDebugLogger returns a logger writing to ~/.codehelper/logs/mcp.log, or
// nil when disabled or the log dir can't be created (logging must never block
// the server from starting).
func newMCPDebugLogger() *mcpDebugLogger {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEHELPER_MCP_LOG"))) {
	case "off", "0", "false", "no":
		return nil
	}
	dir, err := paths.RegistryDir()
	if err != nil {
		return nil
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil
	}
	return &mcpDebugLogger{
		path:    filepath.Join(logDir, "mcp.log"),
		maxSize: mcpLogMaxSize,
		pid:     os.Getpid(),
	}
}

// Path returns the log file path (for surfacing to operators); "" if disabled.
func (l *mcpDebugLogger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// event appends one JSONL record. Best-effort: any error is swallowed so a
// logging failure can never take down a tool call.
func (l *mcpDebugLogger) event(kind string, fields map[string]any) {
	if l == nil {
		return
	}
	rec := map[string]any{
		"ts":    time.Now().Format(time.RFC3339Nano),
		"pid":   l.pid,
		"event": kind,
	}
	for k, v := range fields {
		rec[k] = v
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rotateIfNeededLocked()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// rotateIfNeededLocked moves mcp.log -> mcp.log.1 once it exceeds maxSize so the
// trace stays bounded. Caller holds l.mu.
func (l *mcpDebugLogger) rotateIfNeededLocked() {
	info, err := os.Stat(l.path)
	if err != nil || info.Size() < l.maxSize {
		return
	}
	_ = os.Rename(l.path, l.path+".1")
}

func sessionIDFromContext(ctx context.Context) string {
	if s := server.ClientSessionFromContext(ctx); s != nil {
		return s.SessionID()
	}
	return ""
}

// shortID trims a long session UUID to something greppable but compact.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// addHooks registers the debug-log callbacks onto an existing hook set. Logging
// hooks are additive — capability tracking (registerCapabilityHooks) is wired
// separately and always on, so roots gating works even when logging is disabled.
func (l *mcpDebugLogger) addHooks(h *server.Hooks) {
	h.AddOnRegisterSession(func(ctx context.Context, session server.ClientSession) {
		l.event("session_open", map[string]any{"sid": shortID(session.SessionID())})
	})
	h.AddOnUnregisterSession(func(ctx context.Context, session server.ClientSession) {
		l.event("session_close", map[string]any{"sid": shortID(session.SessionID())})
	})

	// The single most useful record: what the client advertised. If roots is
	// nil here, the server can only scope by spawn CWD — the usual reason a
	// "connected" client still resolves the wrong (or no) project.
	h.AddAfterInitialize(func(ctx context.Context, id any, req *mcp.InitializeRequest, res *mcp.InitializeResult) {
		l.event("initialize", map[string]any{
			"sid":              shortID(sessionIDFromContext(ctx)),
			"client":           req.Params.ClientInfo.Name,
			"client_version":   req.Params.ClientInfo.Version,
			"client_protocol":  req.Params.ProtocolVersion,
			"server_protocol":  res.ProtocolVersion,
			"caps_roots":       req.Params.Capabilities.Roots != nil,
			"caps_sampling":    req.Params.Capabilities.Sampling != nil,
			"caps_elicitation": req.Params.Capabilities.Elicitation != nil,
		})
	})

	h.AddAfterListTools(func(ctx context.Context, id any, req *mcp.ListToolsRequest, res *mcp.ListToolsResult) {
		n := 0
		if res != nil {
			n = len(res.Tools)
		}
		l.event("tools_list", map[string]any{"sid": shortID(sessionIDFromContext(ctx)), "tools": n})
	})

	h.AddBeforeCallTool(func(ctx context.Context, id any, req *mcp.CallToolRequest) {
		l.starts.Store(callKey(ctx, id), time.Now())
		l.event("call_begin", map[string]any{
			"sid":  shortID(sessionIDFromContext(ctx)),
			"tool": req.Params.Name,
			"repo": argRepo(req),
		})
	})
	h.AddAfterCallTool(func(ctx context.Context, id any, req *mcp.CallToolRequest, res any) {
		l.event("call_end", map[string]any{
			"sid":     shortID(sessionIDFromContext(ctx)),
			"tool":    req.Params.Name,
			"ms":      l.elapsedMS(ctx, id),
			"isError": callResultIsError(res),
		})
	})

	h.AddOnError(func(ctx context.Context, id any, method mcp.MCPMethod, message any, err error) {
		l.event("error", map[string]any{
			"sid":    shortID(sessionIDFromContext(ctx)),
			"method": string(method),
			"err":    err.Error(),
		})
	})
}

func callKey(ctx context.Context, id any) string {
	return sessionIDFromContext(ctx) + "/" + jsonScalar(id)
}

func (l *mcpDebugLogger) elapsedMS(ctx context.Context, id any) int64 {
	k := callKey(ctx, id)
	v, ok := l.starts.LoadAndDelete(k)
	if !ok {
		return -1
	}
	return time.Since(v.(time.Time)).Milliseconds()
}

func jsonScalar(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// argRepo extracts the "repo" argument so the log shows whether the client
// pinned a project or relied on workspace-root resolution.
func argRepo(req *mcp.CallToolRequest) string {
	args := req.GetArguments()
	if args == nil {
		return ""
	}
	if r, ok := args["repo"].(string); ok {
		return r
	}
	return ""
}

func callResultIsError(res any) bool {
	if r, ok := res.(*mcp.CallToolResult); ok {
		return r.IsError
	}
	return false
}
