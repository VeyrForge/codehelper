package security

import "testing"

func TestIsSecurityScanFalseFriend_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		rel     string
		content string
		rule    string
		want    bool
	}{
		{
			name:    "stylesheet always demoted",
			rel:     "assets/css/app.css",
			content: `eval(userInput)`,
			rule:    "eval-usage",
			want:    true,
		},
		{
			name:    "css selector prefix demoted",
			rel:     "src/Button.tsx",
			content: `.dangerouslySetInnerHTML { color: red }`,
			rule:    "raw-html-xss",
			want:    true,
		},
		{
			name:    "authz-gap keeps csrf_exempt decorator",
			rel:     "app/views.py",
			content: `@csrf_exempt`,
			rule:    "authz-gap",
			want:    false,
		},
		{
			name:    "authz-gap demotes requireAuth in NextResponse.json",
			rel:     "app/api/probe/route.ts",
			content: `return NextResponse.json({ sql, requireAuth: false })`,
			rule:    "authz-gap",
			want:    true,
		},
		{
			name:    "authz-gap keeps route auth false config",
			rel:     "app/public.ts",
			content: `export const route = { auth: false }`,
			rule:    "authz-gap",
			want:    false,
		},
		{
			name:    "di inject prose demoted for any rule",
			rel:     "src/module.ts",
			content: `constructor(@Inject(TOKEN) private x: X) {}`,
			rule:    "injection-taint",
			want:    true,
		},
		{
			name:    "sql help example demoted",
			rel:     "internal/mcpsvc/ops.go",
			content: `e.g. sql="SELECT * FROM users"`,
			rule:    "sql-string-concat",
			want:    true,
		},
		{
			name:    "real eval call site not demoted by table alone",
			rel:     "app/api/run.js",
			content: `eval(req.body.code)`,
			rule:    "eval-usage",
			want:    false,
		},
		{
			name:    "browser page.eval demoted",
			rel:     "internal/web/browser_rod.go",
			content: `page.Eval("() => document.body")`,
			rule:    "eval-usage",
			want:    true,
		},
		{
			name:    "open-redirect same-path request demoted",
			rel:     "app/views.py",
			content: `return redirect(request.path)`,
			rule:    "open-redirect",
			want:    true,
		},
		{
			name:    "c-unsafe fgets alone demoted",
			rel:     "src/buf.c",
			content: `fgets(buf, sizeof(buf), stdin);`,
			rule:    "c-unsafe-buffer",
			want:    true,
		},
		{
			name:    "shellwords safe argv demoted",
			rel:     "lib/run.rb",
			content: `system(*Shellwords.split(cmd))`,
			rule:    "shell-exec-injection",
			want:    true,
		},
		{
			name:    "rule id stable in table",
			rel:     "app/x.go",
			content: `not a sink`,
			rule:    "sql-string-concat",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isSecurityScanFalseFriend(tc.rel, tc.content, tc.rule)
			if got != tc.want {
				t.Fatalf("isSecurityScanFalseFriend(%q,%q,%q)=%v want %v",
					tc.rel, tc.content, tc.rule, got, tc.want)
			}
		})
	}
}

func TestSecurityFalseFriendRules_UniqueIDsAndCoverage(t *testing.T) {
	if len(securityFalseFriendRules) < 20 {
		t.Fatalf("expected a substantial false-friend table, got %d rules", len(securityFalseFriendRules))
	}
	seen := map[string]struct{}{}
	for _, r := range securityFalseFriendRules {
		if r.id == "" {
			t.Fatal("false-friend rule missing id")
		}
		if r.match == nil {
			t.Fatalf("rule %q missing match func", r.id)
		}
		if _, ok := seen[r.id]; ok {
			t.Fatalf("duplicate false-friend rule id %q", r.id)
		}
		seen[r.id] = struct{}{}
	}
	required := []string{
		"library-internal-path",
		"stylesheet-path",
		"css-selector-or-decorator-prefix",
		"eval-false-friends",
		"hardcoded-secret-false-friends",
		"shell-exec-false-friends",
	}
	for _, id := range required {
		if _, ok := seen[id]; !ok {
			t.Fatalf("missing required false-friend rule id %q", id)
		}
	}
}

func TestFalseFriendApplies(t *testing.T) {
	if !falseFriendApplies(nil, "anything") {
		t.Fatal("empty appliesTo must match all rules")
	}
	if !falseFriendApplies([]string{"eval-usage", "authz-gap"}, "eval-usage") {
		t.Fatal("listed rule should apply")
	}
	if falseFriendApplies([]string{"eval-usage"}, "sql-string-concat") {
		t.Fatal("unlisted rule must not apply")
	}
}
