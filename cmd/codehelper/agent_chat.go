package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/agent"
	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/mcpsvc"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/spf13/cobra"
)

func agentChatCmd() *cobra.Command {
	return newAgentChatCmd(
		"chat [message]",
		"Chat with the Go agent loop in the terminal (proves IDE-agnostic core)",
		"Runs the same agent loop the IDE clients use, directly in the terminal. "+
			"Pass a single message as an argument for one-shot mode, or no argument for a REPL. "+
			"LLM settings: CODEHELPER_LLM_* env vars, or ~/.codehelper/llm.json (see `codehelper config llm`). "+
			"Env overrides file. API key: CODEHELPER_LLM_API_KEY or OPENAI_API_KEY.",
	)
}

func newAgentChatCmd(use, short, long string) *cobra.Command {
	var mode string
	var verbose bool
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			autoEnsureCodehelperGitignore(root)
			cfg := llm.ConfigFromEnv()
			if !cfg.Ready() {
				return fmt.Errorf("configure CODEHELPER_LLM_BASE_URL (or CODEHELPER_LLM_CHAT_URL), CODEHELPER_LLM_MODEL, and CODEHELPER_LLM_API_KEY")
			}
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			tools := mcpsvc.NewLocalToolCaller(reg, root)
			m := agent.NormalizeMode(mode)

			runTurn := func(prior []agent.Turn, text string) (*agent.Result, error) {
				hooks := agent.Hooks{
					OnAssistantToken: func(chunk string) { fmt.Print(chunk) },
					OnToolStart: func(name string, _ map[string]any) {
						fmt.Fprintf(os.Stderr, "\n[tool] %s…\n", name)
					},
				}
				var logFn func(string)
				if verbose {
					logFn = func(line string) { fmt.Fprintln(os.Stderr, "[log] "+line) }
				}
				res, err := agent.Run(cmd.Context(), agent.Options{
					Mode:          m,
					UserText:      text,
					PriorTurns:    prior,
					Hooks:         hooks,
					Log:           logFn,
					LLM:           cfg,
					Tools:         tools,
					WorkspaceRoot: root,

					PrefetchBroadAskEvidence: true,
				})
				if err != nil {
					return nil, err
				}
				if !res.FinalAlreadyStreamed {
					fmt.Print(res.Text)
				}
				fmt.Println()
				return res, nil
			}

			if len(args) == 1 {
				_, err := runTurn(nil, args[0])
				return err
			}

			fmt.Fprintf(os.Stderr, "codehelper agent chat — mode %s (Ctrl-D to exit)\n", m)
			var prior []agent.Turn
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
			for {
				fmt.Fprint(os.Stderr, "\n> ")
				if !scanner.Scan() {
					fmt.Fprintln(os.Stderr)
					return scanner.Err()
				}
				text := strings.TrimSpace(scanner.Text())
				if text == "" {
					continue
				}
				res, err := runTurn(prior, text)
				if err != nil {
					fmt.Fprintln(os.Stderr, "error: "+err.Error())
					continue
				}
				prior = append(prior,
					agent.Turn{Role: "user", Text: text},
					agent.Turn{Role: "assistant", Text: res.Text},
				)
			}
		},
	}
	c.Flags().StringVar(&mode, "mode", "ask", "ask|plan|agent (agent enables workspace writes)")
	c.Flags().BoolVar(&verbose, "verbose", false, "print orchestrator log lines to stderr")
	return c
}
