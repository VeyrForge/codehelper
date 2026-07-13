// Package usage records, persists, and reports per-project tool-usage telemetry
// for codehelper's MCP server.
//
// What it measures and why: codehelper cannot see the calling agent's real LLM
// token bill (prompt/completion tokens live in Claude Code / Cursor / Codex, not
// in this server). What it CAN measure precisely is the size of every tool
// response it injects into the agent's context — and since the project rule is
// that agents route reads/searches through codehelper tools INSTEAD of raw
// Read/Grep/Bash, those response sizes ARE the context volume. So this package
// answers "where is per-project context going, per tool, per session, per
// client" deterministically. For Claude specifically, the real model-token bill
// is additionally recoverable from its on-disk transcripts (see claude.go);
// Cursor/Codex do not expose comparable local token counts, so for those clients
// the report shows codehelper-side output only and says so.
//
// Storage: append-only JSONL under the project's index dir
// (<RepoIndexDir>/usage/events.jsonl), so it is per-project, respects
// CODEHELPER_INDEX_HOME, and never touches the repo when an external index home
// is configured. Recording is best-effort: a write error is dropped, never
// surfaced to the tool call.
package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// Event is one recorded MCP tool call. Args and Snippet are capped, single-line
// previews of the request and response so a human can review HOW each tool was
// used and whether its answer was any good — without bloating the log.
type Event struct {
	TS         time.Time `json:"ts"`
	Session    string    `json:"session"`
	Client     string    `json:"client"` // claude-code | cursor | codex | unknown
	Tool       string    `json:"tool"`
	RepoArg    string    `json:"repo_arg,omitempty"` // explicit repo argument, if any
	Args       string    `json:"args,omitempty"`     // capped, single-line request preview
	Snippet    string    `json:"snippet,omitempty"`  // capped, single-line response preview
	ReqBytes   int       `json:"req_bytes"`
	RespBytes  int       `json:"resp_bytes"`
	RespTokens int       `json:"resp_tokens"` // estimate; see EstimateTokens
	LatencyMS  int64     `json:"latency_ms"`
	IsError    bool      `json:"is_error"`
	// Disabled marks a call recorded while the project had tools turned off: the
	// tool was shadow-executed for measurement and its result was NOT given to the
	// agent. Lets a report separate "what codehelper would have injected" from
	// "what it actually injected".
	Disabled bool `json:"disabled,omitempty"`
}

// Preview caps for the reviewable Args/Snippet fields — short by design so the
// log stays scannable.
const (
	MaxArgsChars    = 200
	MaxSnippetChars = 280
)

// Preview collapses all whitespace runs to single spaces and truncates to max
// runes (appending … when cut), turning a multi-line tool payload into one
// scannable line.
func Preview(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// EstimateTokens approximates the token count of a tool response. It uses the
// ~4-characters-per-token heuristic that holds well for English prose and code
// across the Claude tokenizer family — deliberately dependency-free, since an
// exact tokenizer would couple this to a specific model and add a heavy dep for
// a number that only needs to be directionally right.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// maxEventsFileBytes rotates events.jsonl once it grows past this, keeping one
// previous generation (.1) so the log never grows unbounded on a long-lived
// project while still retaining recent history.
const maxEventsFileBytes = 5 << 20 // 5 MiB

var appendMu sync.Mutex

// Dir is the per-project usage directory.
func Dir(repoRoot string) string {
	return filepath.Join(paths.RepoIndexDir(repoRoot), "usage")
}

// EventsPath is the per-project append-only event log.
func EventsPath(repoRoot string) string {
	return filepath.Join(Dir(repoRoot), "events.jsonl")
}

// Append writes one event to the project's log. Best-effort: any error is
// returned for the caller to ignore, never blocks or corrupts the tool path.
func Append(repoRoot string, ev Event) error {
	if repoRoot == "" {
		return nil
	}
	appendMu.Lock()
	defer appendMu.Unlock()

	path := EventsPath(repoRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	rotateIfLarge(path)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = f.Write(b)
	return err
}

func rotateIfLarge(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < maxEventsFileBytes {
		return
	}
	_ = os.Rename(path, path+".1")
}

// Load reads all events for a project (current log only; the rotated .1 is left
// out of reports to keep them about the recent window). A malformed line is
// skipped rather than failing the whole read.
func Load(repoRoot string) ([]Event, error) {
	f, err := os.Open(EventsPath(repoRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}
