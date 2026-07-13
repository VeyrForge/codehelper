package usage

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/VeyrForge/codehelper/internal/projcfg"
)

// verifyTools are the change-verification tools whose most recent outcome is
// surfaced as "last verify" — the "did the change work" signal.
var verifyTools = map[string]bool{
	"verify": true, "diagnostics": true, "finish_check": true, "test_impact": true,
}

// Report is the full per-project usage view: codehelper-side tool output
// (all clients) plus Claude real-token usage (when available).
type Report struct {
	RepoRoot    string          `json:"repo_root"`
	GeneratedAt time.Time       `json:"generated_at"`
	Sessions    []SessionUsage  `json:"sessions"`
	Tools       []ToolUsage     `json:"tools"`
	Totals      Totals          `json:"totals"`
	Claude      []ClaudeSession `json:"claude_sessions"`
	ClaudeTotal ClaudeTotals    `json:"claude_total"`
	ClaudeFound bool            `json:"claude_found"`
	Codex       []CodexSession  `json:"codex_sessions,omitempty"`
	CodexTotal  CodexTotals     `json:"codex_total"`
	CodexFound  bool            `json:"codex_found"`
	LastVerify  *VerifyStatus   `json:"last_verify,omitempty"`
	Refs        []Event         `json:"refs,omitempty"`
	Verbose     bool            `json:"-"` // expand the recent-call trail to show input + output preview
	// ToolsEnabled / Track reflect the project's MCP runtime config, so the report
	// states up front whether this is a tools-on or baseline (tracking-only) run.
	ToolsEnabled bool   `json:"tools_enabled"`
	Track        string `json:"track"`
}

// SessionUsage aggregates codehelper tool output for one MCP session.
type SessionUsage struct {
	Session    string    `json:"session"`
	Client     string    `json:"client"`
	Calls      int       `json:"calls"`
	RespTokens int       `json:"resp_tokens"`
	Errors     int       `json:"errors"`
	FirstTS    time.Time `json:"first_ts"`
	LastTS     time.Time `json:"last_ts"`
}

// ToolUsage aggregates one tool across all sessions.
type ToolUsage struct {
	Tool       string `json:"tool"`
	Calls      int    `json:"calls"`
	RespTokens int    `json:"resp_tokens"`
	AvgTokens  int    `json:"avg_tokens"`
	AvgLatency int64  `json:"avg_latency_ms"`
	Errors     int    `json:"errors"`
}

// Totals are codehelper-side grand totals.
type Totals struct {
	Calls      int `json:"calls"`
	RespTokens int `json:"resp_tokens"`
	RespBytes  int `json:"resp_bytes"`
	Errors     int `json:"errors"`
	Sessions   int `json:"sessions"`
	// Disabled counts calls shadow-recorded while tools were off (the agent did
	// not receive their result) — i.e. the baseline-mode portion of the log.
	Disabled int `json:"disabled"`
}

// ClaudeTotals sum real Claude tokens across sessions.
type ClaudeTotals struct {
	Sessions    int `json:"sessions"`
	Messages    int `json:"messages"`
	Input       int `json:"input_tokens"`
	Output      int `json:"output_tokens"`
	CacheRead   int `json:"cache_read_tokens"`
	CacheCreate int `json:"cache_create_tokens"`
}

// Total is the single comparable grand total of Claude tokens.
func (c ClaudeTotals) Total() int {
	return c.Input + c.Output + c.CacheRead + c.CacheCreate
}

// CodexTotals sum real Codex tokens across sessions (cumulative per session).
type CodexTotals struct {
	Sessions  int `json:"sessions"`
	Input     int `json:"input_tokens"`
	Output    int `json:"output_tokens"`
	Reasoning int `json:"reasoning_tokens"`
	Cached    int `json:"cached_input_tokens"`
	Total     int `json:"total_tokens"`
}

// VerifyStatus is the most recent change-verification tool outcome.
type VerifyStatus struct {
	Tool    string    `json:"tool"`
	TS      time.Time `json:"ts"`
	IsError bool      `json:"is_error"`
}

