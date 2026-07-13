package main

import (
	"log/slog"

	"github.com/VeyrForge/codehelper/internal/mcpsvc"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/spf13/cobra"
)

func mcpCmd() *cobra.Command {
	var httpAddr string
	var metricsAddr string
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Start Model Context Protocol server (stdio by default)",
		Long:  mcpLongHelp,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			autoEnsureCodehelperGitignore("")
			if err := mcpsvc.Run(reg, httpAddr, metricsAddr); err != nil {
				return err
			}
			return nil
		},
	}
	c.Flags().StringVar(&httpAddr, "http", "", "listen address for Streamable HTTP (e.g. :8765); empty uses stdio")
	c.Flags().StringVar(&metricsAddr, "metrics-addr", "", "optional Prometheus text /metrics listen address (e.g. :9123)")
	c.SilenceUsage = true
	_ = slog.Default()
	return c
}
