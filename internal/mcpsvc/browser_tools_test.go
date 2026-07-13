package mcpsvc

import "testing"

func TestParseAudit(t *testing.T) {
	cases := []struct {
		name  string
		args  map[string]any
		audit bool
		full  bool
	}{
		{"unset", map[string]any{}, false, false},
		{"bool true (legacy)", map[string]any{"audit": true}, true, false},
		{"bool false", map[string]any{"audit": false}, false, false},
		{"string lite", map[string]any{"audit": "lite"}, true, false},
		{"string full", map[string]any{"audit": "full"}, true, true},
		{"string true (coerced bool)", map[string]any{"audit": "true"}, true, false},
		{"string FULL caps", map[string]any{"audit": "FULL"}, true, true},
		{"garbage string", map[string]any{"audit": "yep"}, false, false},
	}
	for _, c := range cases {
		audit, full := parseAudit(c.args)
		if audit != c.audit || full != c.full {
			t.Errorf("%s: parseAudit=(%v,%v) want (%v,%v)", c.name, audit, full, c.audit, c.full)
		}
	}
}
