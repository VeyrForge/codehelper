package security

import "testing"

func TestScanDiffForSecuritySmells(t *testing.T) {
	lines := []AddedDiffLine{
		{File: "auth.go", Line: 10, Content: `api_key := "sk_live_abcdefghijklmnopqrstuv"`},
		{File: "db.py", Line: 20, Content: `query = "SELECT * FROM users WHERE id=" + req.id`},
		{File: "run.js", Line: 3, Content: `eval(userInput)`},
		{File: "ok.go", Line: 1, Content: `db.Query("SELECT * FROM users WHERE id=?", id)`},
		{File: "comment.go", Line: 2, Content: `// password := "not_a_real_secret_here"`},
	}
	got := ScanDiffForSecuritySmells(lines)
	rules := map[string]bool{}
	for _, f := range got {
		rules[f.Rule] = true
		if f.Tool != "codehelper-builtin" {
			t.Errorf("unexpected tool %q", f.Tool)
		}
		if f.Evidence == "" {
			t.Errorf("missing evidence for %s", f.Rule)
		}
	}
	for _, want := range []string{"hardcoded-secret", "sql-string-concat", "eval-usage"} {
		if !rules[want] {
			t.Errorf("missing rule %s in %#v", want, rules)
		}
	}
	if rules["shell-exec-injection"] {
		t.Error("false positive shell-exec on safe query line")
	}
}

func TestScanDiffForSecuritySmells_BladeAndCSRF(t *testing.T) {
	lines := []AddedDiffLine{
		{File: "show.blade.php", Line: 4, Content: `{!! $user->bio !!}`},
		{File: "Kernel.php", Line: 12, Content: `$middleware->validateCsrfTokens(except: ['*']); // csrf_protection = false`},
	}
	got := ScanDiffForSecuritySmells(lines)
	rules := map[string]bool{}
	for _, f := range got {
		rules[f.Rule] = true
	}
	if !rules["blade-unescaped-output"] {
		t.Fatalf("expected blade-unescaped-output, got %#v", rules)
	}
}
