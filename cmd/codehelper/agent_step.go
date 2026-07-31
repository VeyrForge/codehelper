package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/agent"
	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/mcpsvc"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/taskstore"
	"github.com/spf13/cobra"
)

func agentStepCmd() *cobra.Command {
	var taskID, todoID string
	var noVerify bool
	c := &cobra.Command{
		Use:   "step",
		Short: "Execute one approved todo from a saved task",
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID = strings.TrimSpace(taskID)
			todoID = strings.TrimSpace(todoID)
			if taskID == "" || todoID == "" {
				return fmt.Errorf("--task-id and --todo-id are required")
			}
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			llmCfg := llm.ConfigFromEnv()
			if !llmCfg.Ready() {
				return fmt.Errorf("LLM not configured (set CODEHELPER_LLM_* or ~/.codehelper/llm.json)")
			}
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			st := taskstore.New(root)
			task, err := st.Load(taskID)
			if err != nil {
				return err
			}
			execRes, task, err := agent.ExecuteTodo(context.Background(), agent.ExecuteTodoOptions{
				WorkspaceRoot: root,
				Task:          task,
				TodoID:        todoID,
				LLM:           llmCfg,
				Tools:         mcpsvc.NewLocalToolCaller(reg, root),
				Verify:        !noVerify,
				AutoVerify:    true,
				AutoReview:    true,
			})
			out := map[string]any{"execution": execRes, "task": task}
			if err != nil {
				out["error"] = err.Error()
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Println(string(b))
				return err
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	c.Flags().StringVar(&taskID, "task-id", "", "saved task id")
	c.Flags().StringVar(&todoID, "todo-id", "", "todo id to execute")
	c.Flags().BoolVar(&noVerify, "no-verify", false, "skip verification gate after execution")
	return c
}
