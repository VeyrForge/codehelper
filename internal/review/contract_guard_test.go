package review

import "testing"

func TestLikelyPublic_GoExportRules(t *testing.T) {
	tests := []struct {
		path, name string
		want       bool
	}{
		{"cmd/codehelper/autowatch.go", "autoEnsureWatchDaemon", false},
		{"cmd/codehelper/root.go", "Execute", true},
		{"internal/review/heuristics.go", "IsTestPath", false}, // internal/ always private
		{"pkg/types/types.go", "Symbol", true},
		{"app/api/route.ts", "GET", true}, // non-Go path heuristic
		{"lib/util.ts", "helper", false},
	}
	for _, tc := range tests {
		if got := likelyPublic(tc.path, tc.name); got != tc.want {
			t.Errorf("likelyPublic(%q, %q)=%v want %v", tc.path, tc.name, got, tc.want)
		}
	}
}
