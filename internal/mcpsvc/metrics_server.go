package mcpsvc

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/telemetry"
)

// normalizeMetricsAddr rewrites host-less binds (:port) to loopback and refuses
// non-loopback addresses (metrics has no auth).
func normalizeMetricsAddr(addr string) (string, error) {
	normalized, loopback, err := normalizeMCPHTTPAddr(addr)
	if err != nil {
		return "", err
	}
	if !loopback {
		return "", fmt.Errorf("metrics refuses non-loopback bind without auth; got %q (use 127.0.0.1 / localhost / [::1])", addr)
	}
	return normalized, nil
}

// StartMetricsServer exposes Prometheus text metrics for tool latencies (best-effort).
// Host-less binds like ":9090" are rewritten to 127.0.0.1; non-loopback binds are refused.
func StartMetricsServer(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	normalized, err := normalizeMetricsAddr(addr)
	if err != nil {
		slog.Error("metrics not started", "addr", addr, "err", err)
		return
	}
	if normalized != addr {
		slog.Info("metrics bind rewritten to loopback", "from", addr, "to", normalized)
	}
	addr = normalized
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		snap := telemetry.Snapshot()
		for name, v := range snap {
			base := strings.ReplaceAll(strings.TrimPrefix(name, "tool."), ".", "_")
			fmt.Fprintf(w, "codehelper_latency_p50_ns{name=%q} %d\n", base, v.P50.Nanoseconds())
			fmt.Fprintf(w, "codehelper_latency_p95_ns{name=%q} %d\n", base, v.P95.Nanoseconds())
			fmt.Fprintf(w, "codehelper_latency_samples{name=%q} %d\n", base, v.N)
		}
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("metrics listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics server", "err", err)
		}
	}()
}
