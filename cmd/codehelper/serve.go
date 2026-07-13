package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/VeyrForge/codehelper/internal/agentapi"
	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/mcpsvc"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/spf13/cobra"
)

func serveCmd() *cobra.Command {
	var addr string
	c := &cobra.Command{
		Use:   "serve",
		Short: "Run the local Agent HTTP API (loopback-only, SSE chat events)",
		Long: "Starts the loopback HTTP server that exposes the Go agent loop to thin clients " +
			"(VS Code extension, scripts). LLM settings: CODEHELPER_LLM_* env vars (IDE passes these), " +
			"or ~/.codehelper/llm.json for terminal defaults; env overrides file. " +
			"Set CODEHELPER_API_TOKEN to require a bearer token.",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			autoEnsureCodehelperGitignore(root)
			if !strings.HasPrefix(addr, "127.0.0.1:") && !strings.HasPrefix(addr, "localhost:") {
				return fmt.Errorf("serve binds loopback only; got %q", addr)
			}
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			tools := mcpsvc.NewLocalToolCaller(reg, root)
			srv := &agentapi.Server{
				WorkspaceRoot: root,
				LLM:           llm.ConfigFromEnv(),
				Tools:         tools,
				Token:         strings.TrimSpace(os.Getenv("CODEHELPER_API_TOKEN")),
				Version:       version.Current(),
				DefaultRepo:   tools.DefaultRepo(),
			}

			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			// Announce the bound address as one JSON line so spawning
			// clients can parse the random port.
			announce, _ := json.Marshal(map[string]any{
				"event": "listening",
				"addr":  ln.Addr().String(),
			})
			fmt.Println(string(announce))

			httpSrv := &http.Server{
				Handler:           srv.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			errCh := make(chan error, 1)
			go func() { errCh <- httpSrv.Serve(ln) }()

			select {
			case <-ctx.Done():
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = httpSrv.Shutdown(shutdownCtx)
				return nil
			case err := <-errCh:
				if err == http.ErrServerClosed {
					return nil
				}
				return err
			}
		},
	}
	c.Flags().StringVar(&addr, "addr", "127.0.0.1:0", "loopback listen address (port 0 picks a free port)")
	return c
}
