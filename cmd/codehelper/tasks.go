package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/taskstore"
	"github.com/spf13/cobra"
)

func tasksCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "tasks",
		Short: "List tasks and show task history",
	}
	c.AddCommand(tasksListCmd(), tasksShowCmd(), tasksTimelineCmd())
	return c
}

func tasksListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List persisted tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			st := taskstore.New(root)
			ids, err := st.List()
			if err != nil {
				return err
			}
			tasks := make([]*taskstore.Task, 0, len(ids))
			for _, id := range ids {
				if t, err := st.Load(id); err == nil {
					tasks = append(tasks, t)
				}
			}
			b, _ := json.MarshalIndent(map[string]any{"tasks": tasks}, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	return c
}

func tasksShowCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "show <task-id>",
		Short: "Show one task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			t, err := taskstore.New(root).Load(strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(t, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	return c
}

func tasksTimelineCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "timeline <task-id>",
		Short: "Show task event timeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			t, err := taskstore.New(root).Load(strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(map[string]any{
				"task_id": t.ID, "events": t.Events, "messages": t.Messages,
			}, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	return c
}
