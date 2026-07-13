package research

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/taskstore"
)

// Default allowlisted doc host suffixes for controlled fetch.
var defaultAllowHosts = []string{
	"docs.github.com",
	"developer.mozilla.org",
	"nodejs.org",
	"go.dev",
	"pkg.go.dev",
	"laravel.com",
	"wordpress.org",
	"woocommerce.com",
	"php.net",
	"python.org",
	"react.dev",
	"svelte.dev",
}

// Input for a research run.
type Input struct {
	Query        string
	Sources      []string
	AllowHosts   []string
	TimeoutSec   int
	NetworkOK    bool
	UserApproved bool
}

// Output is structured research for plans and MCP responses.
type Output struct {
	Query          string   `json:"query"`
	Needed         string   `json:"needed"`
	SourcesChecked []string `json:"sources_checked"`
	Snippets       []string `json:"snippets,omitempty"`
	Recommendation string   `json:"recommendation"`
	Avoid          []string `json:"avoid,omitempty"`
	ProjectImpact  string   `json:"project_impact"`
	BlockedReason  string   `json:"blocked_reason,omitempty"`
}

// Policy reads research enablement from learning.json.
type Policy struct {
	Enabled bool `json:"enabled"`
}

// LearningEnabled reports whether project learning/memory capture is enabled.
func LearningEnabled(repoRoot string) bool {
	path := filepath.Join(paths.RepoIndexDir(repoRoot), "learning.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return false
	}
	state, _ := root["state"].(string)
	return strings.ToLower(strings.TrimSpace(state)) == "enabled"
}

// NetworkEnabled reports whether research network is allowed for repoRoot.
func NetworkEnabled(repoRoot string) bool {
	path := filepath.Join(paths.RepoIndexDir(repoRoot), "learning.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		return false
	}
	raw, ok := root["research"]
	if !ok {
		return false
	}
	var p Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	return p.Enabled
}

// Run performs approval-gated HTTP fetch against allowlisted official doc URLs.
func Run(ctx context.Context, in Input) (*Output, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if !in.NetworkOK {
		return &Output{
			Query:          query,
			Needed:         "Research requires project opt-in (.codehelper/learning.json research.enabled).",
			Recommendation: "Search the repo first with query/context; enable research when official docs are required.",
			ProjectImpact:  "Research skipped (disabled).",
			BlockedReason:  "research disabled",
		}, nil
	}
	if !in.UserApproved {
		return &Output{
			Query:          query,
			Needed:         "Network research requires explicit user approval.",
			Recommendation: "Approve research in the UI or pass approved=true.",
			ProjectImpact:  "Research blocked pending approval.",
			BlockedReason:  "approval required",
		}, nil
	}
	hosts := in.AllowHosts
	if len(hosts) == 0 {
		hosts = defaultAllowHosts
	}
	urls := in.Sources
	if len(urls) == 0 {
		urls = suggestURLs(query)
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	var checked []string
	var snippets []string
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" || !hostAllowed(raw, hosts) {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "codehelper-research/1.0")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 {
			continue
		}
		checked = append(checked, raw)
		text := strings.Join(strings.Fields(string(body)), " ")
		if len(text) > 400 {
			text = text[:400] + "…"
		}
		if text != "" {
			snippets = append(snippets, raw+": "+text)
		}
	}
	out := &Output{
		Query:          query,
		Needed:         "Official docs checked for current API guidance.",
		SourcesChecked: checked,
		Snippets:       snippets,
		Recommendation: "Apply only patterns validated against this repo's existing code and project rules.",
		Avoid:          []string{"deprecated APIs", "unverified community snippets"},
		ProjectImpact:  "Use research snippets as hints; rewrite to match project conventions.",
	}
	if len(checked) == 0 {
		out.BlockedReason = "no allowlisted sources fetched"
		out.Recommendation = "Add explicit source URLs on allowlisted hosts or rely on repo search."
	}
	return out, nil
}

func hostAllowed(raw string, hosts []string) bool {
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "https://") {
		return false
	}
	for _, h := range hosts {
		if strings.Contains(lower, strings.ToLower(h)) {
			return true
		}
	}
	return false
}

func suggestURLs(query string) []string {
	lq := strings.ToLower(query)
	var out []string
	if strings.Contains(lq, "laravel") {
		out = append(out, "https://laravel.com/docs")
	}
	if strings.Contains(lq, "wordpress") || strings.Contains(lq, "woo") {
		out = append(out, "https://developer.wordpress.org/")
	}
	if strings.Contains(lq, "go ") || strings.Contains(lq, "golang") {
		out = append(out, "https://go.dev/doc/")
	}
	if strings.Contains(lq, "node") || strings.Contains(lq, "npm") {
		out = append(out, "https://nodejs.org/en/docs")
	}
	return out
}

// ToPlanSummary converts output to taskstore plan research summary.
func ToPlanSummary(o *Output) *taskstore.ResearchSummary {
	if o == nil {
		return nil
	}
	return &taskstore.ResearchSummary{
		Needed:         o.Needed,
		Sources:        o.SourcesChecked,
		Recommendation: o.Recommendation,
		Avoid:          o.Avoid,
		ProjectImpact:  o.ProjectImpact,
	}
}
