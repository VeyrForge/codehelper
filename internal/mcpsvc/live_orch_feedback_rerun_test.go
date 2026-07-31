//go:build liveorch

package mcpsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/orchestrator"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestLiveOrchFeedbackRerunLoop exercises orchestrate → orchestration_feedback →
// orchestration_rerun on multiple indexed projects and prints a score card.
//
//	go test ./internal/mcpsvc -tags "liveorch" -run TestLiveOrchFeedbackRerunLoop -v -count=1 -timeout 25m
func TestLiveOrchFeedbackRerunLoop(t *testing.T) {
	reg, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	projects := []struct {
		name  string
		task  string
		fb    string
		pref  string
		avoid string
	}{
		{"express", "how does routing and middleware registration work", "wrong scope — focus on Router and middleware stack, not docs samples", "Router", "docs,examples"},
		{"gin", "how does HTTP routing and middleware work", "wrong scope — prioritize Engine and RouterGroup, avoid README", "Engine,RouterGroup", "README"},
		{"laravel", "how does HTTP routing and middleware work", "wrong scope — focus on RouteServiceProvider / router, avoid vendor noise", "Router,Route", "vendor"},
		{"codehelper", "how does the browser MCP tool capture screenshots", "wrong scope — focus on CaptureBrowser and browser_rod, not MCP catalog prose", "CaptureBrowser,browser_rod", "helpcatalog"},
	}

	handlers := AllToolHandlers(reg)
	call := func(name string, args map[string]any) (string, bool, error) {
		h, ok := handlers[name]
		if !ok {
			return "", true, fmt.Errorf("missing tool %s", name)
		}
		req := mcp.CallToolRequest{}
		req.Params.Name = name
		req.Params.Arguments = args
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		res, err := h(ctx, req)
		if err != nil {
			return "", true, err
		}
		text := flattenEvalResult(res)
		return text, res != nil && res.IsError, nil
	}

	type row struct {
		Repo       string   `json:"repo"`
		RunID      string   `json:"run_id"`
		RerunID    string   `json:"rerun_id"`
		Workflow   string   `json:"workflow"`
		RerunWF    string   `json:"rerun_workflow"`
		FormatOK   bool     `json:"toon_ok"`
		FeedbackOK bool     `json:"feedback_ok"`
		RerunOK    bool     `json:"rerun_ok"`
		Score      int      `json:"score"`
		Notes      string   `json:"notes"`
		OrchHead   string   `json:"orch_head"`
		RerunHead  string   `json:"rerun_head"`
		Errors     []string `json:"errors,omitempty"`
	}
	var rows []row
	runIDRe := regexp.MustCompile(`(?m)^run_id:\s*(\S+)`)
	wfRe := regexp.MustCompile(`(?m)^workflow:\s*(\S+)`)

	for _, p := range projects {
		r := row{Repo: p.name}
		var errs []string
		entry, ok := findRepo(reg, p.name)
		if !ok {
			r.Notes = "repo not in registry"
			r.Score = 1
			rows = append(rows, r)
			continue
		}
		if _, err := os.Stat(filepath.Join(entry.RootPath, ".codehelper")); err != nil {
			r.Notes = "not indexed"
			r.Score = 1
			rows = append(rows, r)
			continue
		}
		_ = orchestrator.SetEnabled(entry.RootPath, true)

		orchText, isErr, err := call("orchestrate", map[string]any{
			"task": p.task, "repo": p.name, "format": "toon",
		})
		if err != nil || isErr {
			errs = append(errs, fmt.Sprintf("orchestrate: %v %s", err, trunc(orchText, 200)))
			r.Score = 2
			r.Errors = errs
			r.Notes = "orchestrate failed"
			rows = append(rows, r)
			continue
		}
		r.OrchHead = trunc(orchText, 240)
		r.FormatOK = looksLikeTOON(orchText) && !strings.HasPrefix(strings.TrimSpace(orchText), "{")
		if m := runIDRe.FindStringSubmatch(orchText); len(m) == 2 {
			r.RunID = m[1]
		}
		if m := wfRe.FindStringSubmatch(orchText); len(m) == 2 {
			r.Workflow = m[1]
		}
		if r.RunID == "" {
			// TOON may nest; also try JSON fallback parse of fields
			r.RunID = extractFieldLoose(orchText, "run_id")
			r.Workflow = extractFieldLoose(orchText, "workflow")
		}
		if r.RunID == "" {
			errs = append(errs, "no run_id in orchestrate TOON")
			r.Score = 3
			r.Errors = errs
			r.Notes = "missing run_id"
			rows = append(rows, r)
			continue
		}

		fbText, fbErrFlag, err := call("orchestration_feedback", map[string]any{
			"run_id": r.RunID, "repo": p.name,
			"message": p.fb, "correction_type": "wrong_scope",
			"preferred_entities": p.pref, "avoid_entities": p.avoid,
		})
		if err != nil || fbErrFlag {
			errs = append(errs, fmt.Sprintf("feedback: %v %s", err, trunc(fbText, 200)))
			r.Score = 4
			r.Errors = errs
			r.Notes = "feedback failed"
			rows = append(rows, r)
			continue
		}
		r.FeedbackOK = strings.Contains(fbText, `"ok": true`) || strings.Contains(fbText, `"ok":true`) || strings.Contains(fbText, "constraints")

		rerunText, rrErrFlag, err := call("orchestration_rerun", map[string]any{
			"run_id": r.RunID, "repo": p.name, "format": "toon",
			"instruction": p.fb, "preferred_entities": p.pref, "avoid_entities": p.avoid,
		})
		if err != nil || rrErrFlag {
			errs = append(errs, fmt.Sprintf("rerun: %v %s", err, trunc(rerunText, 200)))
			r.Score = 4
			r.Errors = errs
			r.Notes = "rerun failed"
			rows = append(rows, r)
			continue
		}
		r.RerunOK = true
		r.RerunHead = trunc(rerunText, 240)
		r.RerunID = extractFieldLoose(rerunText, "run_id")
		r.RerunWF = extractFieldLoose(rerunText, "workflow")
		if m := runIDRe.FindStringSubmatch(rerunText); len(m) == 2 && r.RerunID == "" {
			r.RerunID = m[1]
		}
		if m := wfRe.FindStringSubmatch(rerunText); len(m) == 2 && r.RerunWF == "" {
			r.RerunWF = m[1]
		}

		score := 5
		if r.FormatOK {
			score++
		}
		if r.FeedbackOK {
			score++
		}
		if r.RerunOK {
			score++
		}
		if r.RerunID != "" && r.RerunID != r.RunID {
			score++ // new run created
		}
		lower := strings.ToLower(rerunText + " " + orchText)
		for _, token := range strings.Split(p.pref, ",") {
			token = strings.ToLower(strings.TrimSpace(token))
			if token != "" && strings.Contains(lower, token) {
				score++
				break
			}
		}
		if strings.Contains(strings.ToLower(rerunText), "previous_wrong") || strings.Contains(strings.ToLower(rerunText), "changed_from") {
			score++
		}
		if score > 10 {
			score = 10
		}
		r.Score = score
		r.Notes = "ok"
		r.Errors = errs
		rows = append(rows, r)
		t.Logf("%s score=%d run=%s rerun=%s wf=%s->%s toon=%v", p.name, score, r.RunID, r.RerunID, r.Workflow, r.RerunWF, r.FormatOK)
	}

	outPath := filepath.Join("..", "..", ".testbeds", "reports", "live-orch-feedback-rerun.json")
	if wd, err := os.Getwd(); err == nil {
		// Prefer repo-root/.testbeds when the test binary is launched from the module root.
		cand := filepath.Join(wd, ".testbeds", "reports", "live-orch-feedback-rerun.json")
		if strings.Contains(filepath.ToSlash(wd), "codehelper") {
			outPath = cand
		}
		// When go test runs from internal/mcpsvc, walk up to module root.
		dir := wd
		for i := 0; i < 6; i++ {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				outPath = filepath.Join(dir, ".testbeds", "reports", "live-orch-feedback-rerun.json")
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	_ = os.MkdirAll(filepath.Dir(outPath), 0o755)
	b, _ := json.MarshalIndent(rows, "", "  ")
	_ = os.WriteFile(outPath, b, 0o644)
	t.Logf("wrote %s\n%s", outPath, string(b))

	pass := 0
	for _, r := range rows {
		if r.Score >= 6 && r.RerunOK {
			pass++
		}
	}
	if pass < 3 {
		t.Fatalf("expected >=3 useful orch feedback→rerun loops, got %d", pass)
	}
}

func findRepo(reg *registry.Registry, name string) (registry.Entry, bool) {
	for _, e := range reg.List() {
		if e.Name == name {
			return e, true
		}
	}
	return registry.Entry{}, false
}

func looksLikeTOON(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// TOON typically has key: value lines without wrapping braces for the root.
	return strings.Contains(s, "run_id:") || strings.Contains(s, "agent_brief:") || strings.Contains(s, "workflow:")
}

func extractFieldLoose(s, key string) string {
	re := regexp.MustCompile(`(?m)` + regexp.QuoteMeta(key) + `:\s*(\S+)`)
	if m := re.FindStringSubmatch(s); len(m) == 2 {
		return strings.Trim(m[1], `"'`)
	}
	re2 := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:\s*"([^"]+)"`)
	if m := re2.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	return ""
}

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
