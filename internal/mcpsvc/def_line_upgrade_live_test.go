package mcpsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/security"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// TestContextImpactChangeKit_UpgradesTypeLikeLocLive smokes the residual Loc
// upgrade path on real Rails BaseController / Django QuerySet trees. Graph may
// already store the true line; the disk upgrade must still agree (1→def) and
// agent-facing Loc must cite the class line — never invent method lines.
func TestContextImpactChangeKit_UpgradesTypeLikeLocLive(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	root := findRepoRoot(t)
	cases := []struct {
		bed, name, pathSuffix string
		wantLine              int
	}{
		{
			bed: "rails", name: "BaseController",
			pathSuffix: "actionmailbox/app/controllers/action_mailbox/base_controller.rb",
			wantLine:   5,
		},
		{
			bed: "django", name: "QuerySet",
			pathSuffix: "django/db/models/query.py",
			wantLine:   330,
		},
	}
	for _, tc := range cases {
		t.Run(tc.bed+"/"+tc.name, func(t *testing.T) {
			bed := filepath.Join(root, ".eval-projects", tc.bed)
			meta := filepath.Join(bed, ".codehelper", "meta.json")
			if _, err := os.Stat(meta); err != nil {
				t.Skipf("no indexed %s bed: %v", tc.bed, err)
			}
			abs := filepath.Join(bed, filepath.FromSlash(tc.pathSuffix))
			if _, err := os.Stat(abs); err != nil {
				t.Fatalf("missing source %s: %v", abs, err)
			}
			// Disk helper: bogus graph line=1 must upgrade to the class def.
			if got := security.LineForSymbolDef(abs, tc.name, "class", 1); got != tc.wantLine {
				t.Fatalf("LineForSymbolDef(%s) 1→%d want %d", tc.name, got, tc.wantLine)
			}
			if keep := security.LineForSymbolDef(abs, "index", "method", 1); keep != 1 {
				t.Fatalf("method must stay line=1, got %d", keep)
			}

			reg := &registry.Registry{Entries: map[string]registry.Entry{}}
			handlers := AllToolHandlers(reg)
			ctx := workspacectx.WithRoots(bed)
			wantLoc := strings.ReplaceAll(tc.pathSuffix, "\\", "/") + ":" + itoa(tc.wantLine)

			cText := pairedCall(ctx, handlers, "context", map[string]any{
				"name": tc.name, "path": tc.pathSuffix, "format": "json", "body": "none",
			})
			if !strings.Contains(cText, wantLoc) {
				t.Fatalf("context Loc missing %q in:\n%s", wantLoc, truncateSmoke(cText, 1200))
			}

			iText := pairedCall(ctx, handlers, "impact", map[string]any{
				"target": tc.name, "path": tc.pathSuffix, "format": "json",
				"depth": 1, "max_candidates": 8, "include_tests": false,
			})
			var imp struct {
				Impact *types.ImpactResult `json:"impact"`
			}
			if err := json.Unmarshal([]byte(iText), &imp); err != nil {
				t.Fatalf("impact json: %v\n%s", err, truncateSmoke(iText, 800))
			}
			if imp.Impact == nil {
				t.Fatalf("impact missing payload:\n%s", truncateSmoke(iText, 800))
			}
			if n := len(imp.Impact.MustUpdateCandidates); n > 8 {
				t.Fatalf("max_candidates CAP≤8 weakened: got %d candidates", n)
			}
			found := false
			for _, n := range imp.Impact.Nodes {
				if n.Depth == 0 && strings.Contains(n.Loc, ":"+itoa(tc.wantLine)) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("impact depth-0 Loc missing :%d; nodes=%+v", tc.wantLine, imp.Impact.Nodes)
			}

			ckText := pairedCall(ctx, handlers, "change_kit", map[string]any{
				"target": tc.name, "path": tc.pathSuffix, "format": "json",
			})
			if !strings.Contains(ckText, wantLoc) {
				t.Fatalf("change_kit Loc missing %q in:\n%s", wantLoc, truncateSmoke(ckText, 1200))
			}

			// Forced line=1 symbolDefLoc path (simulates raw graph cite).
			forced := symbolDefLoc(bed, types.Symbol{
				Name: tc.name, Kind: types.SymbolKindClass,
				Path: tc.pathSuffix, LineStart: 1,
			})
			if forced != wantLoc {
				t.Fatalf("symbolDefLoc forced 1→ want %q got %q", wantLoc, forced)
			}
		})
	}
}
