package main

import "github.com/spf13/cobra"

func topLevelPlanCmd() *cobra.Command {
	c := agentPlanCmd()
	c.Use = "plan"
	c.Short = "Structured plan JSON with editable todos (alias for agent plan)"
	return c
}

func topLevelStepCmd() *cobra.Command {
	c := agentStepCmd()
	c.Use = "step"
	c.Short = "Execute one approved todo (alias for agent step)"
	return c
}
