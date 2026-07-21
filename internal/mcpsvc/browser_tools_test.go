package mcpsvc

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/web"
)

func TestUnsupportedBrowserActionHint_Upload(t *testing.T) {
	msg := unsupportedBrowserActionHint([]web.Action{{Do: "upload", Selector: "", Text: ""}})
	if msg == "" || !strings.Contains(msg, "requires selector=") {
		t.Fatalf("expected upload missing-args message, got %q", msg)
	}
	if unsupportedBrowserActionHint([]web.Action{{Do: "upload", Selector: "input[type=file]", Text: "/tmp/x.zip"}}) != "" {
		t.Fatal("upload with selector+path must be allowed (implemented via SetFiles)")
	}
	if unsupportedBrowserActionHint([]web.Action{{Do: "click", Selector: "#ok"}}) != "" {
		t.Fatal("click must not be treated as unsupported")
	}
}

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

func TestWPLoginRecipeRedactsPasswordInLabels(t *testing.T) {
	acts, err := web.ExpandRecipe(web.RecipeWPLogin, "admin", "super-secret-pass")
	if err != nil {
		t.Fatal(err)
	}
	var sawSensitive bool
	for _, a := range acts {
		if a.Sensitive {
			sawSensitive = true
			if a.Text != "super-secret-pass" {
				t.Fatalf("sensitive action lost password for runtime: %+v", a)
			}
		}
	}
	if !sawSensitive {
		t.Fatal("expected Sensitive password fill")
	}
	_ = strings.Builder{}
}

func TestFilterConsoleErrors(t *testing.T) {
	in := []web.ConsoleMessage{
		{Level: "log", Text: "ok"},
		{Level: "error", Text: "bad"},
		{Level: "warning", Text: "warn"},
		{Level: "assert", Text: "nope"},
	}
	got := filterConsoleErrors(in)
	if len(got) != 2 {
		t.Fatalf("want 2 console errors, got %d (%+v)", len(got), got)
	}
}

func TestRenderBrowserReportDiagnostics(t *testing.T) {
	r := &web.BrowserResult{
		Device: "desktop", Viewport: "1280x800@1x", FinalURL: "http://example.local/",
		DocStatus: 200, Format: "webp", Image: []byte("x"),
		Console:    []web.ConsoleMessage{{Level: "error", Text: "boom"}},
		PageErrors: []string{"uncaught"},
		Failed:     []web.FailedRequest{{URL: "http://example.local/x", Status: 404}},
	}
	out := renderBrowserReport(r, false)
	for _, want := range []string{"diagnostics:", "CONSOLE ERRORS", "FAILED REQUESTS", "404"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
}

func TestResolveHeadedAndGUI(t *testing.T) {
	t.Setenv("CODEHELPER_BROWSER_HEADED", "")
	if resolveHeaded(map[string]any{}, nil) {
		t.Fatal("default headless")
	}
	if !resolveHeaded(map[string]any{"headed": true}, nil) {
		t.Fatal("headed=true")
	}
	if !resolveHeaded(map[string]any{"gui": true}, nil) {
		t.Fatal("gui=true")
	}
	if resolveHeaded(map[string]any{"headed": false}, nil) {
		t.Fatal("explicit headed=false")
	}
	t.Setenv("CODEHELPER_BROWSER_HEADED", "1")
	if !resolveHeaded(map[string]any{}, nil) {
		t.Fatal("env headed")
	}
	on := true
	t.Setenv("CODEHELPER_BROWSER_HEADED", "")
	if !resolveHeaded(map[string]any{}, &on) {
		t.Fatal("project default")
	}
	t.Setenv("CODEHELPER_BROWSER_HEADED", "0")
	if resolveHeaded(map[string]any{}, &on) {
		t.Fatal("env off should win over project")
	}
}

func TestResolvePauseOnFail(t *testing.T) {
	t.Setenv("CODEHELPER_BROWSER_PAUSE_ON_FAIL", "")
	if resolvePauseOnFail(map[string]any{}) {
		t.Fatal("default off")
	}
	if !resolvePauseOnFail(map[string]any{"pause_on_fail": true}) {
		t.Fatal("arg on")
	}
	t.Setenv("CODEHELPER_BROWSER_PAUSE_ON_FAIL", "1")
	if !resolvePauseOnFail(map[string]any{}) {
		t.Fatal("env on")
	}
}

func TestParseActionsRef(t *testing.T) {
	acts := parseActions([]any{
		map[string]any{"action": "click", "ref": "e3"},
		map[string]any{"action": "click", "selector": "ref:e4"},
	})
	if len(acts) != 2 || acts[0].Ref != "e3" || acts[1].Selector != "ref:e4" {
		t.Fatalf("got %+v", acts)
	}
}
