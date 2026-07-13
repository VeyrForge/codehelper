package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/mcpsvc"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/plan"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/review"
	"github.com/VeyrForge/codehelper/internal/taskstore"
	"github.com/spf13/cobra"
)

func agentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "agent",
		Short: "High-level orchestration helpers (plan, review, verify hints, finish gate)",
		Long:  agentLongHelp,
	}
	c.AddCommand(agentPlanCmd(), agentStepCmd(), agentReviewCmd(), agentVerifyCmd(), agentFinishCmd(), agentChatCmd())
	return c
}

func agentPlanCmd() *cobra.Command {
	var req, projectType, changedArea, title, mode string
	var save, quick, enrichLLM bool
	var noLLM bool
	c := &cobra.Command{
		Use:   "plan",
		Short: "Structured plan JSON with editable todos (optional --save)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(req) == "" {
				return fmt.Errorf("--request is required")
			}
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			pr, _ := profile.Read(root)
			reg, _ := registry.Load()
			tools := mcpsvc.NewLocalToolCaller(reg, root)
			enrich := enrichLLM && !noLLM
			out, err := plan.BuildEnriched(cmd.Context(), plan.Input{
				Request: req, ProjectType: projectType,
				ChangedArea: changedArea, RepoRoot: root, Quick: quick,
			}, plan.EnrichConfig{
				LLM: llm.ConfigFromEnv(), Tools: tools, EnrichLLM: enrich,
			})
			if err != nil {
				return err
			}
			resp := map[string]any{
				"user_request":    req,
				"project_profile": pr,
				"plan":            out.Plan,
				"todos":           out.Todos,
			}
			if save {
				st := taskstore.New(root)
				tTitle := strings.TrimSpace(title)
				if tTitle == "" {
					tTitle = req
				}
				t, err := st.Create(tTitle, req, mode)
				if err != nil {
					return err
				}
				t.Plan = out.Plan
				t.Todos = out.Todos
				t.DecisionPoints = out.DecisionPoints
				_ = st.AppendEvent(t, taskstore.Event{Type: "plan_created", Actor: "cli"})
				if err := st.Save(t); err != nil {
					return err
				}
				resp["task_id"] = t.ID
				resp["task"] = t
			}
			b, _ := json.MarshalIndent(resp, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	c.Flags().StringVar(&req, "request", "", "describe what you want to build or fix")
	c.Flags().StringVar(&projectType, "project-type", "", "override detected project type")
	c.Flags().StringVar(&changedArea, "changed-area", "", "frontend|backend|fullstack")
	c.Flags().StringVar(&title, "title", "", "task title when --save")
	c.Flags().StringVar(&mode, "mode", "", "task mode when --save")
	c.Flags().BoolVar(&save, "save", false, "persist plan and todos under .codehelper/tasks/")
	c.Flags().BoolVar(&quick, "quick", false, "pattern-only skeleton (skip repo intel and LLM)")
	c.Flags().BoolVar(&enrichLLM, "llm", true, "enrich plan with LLM when configured")
	c.Flags().BoolVar(&noLLM, "no-llm", false, "disable LLM enrichment")
	return c
}

func agentReviewCmd() *cobra.Command {
	var baseRef string
	c := &cobra.Command{
		Use:   "review",
		Short: "Run strict review_diff on current workspace diff",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return err
			}
			st, err := graph.Open(paths.DBPath(root))
			if err != nil {
				return err
			}
			defer st.Close()
			res, err := review.ReviewDiff(context.Background(), st, review.DiffRequest{
				RepoRoot: root, RepoName: filepath.Base(root), Base: baseRef,
				SeverityFloor: review.SeverityMedium, IncludeTests: true, IncludeSecurity: true,
				IncludePerformance: true, IncludeContracts: true,
			})
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	c.Flags().StringVar(&baseRef, "base", "HEAD~1", "git diff base")
	return c
}

func agentVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Print suggested verify commands from project_profile (no execution)",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			pr, err := profile.Read(root)
			if err != nil || pr == nil {
				if _, werr := profile.Write(root); werr != nil {
					return werr
				}
				pr, _ = profile.Read(root)
			}
			var cmds []string
			if pr != nil {
				cmds = append(cmds, pr.TestCommands...)
				cmds = append(cmds, pr.LintCommands...)
			}
			out := map[string]any{
				"verify_commands": cmds,
				"hint":            "Run MCP verify tool with repo_root and chosen commands; argv mode default.",
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
}

func agentFinishCmd() *cobra.Command {
	var base string
	var verifyRan, verifyAbstained bool
	var verifyReason string
	c := &cobra.Command{
		Use:   "finish",
		Short: "Run finish_check-style gate (release readiness + verify flags)",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			stg, err := graph.Open(paths.DBPath(root))
			if err != nil {
				return err
			}
			defer stg.Close()
			ctx := context.Background()
			repoName := filepath.Base(root)
			bref := base
			if bref == "" {
				bref = "HEAD~1"
			}
			rv, err := review.ReviewDiff(ctx, stg, review.DiffRequest{
				RepoRoot: root, RepoName: repoName, Base: bref, SeverityFloor: review.SeverityMedium,
				IncludeTests: true, IncludeSecurity: true, IncludePerformance: true, IncludeContracts: true,
			})
			if err != nil {
				return err
			}
			cg, err := review.ContractGuard(ctx, stg, root, repoName, bref)
			if err != nil {
				return err
			}
			tg, err := review.TestGap(ctx, stg, root, repoName, bref)
			if err != nil {
				return err
			}
			rr := review.BuildReleaseReadiness(rv, cg, tg, review.RiskScore(rv.Findings))
			out := review.BuildFinishCheck(review.FinishCheckInput{
				BaseRef:         bref,
				VerifyRan:       verifyRan,
				VerifyAbstained: verifyAbstained,
				VerifyReason:    verifyReason,
				Release:         rr,
			})
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	c.Flags().StringVar(&base, "base", "HEAD~1", "diff base")
	c.Flags().BoolVar(&verifyRan, "verify-ran", false, "verify was executed")
	c.Flags().BoolVar(&verifyAbstained, "verify-abstained", false, "verify abstained")
	c.Flags().StringVar(&verifyReason, "verify-reason", "", "reason if abstained")
	return c
}
