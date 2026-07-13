package mcpsvc

import (
	"sort"
	"testing"
)

func TestMCPToolCatalogComplete(t *testing.T) {
	names := AllMCPToolNames()
	if len(names) != 60 {
		t.Fatalf("expected 60 MCP tools, got %d: %v", len(names), names)
	}
	seen := map[string]struct{}{}
	for _, n := range names {
		if _, dup := seen[n]; dup {
			t.Fatalf("duplicate tool name in catalog: %s", n)
		}
		seen[n] = struct{}{}
	}
	brief := MCPToolCatalogBrief()
	if brief.Count != len(names) {
		t.Fatalf("brief count %d != len(names) %d", brief.Count, len(names))
	}
	if brief.ParamKeys == "" || brief.ContractPath != "AGENTS.md" {
		t.Fatalf("unexpected brief catalog: %+v", brief)
	}
	full := MCPToolCatalogFull()
	var grouped int
	for _, g := range full.ByGroup {
		grouped += len(g)
	}
	if grouped != len(names) {
		t.Fatalf("grouped tools %d != all names %d", grouped, len(names))
	}
}

func TestMCPMainToolsSubsetOfCatalog(t *testing.T) {
	all := map[string]struct{}{}
	for _, n := range AllMCPToolNames() {
		all[n] = struct{}{}
	}
	for _, m := range MCPMainTools {
		if _, ok := all[m]; !ok {
			t.Fatalf("main tool %q not in catalog", m)
		}
	}
}

func TestAllMCPToolNamesSorted(t *testing.T) {
	names := AllMCPToolNames()
	if !sort.StringsAreSorted(names) {
		t.Fatalf("names not sorted: %v", names)
	}
}
