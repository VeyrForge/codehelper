// Package freshness reports whether the on-disk index for a repo is in sync
// with the git working tree, and whether a watch daemon is keeping it fresh
// automatically. It is consumed by MCP tools so LLMs can warn the user when
// query/context/impact answers may be stale.
package freshness

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/VeyrForge/codehelper/internal/daemon"
	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/meta"
)

// ActionRequired is machine-readable guidance for clients (argv token groups).
type ActionRequired struct {
	Code     string     `json:"code,omitempty"`
	Message  string     `json:"message,omitempty"`
	Commands [][]string `json:"commands,omitempty"`
}

// Report is the response shape attached to MCP tool outputs.
type Report struct {
	IndexedCommit string    `json:"indexed_commit,omitempty"`
	HeadCommit    string    `json:"head_commit,omitempty"`
	IndexedAt     time.Time `json:"indexed_at,omitempty"`
	Stale         bool      `json:"stale"`
	StaleReason   string    `json:"stale_reason,omitempty"`
	// IndexLag is set to "possible" when a watch daemon IS running (so the index
	// is NOT marked stale — it will converge) but the working tree has changed
	// since the last index build. It is the honest-freshness signal: a symbol you
	// just edited may not be indexed yet, so a query/context miss is expected and
	// the agent should disk-check rather than conclude the symbol doesn't exist.
	IndexLag     string `json:"index_lag,omitempty"`
	WatchRunning bool   `json:"watch_running"`
	WatchPID     int    `json:"watch_pid,omitempty"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
	// ActionRequired complements SuggestedFix with structured argv-style commands.
	ActionRequired *ActionRequired `json:"action_required,omitempty"`
}

// MarshalJSON trims fields that ship on every MCP tool response but carry no
// signal for an agent: watch_pid (a raw process id) and head_commit when it equals
// indexed_commit (redundant — they differ only when the index is behind HEAD, and
// that drift is the only case worth reporting). The Go struct is unchanged, so any
// code reading r.WatchPID / r.HeadCommit still sees the full values.
func (r Report) MarshalJSON() ([]byte, error) {
	type alias Report // break the MarshalJSON recursion
	a := alias(r)
	a.WatchPID = 0 // omitempty drops it
	if a.HeadCommit == a.IndexedCommit {
		a.HeadCommit = "" // omitempty drops it — same commit as indexed
	}
	// Second precision is all an agent needs to judge staleness; the nanosecond
	// fraction is ~10 wasted bytes on every response.
	a.IndexedAt = a.IndexedAt.Truncate(time.Second)
	return json.Marshal(a)
}

// Inspect computes a freshness Report for the given index root. The report
// is best-effort: missing meta or non-git roots produce a stale result with
// an actionable suggestion.
func Inspect(indexRoot string) Report {
	r := Report{}
	m, merr := meta.Read(indexRoot)
	if merr != nil {
		if errors.Is(merr, os.ErrNotExist) {
			r.Stale = true
			r.StaleReason = "no index"
			r.SuggestedFix = "codehelper analyze"
			attachActionRequired(&r)
			return r
		}
	}
	if m != nil {
		r.IndexedCommit = m.LastCommit
		r.IndexedAt = m.IndexedAt
	}
	if cur, err := gitutil.HeadCommit(indexRoot); err == nil && cur != "" {
		r.HeadCommit = cur
		if m != nil && m.LastCommit != "" && m.LastCommit != cur {
			r.Stale = true
			r.StaleReason = "git HEAD advanced past indexed commit"
			r.SuggestedFix = "codehelper analyze (or start codehelper watch --daemon)"
		}
	}
	if st, err := daemon.ReadState(indexRoot); err == nil && st != nil && st.PID > 0 && daemon.ProcessAlive(st.PID) {
		// Verify the recorded PID is actually alive — a crashed/killed daemon can
		// leave a stale state file that would otherwise falsely report "running"
		// and suppress the staleness warning below.
		r.WatchRunning = true
		r.WatchPID = st.PID
	}
	// Catch UNCOMMITTED working-tree edits that the git-HEAD check above cannot see
	// (HEAD has not moved). WorkingTreeChangedSince early-exits on the first newer
	// file, so the "something changed" path is cheap regardless of watch state.
	// When NO watch daemon is running this is hard staleness — the user must
	// reindex. When one IS running we do NOT mark the index stale (it reindexes on
	// change and converges), but we still surface index_lag="possible" so the agent
	// knows a just-edited symbol may not be indexed yet: a query/context miss is
	// then expected, and disk fallback / read_workspace_file is the right move
	// rather than concluding the symbol doesn't exist. This closes the false-fresh
	// gap (stale:false + watch_running:true while a new symbol is missing).
	// Language-agnostic; works for any project, git or not.
	if !r.Stale && m != nil && WorkingTreeChangedSince(indexRoot, m.IndexedAt) {
		if r.WatchRunning {
			r.IndexLag = "possible"
			r.SuggestedFix = "working tree changed since index; watch daemon should converge shortly — if a symbol is missing, read_workspace_file the path or run codehelper analyze"
		} else {
			r.Stale = true
			r.StaleReason = "working tree changed since index (uncommitted edits, no watch daemon)"
			r.SuggestedFix = "codehelper analyze --force (or start codehelper watch --daemon)"
		}
	}
	if r.Stale && r.WatchRunning {
		r.SuggestedFix = "watch daemon running; freshness should converge automatically"
	}
	attachActionRequired(&r)
	return r
}

func attachActionRequired(r *Report) {
	if r == nil {
		return
	}
	// Stale index: concrete re-index command (watch may auto-fix; still offer analyze).
	if r.Stale {
		msg := r.StaleReason
		if msg == "" {
			msg = "index is stale"
		}
		code := "stale_index"
		if r.StaleReason == "no index" {
			code = "no_index"
		}
		ar := &ActionRequired{
			Code:     code,
			Message:  msg,
			Commands: [][]string{{"codehelper", "analyze"}},
		}
		if r.WatchRunning && r.StaleReason != "no index" {
			ar.Commands = nil
			ar.Message = msg + "; watch daemon is running — wait for convergence or run codehelper analyze if stuck"
		}
		r.ActionRequired = ar
		return
	}
}
