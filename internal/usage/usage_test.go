package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("empty = %d, want 0", got)
	}
	// 8 chars -> 2 tokens at ~4 chars/token.
	if got := EstimateTokens("abcdefgh"); got != 2 {
		t.Fatalf("8 chars = %d, want 2", got)
	}
	// rounds up.
	if got := EstimateTokens("abcde"); got != 2 {
		t.Fatalf("5 chars = %d, want 2", got)
	}
}

func TestPreview(t *testing.T) {
	if got := Preview("  hello\n\tworld  ", 100); got != "hello world" {
		t.Fatalf("whitespace collapse = %q", got)
	}
	if got := Preview("abcdefghij", 5); got != "abcde…" {
		t.Fatalf("truncate = %q, want abcde…", got)
	}
	if got := Preview("abc", 0); got != "abc" {
		t.Fatalf("max<=0 = %q", got)
	}
	if got := Preview("", 10); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestAppendLoadRoundtrip(t *testing.T) {
	repo := t.TempDir()
	evs := []Event{
		{TS: time.Unix(100, 0).UTC(), Session: "s1", Client: "claude-code", Tool: "query", RespBytes: 40, RespTokens: 10, LatencyMS: 12},
		{TS: time.Unix(200, 0).UTC(), Session: "s1", Client: "claude-code", Tool: "scout", RespBytes: 80, RespTokens: 20, LatencyMS: 30, IsError: true},
	}
	for _, e := range evs {
		if err := Append(repo, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, err := Load(repo)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d events, want 2", len(got))
	}
	if got[0].Tool != "query" || got[1].Tool != "scout" {
		t.Fatalf("order/content wrong: %+v", got)
	}
	if !got[1].IsError {
		t.Fatalf("error flag lost")
	}
}

func TestAppendEmptyRepoRootNoop(t *testing.T) {
	if err := Append("", Event{Tool: "query"}); err != nil {
		t.Fatalf("append empty root should be noop, got %v", err)
	}
}

func TestBuildReportAggregation(t *testing.T) {
	repo := t.TempDir()
	mustAppend := func(e Event) {
		if err := Append(repo, e); err != nil {
			t.Fatal(err)
		}
	}
	mustAppend(Event{TS: time.Unix(100, 0).UTC(), Session: "s1", Client: "claude-code", Tool: "query", RespTokens: 100, RespBytes: 400, LatencyMS: 10})
	mustAppend(Event{TS: time.Unix(110, 0).UTC(), Session: "s1", Client: "claude-code", Tool: "query", RespTokens: 50, RespBytes: 200, LatencyMS: 20})
	mustAppend(Event{TS: time.Unix(120, 0).UTC(), Session: "s2", Client: "cursor", Tool: "scout", RespTokens: 300, RespBytes: 1200, LatencyMS: 40})
	mustAppend(Event{TS: time.Unix(130, 0).UTC(), Session: "s2", Client: "cursor", Tool: "verify", RespTokens: 5, RespBytes: 20, LatencyMS: 5, IsError: true})

	rep, err := BuildReport(repo, 10)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rep.Totals.Calls != 4 || rep.Totals.RespTokens != 455 {
		t.Fatalf("totals wrong: %+v", rep.Totals)
	}
	if rep.Totals.Sessions != 2 {
		t.Fatalf("sessions = %d, want 2", rep.Totals.Sessions)
	}
	// heaviest tool first => scout (300) before query (150).
	if rep.Tools[0].Tool != "scout" {
		t.Fatalf("tools not sorted by tokens: %+v", rep.Tools)
	}
	// query aggregated: 2 calls, 150 tokens, avg 75, avg latency 15.
	var q ToolUsage
	for _, tu := range rep.Tools {
		if tu.Tool == "query" {
			q = tu
		}
	}
	if q.Calls != 2 || q.RespTokens != 150 || q.AvgTokens != 75 || q.AvgLatency != 15 {
		t.Fatalf("query agg wrong: %+v", q)
	}
	// last verify is the errored verify call.
	if rep.LastVerify == nil || rep.LastVerify.Tool != "verify" || !rep.LastVerify.IsError {
		t.Fatalf("last verify wrong: %+v", rep.LastVerify)
	}
	// refs trail most-recent first.
	if len(rep.Refs) != 4 || rep.Refs[0].Tool != "verify" {
		t.Fatalf("refs wrong: %+v", rep.Refs)
	}
	// session client preserved.
	var s2 SessionUsage
	for _, s := range rep.Sessions {
		if s.Session == "s2" {
			s2 = s
		}
	}
	if s2.Client != "cursor" || s2.Calls != 2 {
		t.Fatalf("s2 session wrong: %+v", s2)
	}
}

func TestEncodeProjectPath(t *testing.T) {
	got := encodeProjectPath("/home/u/Projects/go/codehelper")
	want := "-home-u-Projects-go-codehelper"
	if got != want {
		t.Fatalf("encode = %q, want %q", got, want)
	}
}

func TestLoadClaudeSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := "/work/myproj"

	dir := filepath.Join(home, ".claude", "projects", encodeProjectPath(repo))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two assistant lines for one session + a non-assistant line that must be ignored.
	lines := `{"type":"user","sessionId":"abc","cwd":"/work/myproj","message":{}}
{"type":"assistant","sessionId":"abc","cwd":"/work/myproj","timestamp":"2026-06-28T10:00:00Z","gitBranch":"main","message":{"model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":40,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200}}}
{"type":"assistant","sessionId":"abc","cwd":"/work/myproj","timestamp":"2026-06-28T10:05:00Z","gitBranch":"main","message":{"model":"claude-opus-4-8","usage":{"input_tokens":50,"output_tokens":20,"cache_read_input_tokens":500,"cache_creation_input_tokens":0}}}
{"type":"assistant","sessionId":"other","cwd":"/somewhere/else","timestamp":"2026-06-28T10:06:00Z","message":{"model":"x","usage":{"input_tokens":9999,"output_tokens":9999}}}
`
	if err := os.WriteFile(filepath.Join(dir, "abc.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, found := LoadClaudeSessions(repo)
	if !found {
		t.Fatalf("expected transcripts found")
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1 (cross-cwd line must be skipped): %+v", len(sessions), sessions)
	}
	s := sessions[0]
	if s.Session != "abc" || s.Messages != 2 {
		t.Fatalf("session agg wrong: %+v", s)
	}
	if s.Input != 150 || s.Output != 60 || s.CacheRead != 1500 || s.CacheCreate != 200 {
		t.Fatalf("token sums wrong: %+v", s)
	}
	if s.Total() != 150+60+1500+200 {
		t.Fatalf("total wrong: %d", s.Total())
	}
	if s.Model != "claude-opus-4-8" || s.GitBranch != "main" {
		t.Fatalf("model/branch wrong: %+v", s)
	}
}

func TestLoadClaudeSessionsAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, found := LoadClaudeSessions("/no/such/project")
	if found {
		t.Fatalf("expected not found for missing transcript dir")
	}
}
