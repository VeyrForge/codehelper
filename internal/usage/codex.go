package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CodexSession is the real model-token usage for one Codex (OpenAI) session,
// taken from that session's rollout file. Codex writes rollouts under
// ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl; each carries a session_meta event
// (id + cwd) and periodic token_count events whose info.total_token_usage is the
// CUMULATIVE total for the session — so the session total is the LAST such event,
// not a sum. This makes "with codehelper" token usage measurable for Codex too,
// not just Claude.
type CodexSession struct {
	Session     string    `json:"session"`
	Source      string    `json:"source,omitempty"` // codex_vscode | codex_cli | …
	FirstTS     time.Time `json:"first_ts"`
	LastTS      time.Time `json:"last_ts"`
	Input       int       `json:"input_tokens"`
	CachedInput int       `json:"cached_input_tokens"`
	Output      int       `json:"output_tokens"`
	Reasoning   int       `json:"reasoning_output_tokens"`
	TotalTokens int       `json:"total_tokens"`
	Events      int       `json:"token_count_events"`
}

// Total is the single comparable grand total for a Codex session.
func (c CodexSession) Total() int { return c.TotalTokens }

// codexLine matches both event shapes: a top-level `session_meta` (project/id in
// payload), and an `event_msg` whose payload.type is `token_count` (cumulative
// usage in payload.info.total_token_usage).
type codexLine struct {
	Type      string `json:"type"` // session_meta | event_msg | …
	Timestamp string `json:"timestamp"`
	Payload   struct {
		ID         string `json:"id"`
		CWD        string `json:"cwd"`
		Source     string `json:"source"`
		Originator string `json:"originator"`
		Timestamp  string `json:"timestamp"`
		Type       string `json:"type"` // payload-level event type, e.g. token_count
		Info       struct {
			TotalTokenUsage struct {
				InputTokens           int `json:"input_tokens"`
				CachedInputTokens     int `json:"cached_input_tokens"`
				OutputTokens          int `json:"output_tokens"`
				ReasoningOutputTokens int `json:"reasoning_output_tokens"`
				TotalTokens           int `json:"total_tokens"`
			} `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

func codexSessionsDir() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".codex", "sessions"), true
}

// LoadCodexSessions returns per-session real Codex token usage for the rollouts
// whose cwd is within repoRoot, most-recent first. Returns (nil, false) when no
// Codex sessions directory exists. It reads each rollout's first line to filter by
// project before scanning the rest, so unrelated sessions cost one line of I/O.
func LoadCodexSessions(repoRoot string) ([]CodexSession, bool) {
	dir, ok := codexSessionsDir()
	if !ok {
		return nil, false
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, false
	}
	absRepo, _ := filepath.Abs(repoRoot)

	var out []CodexSession
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if s, ok := parseCodexRollout(path, absRepo); ok {
			out = append(out, s)
		}
		return nil
	})
	if len(out) == 0 {
		return nil, true
	}
	sortByLastTSDesc(out)
	return out, true
}

// parseCodexRollout reads one rollout file. It returns (_, false) unless the
// session's cwd is within absRepo. The session total is the last token_count's
// cumulative total_token_usage.
func parseCodexRollout(path, absRepo string) (CodexSession, bool) {
	f, err := os.Open(path)
	if err != nil {
		return CodexSession{}, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var s CodexSession
	matched := false
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var cl codexLine
		if err := json.Unmarshal(line, &cl); err != nil {
			continue
		}
		if cl.Type == "session_meta" {
			cwd, _ := filepath.Abs(cl.Payload.CWD)
			if absRepo != "" && cwd != "" && !withinRepo(cwd, absRepo) {
				return CodexSession{}, false // not this project — stop early
			}
			matched = true
			s.Session = cl.Payload.ID
			s.Source = firstNonEmptyStr(cl.Payload.Source, cl.Payload.Originator)
			s.FirstTS = parseTS(firstNonEmptyStr(cl.Payload.Timestamp, cl.Timestamp))
			s.LastTS = s.FirstTS
			continue
		}
		if cl.Payload.Type == "token_count" {
			u := cl.Payload.Info.TotalTokenUsage
			if u.TotalTokens == 0 && u.InputTokens == 0 && u.OutputTokens == 0 {
				continue
			}
			s.Events++
			s.Input = u.InputTokens
			s.CachedInput = u.CachedInputTokens
			s.Output = u.OutputTokens
			s.Reasoning = u.ReasoningOutputTokens
			s.TotalTokens = u.TotalTokens
			if ts := parseTS(cl.Timestamp); !ts.IsZero() {
				s.LastTS = ts
			}
		}
	}
	if !matched || s.Session == "" || s.TotalTokens == 0 {
		return CodexSession{}, false
	}
	return s, true
}

// withinRepo reports whether cwd is the repo root or nested inside it.
func withinRepo(cwd, absRepo string) bool {
	if cwd == absRepo {
		return true
	}
	rel, err := filepath.Rel(absRepo, cwd)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseTS(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Time{}
}

func sortByLastTSDesc(s []CodexSession) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].LastTS.After(s[j-1].LastTS); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
