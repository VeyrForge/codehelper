package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/VeyrForge/codehelper/internal/enrich"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/spf13/cobra"
)

func enrichCmd() *cobra.Command {
	var (
		asJSON bool
		limit  int
	)
	c := &cobra.Command{
		Use:   "enrich [path]",
		Short: "Index-time LLM enrichment (purpose + aliases) for retrieval",
		Long: "Runs the offline EnrichIndex pass: a local OpenAI-compatible model generates\n" +
			"a one-sentence purpose and up to five domain aliases per symbol, stored in\n" +
			".codehelper/enrich/enrichment.json (content-hash cached).\n\n" +
			"Requires CODEHELPER_ENRICH_URL (mirrors CODEHELPER_EMBED_URL). Run `codehelper\n" +
			"analyze` first. Retrieval picks up the store automatically as a separate,\n" +
			"low-weighted field — zero query-time model cost.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return err
			}
			return runEnrich(cmd.Context(), root, limit, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable result")
	c.Flags().IntVar(&limit, "limit", 0, "max symbols to enrich (0 = all; for smoke tests)")
	return c
}

func runEnrich(ctx context.Context, root string, limit int, asJSON bool) error {
	chat := enrich.FromEnv()
	if chat == nil {
		return fmt.Errorf("CODEHELPER_ENRICH_URL is not set (point at a local OpenAI-compatible model server)")
	}
	m, err := meta.Read(root)
	if err != nil {
		return fmt.Errorf("no index found (run `codehelper analyze` first): %w", err)
	}
	st, err := graph.Open(paths.DBPath(root))
	if err != nil {
		return err
	}
	defer st.Close()

	syms, err := enrich.SymbolsFromStore(ctx, st, m.RepoName)
	if err != nil {
		return err
	}
	if limit > 0 && limit < len(syms) {
		syms = syms[:limit]
	}
	store, err := enrich.OpenStore(enrich.DefaultPath(root))
	if err != nil {
		return err
	}
	res, err := enrich.EnrichBatch(ctx, enrich.Generator{Chat: chat}, syms, store)
	if err != nil {
		return err
	}
	out := struct {
		Repo       string             `json:"repo"`
		Symbols    int                `json:"symbols"`
		StorePath  string             `json:"store_path"`
		StoreCount int                `json:"store_count"`
		Result     enrich.BatchResult `json:"result"`
	}{
		Repo:       m.RepoName,
		Symbols:    len(syms),
		StorePath:  enrich.DefaultPath(root),
		StoreCount: store.Len(),
		Result:     res,
	}
	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("enrich %s: symbols=%d generated=%d cached=%d failed=%d store=%s (%d entries)\n",
		out.Repo, out.Symbols, res.Generated, res.Cached, res.Failed, out.StorePath, out.StoreCount)
	return nil
}
