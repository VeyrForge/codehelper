package main

import (
	"context"
	"fmt"
	"time"

	"github.com/VeyrForge/codehelper/internal/green"
	"github.com/spf13/cobra"
)

func greenCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "green",
		Short: "Manage the local green engine (embedding + LLM servers for rerank/enrichment)",
		Long: `The green engine is the optional local LLM stack that powers two opt-in
codehelper features: semantic rerank (CODEHELPER_EMBED_URL) and index-time
enrichment (CODEHELPER_ENRICH_URL). When enabled, the codehelper MCP server
keeps it alive automatically (spawns it if down, respawns it if killed) and
points itself at it. When disabled, codehelper runs its deterministic path
(BM25 + trigram, no rerank, no enrichment) — nothing breaks, you just lose the
LLM-powered ranking signal.

Config lives at ~/.codehelper/green.json. For semantic rerank alone (tiny model,
~195 MB on first use), prefer "codehelper green init-embed" or
"bash scripts/install-local-embed.sh" — see docs/LOCAL_EMBED.md.`,
	}
	c.AddCommand(greenStatusCmd(), greenStartCmd(), greenStopCmd(), greenRestartCmd(), greenEnableCmd(), greenDisableCmd(), greenInitEmbedCmd())
	return c
}

func loadGreen() (green.Config, error) {
	cfg, ok, err := green.Load()
	if err != nil {
		return green.Config{}, err
	}
	if !ok {
		return green.Config{}, fmt.Errorf("no green config — create ~/.codehelper/green.json (see `codehelper green status`)")
	}
	return cfg, nil
}

func greenStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show config path, enabled state, and per-server health",
		RunE: func(_ *cobra.Command, _ []string) error {
			path, _ := green.ConfigPath()
			fmt.Println("config:", path)
			cfg, ok, err := green.Load()
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("state:  not configured (codehelper runs deterministic — no rerank/enrichment)")
				return nil
			}
			state := "DISABLED (deterministic fallback)"
			if cfg.Enabled {
				state = "ENABLED"
			}
			fmt.Println("state: ", state)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			for _, st := range green.Status(ctx, cfg) {
				health := "DOWN"
				if st.Healthy {
					health = "up"
				}
				owner := "codehelper-managed"
				if st.External {
					owner = "external (systemd/etc)"
				} else if st.PID > 0 {
					owner = fmt.Sprintf("pid %d", st.PID)
				}
				fmt.Printf("  %-6s :%d  %-4s  %-22s  %s\n", st.Name, st.Port, health, st.URLEnv, owner)
			}
			return nil
		},
	}
}

func greenStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Ensure the servers are running (spawn any that are down)",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadGreen()
			if err != nil {
				return err
			}
			if !cfg.Enabled {
				return fmt.Errorf("green is disabled; run `codehelper green enable` first")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			logf := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
			if err := green.Ensure(ctx, cfg, logf); err != nil {
				return err
			}
			fmt.Println("green engine ready.")
			return nil
		},
	}
}

func greenStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the managed servers",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadGreen()
			if err != nil {
				return err
			}
			green.StopAll(cfg)
			fmt.Println("green engine stopped.")
			return nil
		},
	}
}

func greenRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Stop then start the managed servers",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadGreen()
			if err != nil {
				return err
			}
			green.StopAll(cfg)
			time.Sleep(1 * time.Second)
			if !cfg.Enabled {
				fmt.Println("green is disabled; stopped only.")
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			logf := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
			if err := green.Ensure(ctx, cfg, logf); err != nil {
				return err
			}
			fmt.Println("green engine restarted.")
			return nil
		},
	}
}

func greenEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Enable the green engine (MCP will keep it alive and use it)",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadGreen()
			if err != nil {
				return err
			}
			cfg.Enabled = true
			if err := green.Save(cfg); err != nil {
				return err
			}
			fmt.Println("green enabled. Restart the codehelper MCP server (or run `codehelper green start`) to bring it up.")
			return nil
		},
	}
}

func greenDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Disable the green engine and stop it (codehelper falls back to deterministic)",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := loadGreen()
			if err != nil {
				return err
			}
			cfg.Enabled = false
			if err := green.Save(cfg); err != nil {
				return err
			}
			green.StopAll(cfg)
			fmt.Println("green disabled and stopped. codehelper now runs deterministic (BM25 + trigram, no rerank/enrichment).")
			return nil
		},
	}
}

func greenInitEmbedCmd() *cobra.Command {
	var withLLM bool
	var force bool
	c := &cobra.Command{
		Use:   "init-embed",
		Short: "Write embed-only ~/.codehelper/green.json (tiny local semantic rerank)",
		Long: `Writes an embed-only green config so codehelper can auto-detect a local
OpenAI-compatible embed server on :8766 without requiring CODEHELPER_EMBED_URL.

First start downloads ~195 MB (Granite multilingual). Does not pull multi-GB
chat weights unless --with-llm is set. CI should not run this.

See docs/LOCAL_EMBED.md.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			path, err := green.ConfigPath()
			if err != nil {
				return err
			}
			if _, ok, err := green.Load(); err != nil {
				return err
			} else if ok && !force {
				fmt.Println("config already exists:", path)
				fmt.Println("pass --force to overwrite, or edit the file by hand.")
				return nil
			}
			var cfg green.Config
			if withLLM {
				cfg = green.DefaultFullConfig("ge")
			} else {
				cfg = green.DefaultEmbedOnlyConfig("ge")
			}
			if err := green.Save(cfg); err != nil {
				return err
			}
			fmt.Println("wrote", path)
			if withLLM {
				fmt.Println("profile: embed + enrich (chat may download a multi-GB GGUF on first start)")
			} else {
				fmt.Println("profile: embed-only (~195 MB Granite on first `codehelper green start`)")
			}
			fmt.Println("next: ge embed install   # once, if embed venv missing")
			fmt.Println("      codehelper green start")
			fmt.Println("MCP auto-detects http://127.0.0.1:8766 when healthy; or set CODEHELPER_EMBED_URL.")
			return nil
		},
	}
	c.Flags().BoolVar(&withLLM, "with-llm", false, "also configure enrich/chat server (may download multi-GB weights)")
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing green.json")
	return c
}
