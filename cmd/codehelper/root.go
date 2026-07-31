package main

import (
	"os"

	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/spf13/cobra"
)

func rootCmd() *cobra.Command {
	var noTools, tools bool
	var track string
	r := &cobra.Command{
		Use:     "codehelper",
		Short:   "Code intelligence MCP and indexer for Cursor and other LLM agents",
		Long:    rootLongHelp,
		Version: version.Current(),
		Args:    cobra.MaximumNArgs(1),
		// Top-level convenience: `codehelper --no-tools` (and `--tools`/`--track`)
		// flip the current project's MCP config in one word, the same as
		// `codehelper config project --tools off`. With none of these set, keep the
		// old behavior of printing help.
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("no-tools") && !cmd.Flags().Changed("tools") && !cmd.Flags().Changed("track") {
				return cmd.Help()
			}
			edit := projectConfigEdit{path: argPath(args), track: track}
			switch {
			case noTools:
				edit.tools = "off" // baseline: tools off, tracking stays on (summary)
			case tools:
				edit.tools = "on"
			}
			return applyProjectConfig(os.Stdout, edit)
		},
	}
	r.Flags().BoolVar(&noTools, "no-tools", false, "baseline mode for the current project: disable codehelper tools but keep tracking usage")
	r.Flags().BoolVar(&tools, "tools", false, "re-enable codehelper tools for the current project")
	r.Flags().StringVar(&track, "track", "", "set telemetry for the current project: off|summary")
	r.AddCommand(setupCmd(), initCmd(), projectsCmd(), analyzeCmd(), enrichCmd(), upgradeCmd(), updateCmd(), repairCmd(), mcpCmd(), statusCmd(), cleanCmd(), watchCmd(), evalCmd(), modelEvalCmd(), versionCmd(), doctorCmd(), rulesCmd(), featurePatternsCmd(), profileCmd(), expandRequestCmd(), topLevelPlanCmd(), topLevelStepCmd(), runCmd(), agentCmd(), serveCmd(), memoryCmd(), tasksCmd(), configCmd(), docsCmd(), benchCmd(), webCmd(), hooksCmd(), docgenCmd(), usageCmd(), greenCmd(), hintsCmd(), browserCmd(), connectionsCmd(), orchestrationCmd())
	return r
}