// BuildReport loads a project's events + Claude transcripts and aggregates them.
// refs caps the recent-call trail (0 disables it).
func BuildReport(repoRoot string, refs int) (Report, error) {
	events, err := Load(repoRoot)
	if err != nil {
		return Report{}, err
	}
	rep := Report{RepoRoot: repoRoot}
	if cfg, err := projcfg.Load(repoRoot); err == nil {
		rep.ToolsEnabled = cfg.ToolsEnabled
		rep.Track = cfg.Track
	}

	sessIdx := map[string]*SessionUsage{}
	toolIdx := map[string]*ToolUsage{}
	toolLatencySum := map[string]int64{}

	for _, ev := range events {
		su := sessIdx[ev.Session]
		if su == nil {
			su = &SessionUsage{Session: ev.Session, Client: ev.Client}
			sessIdx[ev.Session] = su
		}
		if ev.Client != "" && ev.Client != "unknown" {
			su.Client = ev.Client
		}
		su.Calls++
		su.RespTokens += ev.RespTokens
		if ev.IsError {
			su.Errors++
		}
		if su.FirstTS.IsZero() || ev.TS.Before(su.FirstTS) {
			su.FirstTS = ev.TS
		}
		if ev.TS.After(su.LastTS) {
			su.LastTS = ev.TS
		}

		tu := toolIdx[ev.Tool]
		if tu == nil {
			tu = &ToolUsage{Tool: ev.Tool}
			toolIdx[ev.Tool] = tu
		}
		tu.Calls++
		tu.RespTokens += ev.RespTokens
		toolLatencySum[ev.Tool] += ev.LatencyMS
		if ev.IsError {
			tu.Errors++
		}

		rep.Totals.Calls++
		rep.Totals.RespTokens += ev.RespTokens
		rep.Totals.RespBytes += ev.RespBytes
		if ev.IsError {
			rep.Totals.Errors++
		}
		if ev.Disabled {
			rep.Totals.Disabled++
		}

		if verifyTools[ev.Tool] {
			if rep.LastVerify == nil || ev.TS.After(rep.LastVerify.TS) {
				rep.LastVerify = &VerifyStatus{Tool: ev.Tool, TS: ev.TS, IsError: ev.IsError}
			}
		}
	}

	for _, su := range sessIdx {
		rep.Sessions = append(rep.Sessions, *su)
	}
	sort.Slice(rep.Sessions, func(i, j int) bool { return rep.Sessions[i].LastTS.After(rep.Sessions[j].LastTS) })
	rep.Totals.Sessions = len(rep.Sessions)

	for _, tu := range toolIdx {
		if tu.Calls > 0 {
			tu.AvgTokens = tu.RespTokens / tu.Calls
			tu.AvgLatency = toolLatencySum[tu.Tool] / int64(tu.Calls)
		}
		rep.Tools = append(rep.Tools, *tu)
	}
	sort.Slice(rep.Tools, func(i, j int) bool { return rep.Tools[i].RespTokens > rep.Tools[j].RespTokens })

	if refs > 0 && len(events) > 0 {
		start := len(events) - refs
		if start < 0 {
			start = 0
		}
		trail := append([]Event(nil), events[start:]...)
		// most recent first
		for i, j := 0, len(trail)-1; i < j; i, j = i+1, j-1 {
			trail[i], trail[j] = trail[j], trail[i]
		}
		rep.Refs = trail
	}

	if cs, found := LoadClaudeSessions(repoRoot); found {
		rep.ClaudeFound = true
		rep.Claude = cs
		for _, s := range cs {
			rep.ClaudeTotal.Sessions++
			rep.ClaudeTotal.Messages += s.Messages
			rep.ClaudeTotal.Input += s.Input
			rep.ClaudeTotal.Output += s.Output
			rep.ClaudeTotal.CacheRead += s.CacheRead
			rep.ClaudeTotal.CacheCreate += s.CacheCreate
		}
	}

	if cx, found := LoadCodexSessions(repoRoot); found {
		rep.CodexFound = true
		rep.Codex = cx
		for _, s := range cx {
			rep.CodexTotal.Sessions++
			rep.CodexTotal.Input += s.Input
			rep.CodexTotal.Output += s.Output
			rep.CodexTotal.Reasoning += s.Reasoning
			rep.CodexTotal.Cached += s.CachedInput
			rep.CodexTotal.Total += s.TotalTokens
		}
	}

	return rep, nil
}

