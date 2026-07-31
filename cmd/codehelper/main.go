package main

import (
	"os"

	"github.com/VeyrForge/codehelper/internal/green"
)

func main() {
	// Point this process at the local green engine (semantic rerank + index-time
	// enrichment) when it is configured and enabled — for analyze, watch, query,
	// and mcp alike. No-op (deterministic) when disabled or unconfigured.
	_, _ = green.LoadAndExport()

	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
