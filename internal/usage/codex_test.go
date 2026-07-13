package usage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCodexSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()

	// A rollout whose cwd is the repo: meta line + two cumulative token_count events.
	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "13")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := `{"timestamp":"2026-06-13T09:10:58.389Z","type":"session_meta","payload":{"id":"abc123","cwd":"` + repo + `","source":"vscode"}}
{"timestamp":"2026-06-13T09:11:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":500,"output_tokens":100,"reasoning_output_tokens":20,"total_tokens":1100}}}}
{"timestamp":"2026-06-13T09:12:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":3000,"cached_input_tokens":1500,"output_tokens":300,"reasoning_output_tokens":60,"total_tokens":3300}}}}
`
	if err := os.WriteFile(filepath.Join(dir, "rollout-abc.jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second rollout for a DIFFERENT cwd — must be excluded.
	other := `{"timestamp":"2026-06-13T09:10:58Z","type":"session_meta","payload":{"id":"zzz","cwd":"/somewhere/else","source":"cli"}}
{"timestamp":"2026-06-13T09:11:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":999}}}}
`
	if err := os.WriteFile(filepath.Join(dir, "rollout-other.jsonl"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, found := LoadCodexSessions(repo)
	if !found {
		t.Fatal("expected codex sessions dir to be found")
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session for this repo (other cwd excluded), got %d", len(sessions))
	}
	s := sessions[0]
	// Cumulative: the session total is the LAST token_count, not the sum.
	if s.TotalTokens != 3300 || s.Input != 3000 || s.Output != 300 {
		t.Errorf("expected cumulative last totals (3300/3000/300), got %d/%d/%d", s.TotalTokens, s.Input, s.Output)
	}
	if s.Events != 2 {
		t.Errorf("expected 2 token_count events, got %d", s.Events)
	}
}
