package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ClaudeSession is the real model-token usage for one Claude Code session,
// summed from that session's transcript. These are ACTUAL billed tokens reported
// by the API, not estimates.
type ClaudeSession struct {
	Session     string    `json:"session"`
	Model       string    `json:"model"`
	GitBranch   string    `json:"git_branch"`
	Messages    int       `json:"messages"`
	Input       int       `json:"input_tokens"`
	Output      int       `json:"output_tokens"`
	CacheRead   int       `json:"cache_read_tokens"`
	CacheCreate int       `json:"cache_create_tokens"`
	FirstTS     time.Time `json:"first_ts"`
	LastTS      time.Time `json:"last_ts"`
}

// Total is input + output + cache (creation + read) — a single comparable number
// for "how big was this session" across the report.
func (c ClaudeSession) Total() int {
	return c.Input + c.Output + c.CacheRead + c.CacheCreate
}

// claudeProjectDir maps a repo root to Claude Code's transcript directory.
// Claude Code stores transcripts under ~/.claude/projects/<encoded>, where the
// encoding replaces every non-alphanumeric character of the absolute project
// path with '-' (e.g. /home/u/Projects/app -> -home-u-Projects-app).
func claudeProjectDir(repoRoot string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		abs = repoRoot
	}
	return filepath.Join(home, ".claude", "projects", encodeProjectPath(abs)), true
}

func encodeProjectPath(abs string) string {
	var b strings.Builder
	b.Grow(len(abs))
	for _, r := range abs {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

type transcriptLine struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Timestamp string `json:"timestamp"`
	GitBranch string `json:"gitBranch"`
	Message   struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// LoadClaudeSessions reads Claude Code transcripts for a project and returns
// per-session real token usage, most-recent first. Returns (nil, false) when no
// transcript directory exists (Claude was never used here, or this is a
// Cursor/Codex-only project) — callers treat that as "real Claude tokens
// unavailable" rather than an error.
func LoadClaudeSessions(repoRoot string) ([]ClaudeSession, bool) {
	dir, ok := claudeProjectDir(repoRoot)
	if !ok {
		return nil, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}

	byID := map[string]*ClaudeSession{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		accumulateTranscript(filepath.Join(dir, e.Name()), repoRoot, byID)
	}
	if len(byID) == 0 {
		return nil, true
	}

	out := make([]ClaudeSession, 0, len(byID))
	for _, s := range byID {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastTS.After(out[j].LastTS) })
	return out, true
}

func accumulateTranscript(path, repoRoot string, byID map[string]*ClaudeSession) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	absRepo, _ := filepath.Abs(repoRoot)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var tl transcriptLine
		if err := json.Unmarshal(line, &tl); err != nil {
			continue
		}
		if tl.Type != "assistant" || tl.SessionID == "" {
			continue
		}
		// Defensive: the encoded dir is project-specific, but skip any stray line
		// whose cwd points elsewhere so cross-project bleed is impossible.
		if tl.CWD != "" && absRepo != "" {
			if c, _ := filepath.Abs(tl.CWD); c != absRepo {
				continue
			}
		}
		u := tl.Message.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
			continue
		}
		s := byID[tl.SessionID]
		if s == nil {
			s = &ClaudeSession{Session: tl.SessionID}
			byID[tl.SessionID] = s
		}
		s.Messages++
		s.Input += u.InputTokens
		s.Output += u.OutputTokens
		s.CacheRead += u.CacheReadInputTokens
		s.CacheCreate += u.CacheCreationInputTokens
		if tl.Message.Model != "" {
			s.Model = tl.Message.Model
		}
		if tl.GitBranch != "" {
			s.GitBranch = tl.GitBranch
		}
		if ts, err := time.Parse(time.RFC3339, tl.Timestamp); err == nil {
			if s.FirstTS.IsZero() || ts.Before(s.FirstTS) {
				s.FirstTS = ts
			}
			if ts.After(s.LastTS) {
				s.LastTS = ts
			}
		}
	}
}
