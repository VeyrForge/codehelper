//go:build ch_modules

package product_test

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/product"
)

// Compile-time smoke for selective builds. Run with e.g.:
//
//	go test -tags 'ch_modules,ch_edit' ./internal/product/ -run Select
func TestSelectModeCompile(t *testing.T) {
	if !product.SelectMode() {
		t.Fatal("expected SelectMode with ch_modules tag")
	}
	cat := product.Catalog()
	if len(cat) != 6 {
		t.Fatalf("Catalog len=%d want 6", len(cat))
	}
	sum := product.Summary()
	if sum == "" || strings.HasPrefix(sum, "full bundle") {
		t.Fatalf("Summary=%q want selective module list", sum)
	}
	if !strings.Contains(sum, "codehelper-core") {
		t.Fatalf("Summary=%q must list codehelper-core", sum)
	}
	ids := product.EnabledIDs()
	if len(ids) < 1 || ids[0] != product.Core {
		t.Fatalf("EnabledIDs=%v must start with core", ids)
	}
	if !product.ToolEnabled("query") {
		t.Fatal("core tool query must stay enabled")
	}
}

func TestSelectModeRespectsTags(t *testing.T) {
	// Assertions are soft: only check consistency between Enabled* and ToolEnabled.
	if product.EditEnabled() != product.ToolEnabled("rename_symbol") {
		t.Fatalf("edit flag/tool mismatch: Edit=%v rename=%v", product.EditEnabled(), product.ToolEnabled("rename_symbol"))
	}
	if product.CheckEnabled() != product.ToolEnabled("review") {
		t.Fatalf("check flag/tool mismatch: Check=%v review=%v", product.CheckEnabled(), product.ToolEnabled("review"))
	}
	if product.BrowserEnabled() != product.ToolEnabled("browser") {
		t.Fatalf("browser flag/tool mismatch: Browser=%v browser=%v", product.BrowserEnabled(), product.ToolEnabled("browser"))
	}
	if product.OpsEnabled() != product.ToolEnabled("remote_list") {
		t.Fatalf("ops flag/tool mismatch: Ops=%v remote_list=%v", product.OpsEnabled(), product.ToolEnabled("remote_list"))
	}
}
