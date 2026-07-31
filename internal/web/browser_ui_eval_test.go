//go:build rod

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// uiEvalTask mirrors methodology-lite browser agent evals (QASkills / mcpbr):
// deterministic tool success predicates without a cloud LLM in the loop.
type uiEvalTask struct {
	ID     string `json:"id"`
	Layer  string `json:"layer"` // tool | task | safety_note
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
	Ms     int64  `json:"ms"`
}

func actionPassed(log []string) bool {
	for _, l := range log {
		if strings.Contains(l, "FAILED") {
			return false
		}
	}
	return len(log) > 0
}

func serveUIFixture(t *testing.T, name string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "ui_fixture", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	}))
}

// TestUIEvalHarness_AgentCanVerifyUI is the Layer-1/2 browser smoke:
// outline → assert fail on broken page → "fix" → retest pass → CMS-like assert.
// Optional live WordPress is covered by scripts/browser-ui-eval.sh (soft skip).
func TestUIEvalHarness_AgentCanVerifyUI(t *testing.T) {
	requireBrowser(t)
	var tasks []uiEvalTask
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// --- Task: outline discovers selectors (perception / tool layer) ---
	{
		srv := serveUIFixture(t, "broken.html")
		defer srv.Close()
		start := time.Now()
		res, err := CaptureBrowser(ctx, BrowserOptions{URL: srv.URL, Outline: true, WaitMS: 100})
		elapsed := time.Since(start).Milliseconds()
		ok := err == nil
		detail := ""
		if err != nil {
			detail = err.Error()
		} else {
			foundEmail, foundSubmit := false, false
			for _, el := range res.Outline {
				if el.Selector == "#email" || strings.Contains(el.Selector, "email") {
					foundEmail = true
				}
				if el.Selector == "#submit" || strings.Contains(strings.ToLower(el.Name), "place order") {
					foundSubmit = true
				}
			}
			ok = foundEmail && foundSubmit
			if !ok {
				detail = "outline missing #email or #submit"
			}
		}
		tasks = append(tasks, uiEvalTask{ID: "outline_selectors", Layer: "tool", Pass: ok, Detail: detail, Ms: elapsed})
		if !ok {
			t.Errorf("outline_selectors: %s", detail)
		}
	}

	// --- Task: broken page assert fails (success predicate detects defect) ---
	{
		srv := serveUIFixture(t, "broken.html")
		defer srv.Close()
		start := time.Now()
		res, err := CaptureBrowser(ctx, BrowserOptions{
			URL: srv.URL,
			Actions: []Action{
				{Do: "fill", Selector: "#email", Text: "qa@example.com"},
				{Do: "click", Selector: "#submit"},
				{Do: "assert_text", Selector: "#status", Text: "Order confirmed for qa@example.com"},
			},
		})
		elapsed := time.Since(start).Milliseconds()
		// Expect assert FAILURE (agent would then debug).
		ok := err == nil && !actionPassed(res.ActionLog)
		detail := ""
		if err != nil {
			detail = err.Error()
			ok = false
		} else if actionPassed(res.ActionLog) {
			detail = "assert unexpectedly passed on broken fixture"
		}
		tasks = append(tasks, uiEvalTask{ID: "broken_assert_fails", Layer: "task", Pass: ok, Detail: detail, Ms: elapsed})
		if !ok {
			t.Errorf("broken_assert_fails: %s (log=%v)", detail, res.ActionLog)
		}
	}

	// --- Task: implement→retest — fixed page assert passes ---
	{
		srv := serveUIFixture(t, "fixed.html")
		defer srv.Close()
		start := time.Now()
		res, err := CaptureBrowser(ctx, BrowserOptions{
			URL: srv.URL,
			Actions: []Action{
				{Do: "fill", Selector: "#email", Text: "qa@example.com"},
				{Do: "click", Selector: "#submit"},
				{Do: "wait", Selector: "#status", MS: 5000},
				{Do: "assert_text", Selector: "#status", Text: "Order confirmed for qa@example.com"},
			},
		})
		elapsed := time.Since(start).Milliseconds()
		ok := err == nil && actionPassed(res.ActionLog)
		detail := ""
		if err != nil {
			detail = err.Error()
		} else if !ok {
			detail = "assert failed after fix: " + strings.Join(res.ActionLog, " | ")
		}
		tasks = append(tasks, uiEvalTask{ID: "fixed_assert_passes", Layer: "task", Pass: ok, Detail: detail, Ms: elapsed})
		if !ok {
			t.Errorf("fixed_assert_passes: %s", detail)
		}
	}

	// --- Task: CMS-like list assert (WordPress-shaped selectors without live WP) ---
	{
		srv := serveUIFixture(t, "cms_list.html")
		defer srv.Close()
		start := time.Now()
		res, err := CaptureBrowser(ctx, BrowserOptions{
			URL: srv.URL,
			Actions: []Action{
				{Do: "wait", Selector: "#the-list", MS: 5000},
				{Do: "assert_text", Selector: "h1", Text: "Plugins"},
				{Do: "assert_text", Selector: "#the-list", Text: "Hello Dolly"},
			},
		})
		elapsed := time.Since(start).Milliseconds()
		ok := err == nil && actionPassed(res.ActionLog)
		detail := ""
		if err != nil {
			detail = err.Error()
		} else if !ok {
			detail = strings.Join(res.ActionLog, " | ")
		}
		tasks = append(tasks, uiEvalTask{ID: "cms_list_assert", Layer: "task", Pass: ok, Detail: detail, Ms: elapsed})
		if !ok {
			t.Errorf("cms_list_assert: %s", detail)
		}
	}

	// --- Task: CMS-like form (fill/select/assert) + wait_hydrate landmark ---
	{
		srv := serveUIFixture(t, "cms_form.html")
		defer srv.Close()
		start := time.Now()
		res, err := CaptureBrowser(ctx, BrowserOptions{
			URL:         srv.URL,
			WaitHydrate: true,
			Actions: []Action{
				{Do: "fill", TestID: "plugin-name", Text: "Hello Dolly"},
				{Do: "select", TestID: "channel", Text: "Beta"},
				{Do: "click", TestID: "install"},
				{Do: "assert_text", Selector: "#status", Text: "Ready to install Hello Dolly (beta) with 0 file(s)"},
			},
		})
		elapsed := time.Since(start).Milliseconds()
		ok := err == nil && actionPassed(res.ActionLog)
		detail := ""
		if err != nil {
			detail = err.Error()
		} else if !ok {
			detail = strings.Join(res.ActionLog, " | ")
		}
		tasks = append(tasks, uiEvalTask{ID: "cms_form_assert", Layer: "task", Pass: ok, Detail: detail, Ms: elapsed})
		if !ok {
			t.Errorf("cms_form_assert: %s", detail)
		}
	}

	// --- Task: failure debug pack completeness (outline/snapshot/URL/log) ---
	{
		srv := serveUIFixture(t, "broken.html")
		defer srv.Close()
		packDir := t.TempDir()
		start := time.Now()
		res, err := CaptureBrowser(ctx, BrowserOptions{
			URL: srv.URL,
			Actions: []Action{
				{Do: "fill", Selector: "#email", Text: "qa@example.com"},
				{Do: "click", Selector: "#submit"},
				{Do: "assert_text", Selector: "#status", Text: "Order confirmed for qa@example.com"},
			},
			WriteDebugPack: true,
			DebugPackDir:   packDir,
		})
		elapsed := time.Since(start).Milliseconds()
		ok := err == nil && res.FailurePack != nil && res.FailurePack.Failed
		detail := ""
		if err != nil {
			detail = err.Error()
			ok = false
		} else if res.FailurePack == nil || !res.FailurePack.Failed {
			detail = "missing FailurePack"
			ok = false
		} else if res.DebugPackJSON == "" {
			detail = "debug pack not written"
			ok = false
		} else if len(res.FailurePack.Outline) == 0 && res.FailurePack.Snapshot == "" {
			detail = "pack missing outline and snapshot"
			ok = false
		} else if res.FailurePack.FinalURL == "" || len(res.FailurePack.ActionLog) == 0 {
			detail = "pack missing url or action_log"
			ok = false
		}
		if ok {
			if _, serr := os.Stat(res.DebugPackJSON); serr != nil {
				ok = false
				detail = "report.json missing: " + serr.Error()
			}
		}
		tasks = append(tasks, uiEvalTask{ID: "failure_debug_pack", Layer: "tool", Pass: ok, Detail: detail, Ms: elapsed})
		if !ok {
			t.Errorf("failure_debug_pack: %s", detail)
		}
	}

	// --- Task: upload sandbox + multi-file attach on CMS form ---
	{
		srv := serveUIFixture(t, "cms_form.html")
		defer srv.Close()
		ws := t.TempDir()
		f1 := filepath.Join(ws, "a.zip")
		f2 := filepath.Join(ws, "b.zip")
		_ = os.WriteFile(f1, []byte("PK\x03\x04"), 0o644)
		_ = os.WriteFile(f2, []byte("PK\x03\x04"), 0o644)
		start := time.Now()
		res, err := CaptureBrowser(ctx, BrowserOptions{
			URL:           srv.URL,
			WorkspaceRoot: ws,
			Actions: []Action{
				{Do: "fill", TestID: "plugin-name", Text: "Zip Pack"},
				{Do: "upload", Selector: "#pluginzip", Text: f1 + "||" + f2},
				{Do: "click", TestID: "install"},
				{Do: "assert_text", Selector: "#status", Text: "with 2 file(s)"},
			},
		})
		elapsed := time.Since(start).Milliseconds()
		ok := err == nil && actionPassed(res.ActionLog)
		detail := ""
		if err != nil {
			detail = err.Error()
		} else if !ok {
			detail = strings.Join(res.ActionLog, " | ")
		}
		tasks = append(tasks, uiEvalTask{ID: "cms_upload_multifile", Layer: "task", Pass: ok, Detail: detail, Ms: elapsed})
		if !ok {
			t.Errorf("cms_upload_multifile: %s", detail)
		}
	}

	// --- Safety note recorded for sibling product APIs (not a hard fail) ---
	tasks = append(tasks, uiEvalTask{
		ID:     "todo_sibling_apis",
		Layer:  "safety_note",
		Pass:   true,
		Detail: "Covered: outline/actions/assert/session/recipe/upload sandbox/multi-file/failure debug pack/wait_hydrate. Remaining nightly: pinned-model success-rate runner; optional browser close/reset MCP.",
		Ms:     0,
	})

	passed, failed := 0, 0
	for _, tk := range tasks {
		if tk.Pass {
			passed++
		} else {
			failed++
		}
		t.Logf("ui_eval %s layer=%s pass=%v ms=%d %s", tk.ID, tk.Layer, tk.Pass, tk.Ms, tk.Detail)
	}

	summary := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"methodology":  "browser-ui-eval.md — deterministic tool + task predicates (QASkills three-layer lite)",
		"tasks":        tasks,
		"passed":       passed,
		"failed":       failed,
		"verdict":      map[bool]string{true: "PASS", false: "FAIL"}[failed == 0],
	}
	if p := os.Getenv("CODEHELPER_BROWSER_UI_REPORT"); p != "" {
		b, _ := json.MarshalIndent(summary, "", "  ")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatalf("write browser ui report: %v", err)
		}
		t.Logf("wrote %s", p)
	}
	if failed > 0 {
		t.Fatalf("browser UI eval: %d task(s) failed", failed)
	}
}
