package main

import (
	"log/slog"
	"os"

	"github.com/VeyrForge/codehelper/internal/mcpsvc"
	"github.com/VeyrForge/codehelper/internal/registry"
)

func main() {
	reg, err := registry.Load()
	if err != nil {
		slog.Error("registry", "err", err)
		os.Exit(1)
	}
	metrics := os.Getenv("CODEHELPER_METRICS_ADDR")
	if err := mcpsvc.Run(reg, os.Getenv("CODEHELPER_MCP_HTTP"), metrics); err != nil {
		slog.Error("mcp", "err", err)
		os.Exit(1)
	}
}
