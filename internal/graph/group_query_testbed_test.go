package graph

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// TestFanOutGroupQuery_LiveWorkspaceGroupsTestbed exercises indexed sibling beds
// produced by scripts/prepare-workspace-groups-testbed.sh.
func TestFanOutGroupQuery_LiveWorkspaceGroupsTestbed(t *testing.T) {
	base := os.Getenv("CODEHELPER_WORKSPACE_GROUPS_TESTBED")
	if base == "" {
		t.Skip("set CODEHELPER_WORKSPACE_GROUPS_TESTBED after scripts/prepare-workspace-groups-testbed.sh")
	}
	ctx := context.Background()
	var members []GroupMemberDB
	for _, repo := range []string{"api", "web"} {
		root := filepath.Join(base, repo)
		db := paths.DBPath(root)
		if _, err := os.Stat(db); err != nil {
			t.Skipf("missing indexed bed %s (%v)", repo, err)
		}
		st, err := Open(db)
		if err != nil {
			t.Fatalf("open %s: %v", repo, err)
		}
		t.Cleanup(func() { _ = st.Close() })
		members = append(members, GroupMemberDB{Repo: repo, Store: st})
	}
	got := FanOutGroupQuery(ctx, "platform", "UserService", "", 10, members)
	if got.Count < 2 {
		t.Fatalf("expected UserService in api+web, got %#v", got.Hits)
	}
	if !got.Ambiguous {
		t.Fatal("expected ambiguous UserService across siblings")
	}
}
