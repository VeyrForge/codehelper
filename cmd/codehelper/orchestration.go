package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/mcpsvc"
	"github.com/VeyrForge/codehelper/internal/orchestrator"
	"github.com/VeyrForge/codehelper/internal/orchestrator/eval"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/spf13/cobra"
)

func orchestrationCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "orchestration",
		Short: "Enable or disable local orchestration (guided investigation workflows)",
		Long: `Local orchestration runs guided investigation workflows locally:
classify task → deterministic tool chain → context pack + compact trace.

When disabled, orchestrate/rerun/feedback tools return a redirect notice.
The orchestration tool (action=enable|disable|status) always works.

  codehelper orchestration enable
  codehelper orchestration disable
  codehelper orchestration status`,
	}
	c.AddCommand(orchestrationEnableCmd(), orchestrationDisableCmd(), orchestrationStatusCmd(), orchestrationEvalCmd())
	return c
}

func orchestrationEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable [path]",
		Short: "Enable local orchestration for the current project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return setOrchestration(args, true)
		},
	}
}

func orchestrationDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable [path]",
		Short: "Disable local orchestration for the current project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return setOrchestration(args, false)
		},
	}
}

func orchestrationStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [path]",
		Short: "Show local orchestration status",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			_, repoRoot, err := indexer.ResolveIndexPaths(argPath(args), "")
			if err != nil {
				return fmt.Errorf("orchestration status requires a git repository: %w", err)
			}
			cfg, err := orchestrator.Load(repoRoot)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{
				"path":    orchestrator.Path(repoRoot),
				"enabled": cfg.Enabled,
				"tools":   []string{"orchestrate", "orchestration_rerun", "orchestration_feedback", "run_trace", "explain_run", "orchestration_memory"},
				"local_llm": map[string]any{
					"ready": orchestrator.ResolveLocalChat() != nil,
					"hint":  "Configure green engine (`codehelper green enable`) with llm server → CODEHELPER_ENRICH_URL, or ~/.codehelper/llm.json",
				},
			})
		},
	}
}

func setOrchestration(args []string, on bool) error {
	_, repoRoot, err := indexer.ResolveIndexPaths(argPath(args), "")
	if err != nil {
		return fmt.Errorf("orchestration requires a git repository: %w", err)
	}
	if err := orchestrator.SetEnabled(repoRoot, on); err != nil {
		return err
	}
	state := "disabled"
	if on {
		state = "enabled"
	}
	fmt.Printf("local orchestration %s (%s)\n", state, orchestrator.Path(repoRoot))
	return nil
}

func orchestrationEvalCmd() *cobra.Command {
	var outPath string
	var skipIndex bool
	var forceIndex bool
	var variants []string
	c := &cobra.Command{
		Use:   "eval",
		Short: "Benchmark orchestrate vs manual MCP vs no-MCP baseline across indexed projects",
		Long: `Runs project-adaptive tasks on every indexed repo in the registry.
Use --variants all for index/format permutations (fresh/skip/force index, TOON vs JSON).

  codehelper orchestration eval
  codehelper orchestration eval --variants all --out report.json
  codehelper orchestration eval --force-index`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runOrchestrationEval(outPath, skipIndex, forceIndex, variants)
		},
	}
	c.Flags().StringVar(&outPath, "out", "", "Write JSON report to path (default stdout)")
	c.Flags().BoolVar(&skipIndex, "skip-index", false, "Skip pre-benchmark analyze refresh")
	c.Flags().BoolVar(&forceIndex, "force-index", false, "Force analyze before each project")
	c.Flags().StringSliceVar(&variants, "variants", nil, "Variant names or 'all'")
	return c
}

func runOrchestrationEval(outPath string, skipIndex, forceIndex bool, variantNames []string) error {
	reg, err := registry.Load()
	if err != nil {
		return err
	}
	runner := mcpsvc.EvalRunner(reg)
	if skipIndex {
		runner.Config.IndexMode = eval.IndexSkip
		runner.RefreshIndex = false
	} else if forceIndex {
		runner.Config.IndexMode = eval.IndexForce
		runner.RefreshIndex = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()

	selected := eval.ResolveVariants(variantNames)
	multi := len(selected) > 1 || (len(variantNames) == 1 && variantNames[0] == "all")

	var w = os.Stdout
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	if !multi {
		runner.Config = selected[0].Config
		runner.RefreshIndex = runner.Config.IndexMode != eval.IndexSkip
		rep, err := runner.RunAll(ctx, reg)
		if err != nil {
			return err
		}
		if err := enc.Encode(rep); err != nil {
			return err
		}
		if outPath != "" {
			fmt.Fprintf(os.Stderr, "wrote %s (%d projects, %s)\n", outPath, len(rep.Projects), selected[0].Name)
		}
		return nil
	}

	vrep, err := runner.RunAllVariants(ctx, reg, selected)
	if err != nil {
		return err
	}
	if err := enc.Encode(vrep); err != nil {
		return err
	}
	if outPath != "" {
		fmt.Fprintf(os.Stderr, "wrote %s (%d variants)\n", outPath, len(vrep.Variants))
		for _, note := range vrep.Analysis.FormatImpact {
			fmt.Fprintln(os.Stderr, " ", note)
		}
		for _, note := range vrep.Analysis.IndexImpact {
			fmt.Fprintln(os.Stderr, " ", note)
		}
	}
	return nil
}
