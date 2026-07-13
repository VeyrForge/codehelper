package telemetry

import (
	"log/slog"
	"sort"
	"sync"
	"time"
)

var (
	mu      sync.Mutex
	latency = map[string][]time.Duration{}
)

// RecordLatency appends duration for a tool or phase (nanoseconds histogram in-memory).
func RecordLatency(name string, d time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	if len(latency[name]) > 1000 {
		latency[name] = latency[name][len(latency[name])-500:]
	}
	latency[name] = append(latency[name], d)
}

// Percentiles returns approximate p50 and p95 from recent samples.
func Percentiles(name string) (p50, p95 time.Duration, n int) {
	mu.Lock()
	defer mu.Unlock()
	ls := append([]time.Duration(nil), latency[name]...)
	n = len(ls)
	if n == 0 {
		return 0, 0, 0
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i] < ls[j] })
	p50 = ls[n*50/100]
	if n*95/100 >= n {
		p95 = ls[n-1]
	} else {
		p95 = ls[n*95/100]
	}
	return p50, p95, n
}

// Snapshot returns p50/p95 for all tracked names.
func Snapshot() map[string]struct {
	P50, P95 time.Duration
	N        int
} {
	mu.Lock()
	defer mu.Unlock()
	out := map[string]struct {
		P50, P95 time.Duration
		N        int
	}{}
	for name, ls := range latency {
		cp := append([]time.Duration(nil), ls...)
		n := len(cp)
		if n == 0 {
			continue
		}
		sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
		p50 := cp[n*50/100]
		var p95 time.Duration
		if n*95/100 >= n {
			p95 = cp[n-1]
		} else {
			p95 = cp[n*95/100]
		}
		out[name] = struct {
			P50, P95 time.Duration
			N        int
		}{P50: p50, P95: p95, N: n}
	}
	return out
}

// LogSummary writes p50-ish recent median to slog (debug).
func LogSummary(name string) {
	p50, p95, n := Percentiles(name)
	if n == 0 {
		return
	}
	slog.Debug("telemetry", "phase", name, "p50", p50, "p95", p95, "samples", n)
}

// Timer returns a function to defer-call to record elapsed time.
func Timer(name string) func() {
	t0 := time.Now()
	return func() {
		RecordLatency(name, time.Since(t0))
	}
}
