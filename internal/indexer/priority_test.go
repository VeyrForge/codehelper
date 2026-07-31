//go:build !windows

package indexer

import "testing"

func TestParseThreadNice(t *testing.T) {
	// Unset env: defaults to 10 (a clean test env has no CODEHELPER_NICE).
	if got := parseThreadNice(); got != 10 {
		t.Fatalf("parseThreadNice() unset = %d, want 10", got)
	}

	cases := []struct {
		name string
		env  string
		want int
	}{
		{"default when empty", "", 10},
		{"explicit value", "5", 5},
		{"zero disables", "0", 0},
		{"clamped to max", "42", 19},
		{"negative falls back to default", "-3", 10},
		{"garbage falls back to default", "abc", 10},
		{"whitespace trimmed", "  7 ", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODEHELPER_NICE", tc.env)
			if got := parseThreadNice(); got != tc.want {
				t.Fatalf("parseThreadNice() with %q = %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}
