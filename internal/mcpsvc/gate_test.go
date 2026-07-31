package mcpsvc

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/VeyrForge/codehelper/internal/projcfg"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/usage"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestToolAllowsDisabledShadow_AllowlistOnly(t *testing.T) {
	for name := range disabledShadowAllowlist {
		if !toolAllowsDisabledShadow(name) {
			t.Fatalf("%s is on disabledShadowAllowlist and should allow shadow", name)
		}
	}
	for _, name := range []string{
		"write_workspace_file", "apply_patch_workspace_file", "verify",
		"browser", "remote_exec", "agent_execute_todo", "docs_add", "run_alias",
		// annotReadOnly* but NOT pure-graph — must never shadow when disabled
		"diagnostics", "orchestrate", "db_query", "db_schema",
		"kickoff", "docs", "web_search", "lsp", "ci_status",
		"read_workspace_file", "change_kit", "investigate", "context_bundle",
	} {
		if toolAllowsDisabledShadow(name) {
			t.Fatalf("%s must NOT shadow-execute when tools disabled", name)
		}
	}
}

// TestRegisteredToolsHaveReadOnlyHint keeps annotation completeness separate from
// the disabled-mode shadow allowlist (which is explicit and not derived from hints).
func TestRegisteredToolsHaveReadOnlyHint(t *testing.T) {
	reg := &registry.Registry{Entries: map[string]registry.Entry{}}
	srv := server.NewMCPServer("codehelper-shadow-annot", "0")
	RegisterAll(srv, reg)
	tools := srv.ListTools()

	var unclassified []string
	for name, st := range tools {
		if st.Tool.Annotations.ReadOnlyHint == nil {
			unclassified = append(unclassified, name)
		}
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		t.Fatalf("registered tools missing ReadOnlyHint classification (add annotReadOnly* or a non-RO annot*): %v", unclassified)
	}
}

// TestDisabledShadowNotDerivedFromReadOnlyHint proves RegisterAll no longer syncs
// the live shadow set from ReadOnlyHint — RO-but-side-effect tools stay blocked.
func TestDisabledShadowNotDerivedFromReadOnlyHint(t *testing.T) {
	reg := &registry.Registry{Entries: map[string]registry.Entry{}}
	srv := server.NewMCPServer("codehelper-shadow-allowlist", "0")
	RegisterAll(srv, reg)
	tools := srv.ListTools()

	for name := range disabledShadowAllowlist {
		st, ok := tools[name]
		if !ok {
			t.Errorf("allowlisted shadow tool %q is not registered", name)
			continue
		}
		if h := st.Tool.Annotations.ReadOnlyHint; h == nil || !*h {
			t.Errorf("allowlisted shadow tool %q must still be ReadOnlyHint=true", name)
		}
	}

	mustNotShadow := []string{
		"diagnostics", "orchestrate", "db_query", "db_schema",
		"kickoff", "docs", "web_search", "lsp", "ci_status",
	}
	for _, name := range mustNotShadow {
		st, ok := tools[name]
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		if h := st.Tool.Annotations.ReadOnlyHint; h == nil || !*h {
			t.Errorf("%s expected ReadOnlyHint=true (regression subject), got %v", name, h)
		}
		if toolAllowsDisabledShadow(name) {
			t.Errorf("%s is ReadOnlyHint but must NOT shadow when tools disabled", name)
		}
	}

	for _, name := range []string{
		"write_workspace_file", "apply_patch_workspace_file", "verify",
		"browser", "remote_exec", "agent_execute_todo", "docs_add", "run_alias",
		"hints", "glossary", "agent_memory", "web", "orchestration_feedback",
	} {
		if toolAllowsDisabledShadow(name) {
			t.Errorf("%s must not shadow-execute when tools disabled", name)
		}
	}
}

func TestGateMiddleware_SkipsSideEffectWhenDisabled(t *testing.T) {
	root := t.TempDir()
	cfg := projcfg.Default()
	cfg.ToolsEnabled = false
	cfg.Track = projcfg.TrackOff
	if err := projcfg.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	projCfgCache.Delete(root)

	prev := usageRecorder
	usageRecorder = usage.NewRecorder()
	t.Cleanup(func() { usageRecorder = prev })
	_ = usageRecorder.RepoRoot("", "", func() string { return root })

	mustSkip := []string{
		"write_workspace_file",
		"diagnostics", "orchestrate",
		"db_query", "db_schema",
		"kickoff", "docs", "web_search", "lsp", "ci_status",
	}
	for _, toolName := range mustSkip {
		t.Run(toolName, func(t *testing.T) {
			var called atomic.Int32
			handler := gateMiddleware(nil)(func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				called.Add(1)
				return mcp.NewToolResultText("side-effect-ran"), nil
			})

			req := mcp.CallToolRequest{}
			req.Params.Name = toolName
			res, err := handler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			if called.Load() != 0 {
				t.Fatalf("%s must not call next when tools disabled; calls=%d", toolName, called.Load())
			}
			if res == nil || len(res.Content) == 0 {
				t.Fatal("expected disabled notice")
			}
			txt := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(txt, "disabled") {
				t.Fatalf("expected disabled notice, got %q", txt)
			}
		})
	}
}

func TestGateMiddleware_ShadowsAllowlistWhenDisabled(t *testing.T) {
	root := t.TempDir()
	cfg := projcfg.Default()
	cfg.ToolsEnabled = false
	cfg.Track = projcfg.TrackOff
	if err := projcfg.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	projCfgCache.Delete(root)

	prev := usageRecorder
	usageRecorder = usage.NewRecorder()
	t.Cleanup(func() { usageRecorder = prev })
	_ = usageRecorder.RepoRoot("", "", func() string { return root })

	for name := range disabledShadowAllowlist {
		t.Run(name, func(t *testing.T) {
			var called atomic.Int32
			handler := gateMiddleware(nil)(func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				called.Add(1)
				return mcp.NewToolResultText("shadow-ok"), nil
			})

			req := mcp.CallToolRequest{}
			req.Params.Name = name
			res, err := handler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			if called.Load() != 1 {
				t.Fatalf("allowlisted tool should shadow-execute once; calls=%d", called.Load())
			}
			txt := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(txt, "disabled") {
				t.Fatalf("agent must still get disabled notice, got %q", txt)
			}
			if strings.Contains(txt, "shadow-ok") {
				t.Fatal("agent must not see shadow result")
			}
		})
	}
}

// Ensure server.ToolHandlerMiddleware type stays wired (compile check).
var _ server.ToolHandlerMiddleware = gateMiddleware(nil)