// RenderJSON returns the report as indented JSON.
func (r Report) RenderJSON() string {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Render returns a clean, human-readable text report.
func (r Report) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "codehelper usage — %s\n", r.RepoRoot)
	if !r.GeneratedAt.IsZero() {
		fmt.Fprintf(&b, "generated %s\n", r.GeneratedAt.Format("2006-01-02 15:04"))
	}
	if r.ToolsEnabled {
		fmt.Fprintf(&b, "mode: tools ENABLED · telemetry %q\n", r.Track)
	} else {
		fmt.Fprintf(&b, "mode: BASELINE (tools disabled — results recorded, not given to the agent) · telemetry %q\n", r.Track)
	}
	if r.Totals.Disabled > 0 {
		fmt.Fprintf(&b, "  %d of %d calls were shadow-recorded in baseline mode\n", r.Totals.Disabled, r.Totals.Calls)
	}

	if r.Totals.Calls == 0 {
		b.WriteString("\nNo codehelper tool calls recorded yet for this project.\n")
	} else {
		fmt.Fprintf(&b, "\nCODEHELPER OUTPUT (context injected by tools — all clients)\n")
		fmt.Fprintf(&b, "  %d calls across %d sessions · ~%s tokens · %d errors\n",
			r.Totals.Calls, r.Totals.Sessions, comma(r.Totals.RespTokens), r.Totals.Errors)

		b.WriteString("\n  By tool (heaviest first):\n")
		tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  TOOL\tCALLS\t~TOKENS\tAVG\tp~ms\tERR")
		for _, t := range r.Tools {
			fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\t%d\t%d\n",
				t.Tool, t.Calls, comma(t.RespTokens), comma(t.AvgTokens), t.AvgLatency, t.Errors)
		}
		tw.Flush()

		b.WriteString("\n  By session (most recent first):\n")
		tw = tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  SESSION\tCLIENT\tCALLS\t~TOKENS\tERR\tLAST")
		for _, s := range r.Sessions {
			fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\t%d\t%s\n",
				short(s.Session), s.Client, s.Calls, comma(s.RespTokens), s.Errors, ago(s.LastTS))
		}
		tw.Flush()
	}

	b.WriteString("\nCLAUDE MODEL TOKENS (real, billed — from Claude Code transcripts)\n")
	if !r.ClaudeFound {
		b.WriteString("  No Claude transcripts for this project. (Cursor does not expose\n")
		b.WriteString("  per-session token counts locally — only the codehelper output above is\n")
		b.WriteString("  measurable for that client. Codex tokens, if any, are shown below.)\n")
	} else if len(r.Claude) == 0 {
		b.WriteString("  Transcript directory present but no token-bearing messages found.\n")
	} else {
		ct := r.ClaudeTotal
		fmt.Fprintf(&b, "  %d sessions · %d messages · ~%s total tokens\n",
			ct.Sessions, ct.Messages, comma(ct.Total()))
		fmt.Fprintf(&b, "  in %s · out %s · cache-read %s · cache-write %s\n",
			comma(ct.Input), comma(ct.Output), comma(ct.CacheRead), comma(ct.CacheCreate))

		b.WriteString("\n  By session (most recent first):\n")
		tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  SESSION\tMODEL\tMSGS\tIN\tOUT\tCACHE\tTOTAL\tLAST")
		for _, s := range r.Claude {
			fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
				short(s.Session), s.Model, s.Messages, comma(s.Input), comma(s.Output),
				comma(s.CacheRead+s.CacheCreate), comma(s.Total()), ago(s.LastTS))
		}
		tw.Flush()
	}

	if r.CodexFound && len(r.Codex) > 0 {
		ct := r.CodexTotal
		b.WriteString("\nCODEX MODEL TOKENS (real — from ~/.codex/sessions rollouts)\n")
		fmt.Fprintf(&b, "  %d sessions · ~%s total tokens (in %s · out %s · reasoning %s · cached %s)\n",
			ct.Sessions, comma(ct.Total), comma(ct.Input), comma(ct.Output), comma(ct.Reasoning), comma(ct.Cached))
		tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  SESSION\tSOURCE\tIN\tOUT\tREASON\tTOTAL\tLAST")
		for _, s := range r.Codex {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				short(s.Session), s.Source, comma(s.Input), comma(s.Output),
				comma(s.Reasoning), comma(s.TotalTokens), ago(s.LastTS))
		}
		tw.Flush()
	}

	if r.LastVerify != nil {
		status := "PASS"
		if r.LastVerify.IsError {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "\nLAST VERIFY: %s via %s (%s)\n", status, r.LastVerify.Tool, ago(r.LastVerify.TS))
	}

	if len(r.Refs) > 0 {
		b.WriteString("\nRECENT TOOL CALLS (what codehelper did, most recent first)\n")
		if r.Verbose {
			renderVerboseTrail(&b, r.Refs)
		} else {
			tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "  WHEN\tCLIENT\tTOOL\t~TOKENS\tms\tERR")
			for _, e := range r.Refs {
				errMark := ""
				if e.IsError {
					errMark = "ERR"
				}
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%d\t%s\n",
					ago(e.TS), e.Client, e.Tool, comma(e.RespTokens), e.LatencyMS, errMark)
			}
			tw.Flush()
			b.WriteString("  (use --verbose / verbose:true to see each call's input + output)\n")
		}
	}

	return b.String()
}

// renderVerboseTrail prints each recent call as a block with its input and a
// preview of codehelper's output — the view for judging whether a tool actually
// did a good job (empty/thin result, wrong hit, slow, or token-heavy).
func renderVerboseTrail(b *strings.Builder, refs []Event) {
	for _, e := range refs {
		status := "ok"
		if e.IsError {
			status = "ERR"
		}
		fmt.Fprintf(b, "\n  %s  %s  %s  [%s]  ~%s tok · %dms\n",
			ago(e.TS), e.Client, e.Tool, status, comma(e.RespTokens), e.LatencyMS)
		if e.Args != "" {
			fmt.Fprintf(b, "    in:  %s\n", e.Args)
		}
		out := e.Snippet
		if out == "" {
			out = "(no text output)"
		}
		fmt.Fprintf(b, "    out: %s\n", out)
	}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("01-02 15:04")
}

func comma(n int) string {
	if n < 0 {
		return "-" + comma(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
