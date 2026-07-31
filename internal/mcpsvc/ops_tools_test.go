package mcpsvc

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestOpsTools_RequiredArgsActionableErrors checks the fail-fast validation on the
// ops tools returns an isError result that names the missing args, gives an
// example, and points at remote_list — instead of a confusing downstream error
// like `ssh host "" not configured`. Runs before repo resolution, so an empty
// registry suffices.
func TestOpsTools_RequiredArgsActionableErrors(t *testing.T) {
	h := AllToolHandlers(&registry.Registry{Entries: map[string]registry.Entry{}})
	ctx := context.Background()
	cases := []struct {
		tool  string
		args  map[string]any
		wants []string
	}{
		{"remote_exec", map[string]any{"recipe": "tail-log"}, []string{"host", "recipe", "remote_list"}},
		{"remote_exec", map[string]any{"host": "prod"}, []string{"host", "recipe"}},
		{"db_query", map[string]any{"sql": "SELECT 1"}, []string{"connection", "sql", "SELECT"}},
		{"db_query", map[string]any{"connection": "a"}, []string{"connection", "sql"}},
	}
	for _, tc := range cases {
		req := mcp.CallToolRequest{}
		req.Params.Name = tc.tool
		req.Params.Arguments = tc.args
		res, err := h[tc.tool](ctx, req)
		if err != nil {
			t.Fatalf("%s: protocol-level error, want isError result: %v", tc.tool, err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("%s: expected isError result for missing args, got %+v", tc.tool, res)
		}
		txt := resultText(res)
		for _, w := range tc.wants {
			if !strings.Contains(txt, w) {
				t.Errorf("%s error %q missing actionable token %q", tc.tool, txt, w)
			}
		}
	}
}

func TestConnectionsBrief_IncludesRecipesAndAliases(t *testing.T) {
	root := t.TempDir()
	var c connections.Config
	_ = c.AddSSHHost(connections.SSHHost{Name: "h", Hostname: "h.example.com", AllowedCommands: []string{"tail"}})
	_ = c.AddRecipe("h", connections.Recipe{Name: "tail-log", Argv: []string{"tail", "-n", "50", "/var/log/syslog"}})
	_ = c.AddAlias(connections.CommandAlias{Name: "logs", RemoteHost: "h", RemoteRecipe: "tail-log"})
	if err := connections.Save(root, c); err != nil {
		t.Fatal(err)
	}
	brief := connectionsBriefFor(root)
	if brief == nil || len(brief.SSHHosts) != 1 || len(brief.SSHHosts[0].Recipes) != 1 {
		t.Fatalf("recipes missing: %+v", brief)
	}
	if len(brief.Aliases) != 1 || brief.Aliases[0].RemoteRecipe != "tail-log" {
		t.Fatalf("aliases missing: %+v", brief)
	}
}

func TestOpsToolHandlersRegistered(t *testing.T) {
	reg := &registry.Registry{Entries: map[string]registry.Entry{}}
	h := AllToolHandlers(reg)
	for _, name := range []string{
		"remote_list", "remote_exec", "log_read", "db_query", "db_schema",
		"run_alias", "env_context", "ci_status",
	} {
		if _, ok := h[name]; !ok {
			t.Fatalf("missing handler %q", name)
		}
	}
}
