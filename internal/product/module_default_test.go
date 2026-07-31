//go:build !ch_modules

package product

import (
	"strings"
	"testing"
)

func TestDefaultFullBundle(t *testing.T) {
	if SelectMode() {
		t.Fatal("default test binary must not use ch_modules")
	}
	if !EditEnabled() || !CheckEnabled() || !BrowserEnabled() || !OpsEnabled() {
		t.Fatalf("default full bundle must enable edit/check/browser/ops; got edit=%v check=%v browser=%v ops=%v",
			EditEnabled(), CheckEnabled(), BrowserEnabled(), OpsEnabled())
	}
	if TeamEnabled() {
		t.Fatal("team must stay opt-in in the default full bundle")
	}
	sum := Summary()
	if !strings.Contains(sum, "full bundle") {
		t.Fatalf("Summary=%q want full bundle", sum)
	}
	ids := EnabledIDs()
	if len(ids) != 5 {
		t.Fatalf("EnabledIDs=%v want 5 (core+edit+check+browser+ops)", ids)
	}
}

func TestToolModuleCoverage(t *testing.T) {
	want := map[string]ID{
		"rename_symbol":    Edit,
		"insert_at_symbol": Edit,
		"review":           Check,
		"browser":          Browser,
		"remote_list":      Ops,
		"ci_status":        Ops,
	}
	for name, mod := range want {
		if got := ToolModule(name); got != mod {
			t.Errorf("ToolModule(%q)=%q want %q", name, got, mod)
		}
		if !ToolEnabled(name) {
			t.Errorf("ToolEnabled(%q)=false in default full bundle", name)
		}
	}
	if ToolModule("query") != Core {
		t.Errorf("query should be core")
	}
	if !ToolEnabled("query") {
		t.Errorf("core tools must stay enabled")
	}
}

func TestCatalogStable(t *testing.T) {
	c := Catalog()
	if len(c) != 6 {
		t.Fatalf("Catalog len=%d want 6", len(c))
	}
	seen := map[ID]bool{}
	for _, m := range c {
		if m.Name == "" || m.Purpose == "" {
			t.Errorf("module %+v missing name/purpose", m)
		}
		if seen[m.ID] {
			t.Errorf("duplicate module id %q", m.ID)
		}
		seen[m.ID] = true
		if m.ID != Core && m.BuildTag == "" {
			t.Errorf("non-core module %q needs BuildTag", m.ID)
		}
	}
	for _, id := range []ID{Core, Edit, Check, Browser, Ops, Team} {
		if !seen[id] {
			t.Errorf("missing module %q", id)
		}
	}
}
