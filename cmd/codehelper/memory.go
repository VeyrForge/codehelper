package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/memory"
	"github.com/VeyrForge/codehelper/internal/taskstore"
	"github.com/spf13/cobra"
)

func memoryCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "memory",
		Short: "List, approve, or reject project memory proposals",
	}
	c.AddCommand(memoryListCmd(), memoryApproveCmd(), memoryRejectCmd())
	return c
}

func memoryListCmd() *cobra.Command {
	var taskID string
	c := &cobra.Command{
		Use:   "list",
		Short: "List memory entries or task proposals",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if strings.TrimSpace(taskID) != "" {
				t, err := taskstore.New(root).Load(taskID)
				if err != nil {
					return err
				}
				b, _ := json.MarshalIndent(map[string]any{"proposals": t.MemoryProposals}, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			hits, err := memory.Open(root).Search("", 20)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(map[string]any{"memory": hits}, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	c.Flags().StringVar(&taskID, "task-id", "", "Task id for pending proposals")
	return c
}

func memoryApproveCmd() *cobra.Command {
	var taskID, proposalID, text string
	c := &cobra.Command{
		Use:   "approve",
		Short: "Approve a memory proposal or add text directly",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			ms := memory.Open(root)
			if strings.TrimSpace(proposalID) != "" {
				t, err := taskstore.New(root).ResolveMemoryProposal(taskID, proposalID, "approved")
				if err != nil {
					return err
				}
				for _, mp := range t.MemoryProposals {
					if mp.ID == proposalID {
						_ = ms.AddDecision(mp.Text)
						break
					}
				}
				b, _ := json.MarshalIndent(t, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("--text or --proposal-id required")
			}
			_ = ms.AddDecision(text)
			fmt.Println(`{"ok":true}`)
			return nil
		},
	}
	c.Flags().StringVar(&taskID, "task-id", "", "Task id")
	c.Flags().StringVar(&proposalID, "proposal-id", "", "Proposal id")
	c.Flags().StringVar(&text, "text", "", "Memory text to save")
	return c
}

func memoryRejectCmd() *cobra.Command {
	var taskID, proposalID string
	c := &cobra.Command{
		Use:   "reject",
		Short: "Reject a pending memory proposal",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if strings.TrimSpace(proposalID) == "" {
				return fmt.Errorf("--proposal-id is required")
			}
			t, err := taskstore.New(root).ResolveMemoryProposal(taskID, proposalID, "rejected")
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(t, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	c.Flags().StringVar(&taskID, "task-id", "", "Task id")
	c.Flags().StringVar(&proposalID, "proposal-id", "", "Proposal id")
	return c
}
