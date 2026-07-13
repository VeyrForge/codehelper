package mcpsvc

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/ops"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterOpsTools wires security-gated external operation tools (logs, SSH, DB, aliases, env, CI).
func RegisterOpsTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg

	s.AddTool(mcp.NewTool("remote_list",
		mcp.WithDescription("Read-only map of configured SSH hosts, DB connections, log sources, and command aliases for this project. Secrets are never returned — configure via `codehelper connections` CLI."),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("remote_list", remoteListHandler(regRef)))

	s.AddTool(mcp.NewTool("remote_exec",
		mcp.WithDescription("Run a NAMED recipe on a configured SSH host (never free-form shell). Host and recipe must exist in connections config; remote argv must be on the host allowlist."),
		mcp.WithString("host", mcp.Required(), mcp.Description("SSH host profile name")),
		mcp.WithString("recipe", mcp.Required(), mcp.Description("Recipe name on that host")),
		mcp.WithString("params", mcp.Description("JSON object of recipe params, e.g. {\"lines\":\"100\",\"path\":\"/var/log/nginx/error.log\"}")),
		mcp.WithNumber("timeout_seconds", mcp.Description("Timeout (default 30, max 120)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotOpenWorld(),
	), timedTool("remote_exec", remoteExecHandler(regRef)))

	s.AddTool(mcp.NewTool("log_read",
		mcp.WithDescription("Tail a configured LOCAL log source (from connections add-log). Remote logs use remote_exec with a tail recipe."),
		mcp.WithString("source", mcp.Required(), mcp.Description("Log source name")),
		mcp.WithNumber("lines", mcp.Description("Lines to tail (default 200, max 1000)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("log_read", logReadHandler(regRef)))

	s.AddTool(mcp.NewTool("db_query",
		mcp.WithDescription("Read-only SQL against a configured database profile. DDL/DML blocked. SQLite in-process; other drivers coming soon."),
		mcp.WithString("connection", mcp.Required(), mcp.Description("Database profile name")),
		mcp.WithString("sql", mcp.Required(), mcp.Description("SELECT query")),
		mcp.WithNumber("max_rows", mcp.Description("Row cap (default 100)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("db_query", dbQueryHandler(regRef)))

	s.AddTool(mcp.NewTool("db_schema",
		mcp.WithDescription("Schema introspection for a configured sqlite database. Optional comma-separated table filter."),
		mcp.WithString("connection", mcp.Required(), mcp.Description("Database profile name")),
		mcp.WithString("tables", mcp.Description("Comma-separated table names (default: all, max 50)")),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("db_schema", dbSchemaHandler(regRef)))

	s.AddTool(mcp.NewTool("run_alias",
		mcp.WithDescription("Run a user-configured command alias (declarative argv or remote recipe). Aliases with requires_approval need approved=true."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Alias name")),
		mcp.WithString("params", mcp.Description("JSON params for remote aliases")),
		mcp.WithBoolean("approved", mcp.Description("User confirmed destructive/approval-gated alias"), mcp.DefaultBool(false)),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotVerify(),
	), timedTool("run_alias", runAliasHandler(regRef)))

	s.AddTool(mcp.NewTool("env_context",
		mcp.WithDescription("Detect toolchain versions, npm/make scripts, docker-compose hint, and configured aliases/log sources from project files — one call, no shell."),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("env_context", envContextHandler(regRef)))

	s.AddTool(mcp.NewTool("ci_status",
		mcp.WithDescription("Read-only GitHub PR and workflow run summary via gh CLI. Requires connections policy github config and env:GITHUB_TOKEN."),
		mcp.WithString("repo", mcp.Description("Repository name")),
		mcp.WithString("format", mcp.Description("toon (default) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("ci_status", ciStatusHandler(regRef)))
}

func remoteListHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		repo, err := resolveRepoInitialized(ctx, reg, argString(req.GetArguments(), "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := ops.ListCapabilities(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mustToolResultFormatted(out, resolveFormat(req.GetArguments()))
	}
}

func remoteExecHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		host := strings.TrimSpace(argString(args, "host"))
		recipe := strings.TrimSpace(argString(args, "recipe"))
		if host == "" || recipe == "" {
			return mcp.NewToolResultError("remote_exec needs both host and recipe — e.g. host=\"prod-web\" recipe=\"tail-log\". Call remote_list to see configured hosts and the recipes each one allows."), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		params := parseJSONMap(argString(args, "params"))
		timeout := time.Duration(int(mcp.ParseInt64(req, "timeout_seconds", 30))) * time.Second
		out, err := ops.ExecRecipe(ctx, repo.RootPath, host, recipe, params, timeout)
		if err != nil && out == nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		body, _ := ops.MarshalJSON(out)
		if err != nil {
			return mcp.NewToolResultText(body + "\nerror: " + err.Error()), nil
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func logReadHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		lines := int(mcp.ParseInt64(req, "lines", 200))
		out, err := ops.ReadLog(ctx, repo.RootPath, argString(args, "source"), lines)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func dbQueryHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		conn := strings.TrimSpace(argString(args, "connection"))
		sqlText := strings.TrimSpace(argString(args, "sql"))
		if conn == "" || sqlText == "" {
			return mcp.NewToolResultError("db_query needs both connection and sql — e.g. connection=\"analytics\" sql=\"SELECT id, name FROM users LIMIT 20\". Call remote_list to see configured database connections (read-only SELECT only)."), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		maxRows := int(mcp.ParseInt64(req, "max_rows", 100))
		out, err := ops.QueryDB(ctx, repo.RootPath, conn, sqlText, maxRows)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func dbSchemaHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var tables []string
		if raw := argString(args, "tables"); raw != "" {
			for _, p := range strings.Split(raw, ",") {
				if p = strings.TrimSpace(p); p != "" {
					tables = append(tables, p)
				}
			}
		}
		out, err := ops.SchemaDB(ctx, repo.RootPath, argString(args, "connection"), tables)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func runAliasHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		approved := false
		if v, ok := args["approved"].(bool); ok {
			approved = v
		}
		out, err := ops.RunAlias(ctx, repo.RootPath, argString(args, "name"), parseJSONMap(argString(args, "params")), approved)
		if err != nil && out == nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err != nil {
			body, _ := ops.MarshalJSON(out)
			return mcp.NewToolResultText(body + "\nerror: " + err.Error()), nil
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func envContextHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		repo, err := resolveRepoInitialized(ctx, reg, argString(req.GetArguments(), "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := ops.DetectEnv(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mustToolResultFormatted(out, resolveFormat(req.GetArguments()))
	}
}

func ciStatusHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		repo, err := resolveRepoInitialized(ctx, reg, argString(req.GetArguments(), "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := ops.CIStatus(ctx, repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mustToolResultFormatted(out, resolveFormat(req.GetArguments()))
	}
}

func parseJSONMap(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var m map[string]string
	if json.Unmarshal([]byte(raw), &m) == nil {
		return m
	}
	var anyMap map[string]any
	if json.Unmarshal([]byte(raw), &anyMap) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range anyMap {
		out[k] = fmtAny(v)
	}
	return out
}

func fmtAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
