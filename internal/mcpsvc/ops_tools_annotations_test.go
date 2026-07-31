package mcpsvc

import (
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/server"
)

// TestOpsToolAnnotationsMatchNetworkReach guards the approval-metadata contract
// clients use for consent UI: a tool that reaches an external system (SSH host,
// configured database, GitHub API) must advertise openWorldHint=true so a strict
// client does not treat it the same as a purely local/offline read. Regression
// for db_query/db_schema/ci_status previously being misclassified closed-world.
func TestOpsToolAnnotationsMatchNetworkReach(t *testing.T) {
	reg, err := registry.Load()
	if err != nil {
		t.Skipf("registry load failed: %v", err)
	}
	srv := server.NewMCPServer("codehelper-annot-test", "0")
	RegisterAll(srv, reg)
	tools := srv.ListTools()

	openWorld := []string{"remote_exec", "db_query", "db_schema", "ci_status"}
	for _, name := range openWorld {
		st, ok := tools[name]
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		hint := st.Tool.Annotations.OpenWorldHint
		if hint == nil || !*hint {
			t.Errorf("%s: want openWorldHint=true (reaches an external system), got %v", name, hint)
		}
		if ro := st.Tool.Annotations.ReadOnlyHint; name != "remote_exec" && (ro == nil || !*ro) {
			t.Errorf("%s: want readOnlyHint=true, got %v", name, ro)
		}
	}

	closedWorld := []string{"remote_list", "log_read", "env_context"}
	for _, name := range closedWorld {
		st, ok := tools[name]
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		hint := st.Tool.Annotations.OpenWorldHint
		if hint == nil || *hint {
			t.Errorf("%s: want openWorldHint=false (local-only), got %v", name, hint)
		}
	}
}
