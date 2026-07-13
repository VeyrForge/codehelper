package main

import "github.com/spf13/cobra"

func runCmd() *cobra.Command {
	return newAgentChatCmd(
		"run [message]",
		"Run the agent chat loop (alias for `agent chat`)",
		"Primary terminal entry for the IDE-agnostic agent core. Equivalent to `codehelper agent chat`.",
	)
}
