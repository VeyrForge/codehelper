package freshness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReportMarshalTrimsNoiseFields(t *testing.T) {
	// Common case: index at HEAD, watcher running. head_commit is redundant with
	// indexed_commit and watch_pid is a bare process id — both should be dropped.
	full := Report{
		IndexedCommit: "0a9c72abf7c9b3222bc2218e477141ed03eeb27f",
		HeadCommit:    "0a9c72abf7c9b3222bc2218e477141ed03eeb27f",
		IndexedAt:     time.Unix(1751662402, 444365164).UTC(), // sub-second fraction
		Stale:         false,
		WatchRunning:  true,
		WatchPID:      45056,
	}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	if strings.Contains(js, "watch_pid") {
		t.Errorf("watch_pid should be dropped: %s", js)
	}
	if strings.Contains(js, ".444365164") {
		t.Errorf("indexed_at nanosecond fraction should be truncated: %s", js)
	}
	if strings.Contains(js, "head_commit") {
		t.Errorf("head_commit should be dropped when equal to indexed_commit: %s", js)
	}
	// Signal that IS useful must survive.
	if !strings.Contains(js, "indexed_commit") || !strings.Contains(js, "watch_running") {
		t.Errorf("useful fields dropped: %s", js)
	}

	// The Go struct is untouched — code reading the fields still sees them.
	if full.WatchPID != 45056 || full.HeadCommit == "" {
		t.Errorf("MarshalJSON mutated the struct")
	}

	// Drift case: index behind HEAD — head_commit MUST be reported.
	drift := Report{
		IndexedCommit: "aaaa111",
		HeadCommit:    "bbbb222",
		Stale:         true,
	}
	db, _ := json.Marshal(drift)
	if !strings.Contains(string(db), "bbbb222") {
		t.Errorf("head_commit must be reported when it differs from indexed_commit: %s", db)
	}

	// Report the per-response byte savings.
	type alias Report
	untrimmed, _ := json.Marshal(alias(full))
	t.Logf("freshness JSON: trimmed=%d bytes, untrimmed=%d bytes, saved=%d", len(b), len(untrimmed), len(untrimmed)-len(b))
}
