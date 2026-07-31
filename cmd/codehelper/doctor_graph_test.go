package main

import "testing"

func TestHasGraphQualityWarning(t *testing.T) {
	t.Parallel()
	cases := []struct {
		w    string
		want bool
	}{
		{"primary language \"php\" has 10 symbols but 0 call/import edges (inventory-only) — …", true},
		{"primary language \"kt\" has 10 symbols but 0 call edges (contains-only) — …", true},
		{"graph looks contains-only (edge_count=40 ≈ symbol_count=40) — …", true},
		{"primary language \"gdscript\" call graph is sparse (0.020 calls/symbol; …", true},
		{"primary language \"elixir\" has 0 indexed symbols — …", true},
		{"watch daemon is not running; consider `codehelper watch --daemon`", false},
		{"browser tier not compiled into this binary", false},
	}
	for _, tc := range cases {
		if got := hasGraphQualityWarning([]string{tc.w}); got != tc.want {
			t.Errorf("%q → %v want %v", tc.w, got, tc.want)
		}
	}
}
