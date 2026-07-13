package mcpsvc

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/telemetry"
)

// StartMetricsServer exposes Prometheus text metrics for tool latencies (best-effort).
func StartMetricsServer(addr string) {
	if strings.TrimSpace(addr) == "" {
		return
	}
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
