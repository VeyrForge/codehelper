package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/VeyrForge/codehelper/internal/helpcatalog"
	"github.com/spf13/cobra"
)

func helpCmd(root *cobra.Command) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "help [topic]",
		Short: "Browse CLI + MCP catalog, or show help for a command",
		Long: `Browse the codehelper CLI and MCP tool catalog, or delegate to a
command's built-in --help when [topic] is a CLI command name.

  codehelper help                  Overview + how to look things up
  codehelper help tools [name]     MCP tools (use --main for the top 8)
  codehelper help cli [name]       CLI commands by group
  codehelper help group <name>     MCP tools in one group (e.g. graph, gates)
  codehelper help lookup <term>    Search tools and CLI by keyword
  codehelper help reference        Full docs/MCP_TOOLS.md when available
  codehelper help analyze          Same as codehelper analyze --help`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				topic := args[0]
				if sub, _, err := root.Find([]string{topic}); err == nil && sub != root && sub != cmd {
					return sub.Help()
				}
				return renderHelpTopic(os.Stdout, topic, asJSON)
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(helpcatalog.OverviewData())
			}
			helpcatalog.RenderOverview(os.Stdout)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output (overview or single topic)")
	c.AddCommand(helpToolsCmd(), helpCLICmd(), helpGroupCmd(), helpLookupCmd(), helpReferenceCmd())
	return c
}

func helpToolsCmd() *cobra.Command {
	var asJSON, mainOnly bool
	c := &cobra.Command{
		Use:   "tools [name]",
		Short: "List MCP tools or show one tool",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				t := helpcatalog.ToolByName(args[0])
				if t == nil {
					return fmt.Errorf("unknown MCP tool %q — try: codehelper help lookup %s", args[0], args[0])
				}
				if asJSON {
					return writeJSON(os.Stdout, t)
				}
				helpcatalog.RenderTools(os.Stdout, []helpcatalog.Tool{*t})
				return nil
			}
			tools := helpcatalog.Tools(helpcatalog.Filter{MainOnly: mainOnly})
			if asJSON {
				return writeJSON(os.Stdout, tools)
			}
			if mainOnly {
				fmt.Fprintf(os.Stdout, "Main MCP tools (%d):\n", len(tools))
			} else {
				fmt.Fprintf(os.Stdout, "MCP tools (%d):\n", len(tools))
			}
			helpcatalog.RenderTools(os.Stdout, tools)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	c.Flags().BoolVar(&mainOnly, "main", false, "only the 8 main MCP tools")
	return c
}

func helpCLICmd() *cobra.Command {
	var asJSON bool
	var group string
	c := &cobra.Command{
		Use:   "cli [name]",
		Short: "List CLI commands or show one command",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				cl := helpcatalog.CLIByName(args[0])
				if cl == nil {
					return fmt.Errorf("unknown CLI command %q — try: codehelper help %s", args[0], args[0])
				}
				if asJSON {
					return writeJSON(os.Stdout, cl)
				}
				helpcatalog.RenderCLIs(os.Stdout, []helpcatalog.CLI{*cl})
				return nil
			}
			cmds := helpcatalog.CLIs(helpcatalog.Filter{Group: group})
			if asJSON {
				return writeJSON(os.Stdout, cmds)
			}
			if group != "" {
				fmt.Fprintf(os.Stdout, "CLI commands [%s]:\n", group)
			} else {
				fmt.Fprintf(os.Stdout, "CLI commands:\n")
			}
			helpcatalog.RenderCLIs(os.Stdout, cmds)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	c.Flags().StringVar(&group, "group", "", "filter by CLI group (setup, index, mcp, …)")
	return c
}

func helpGroupCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "group <name>",
		Short: "List MCP tools in a catalog group",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			meta := helpcatalog.CatalogMeta()
			names, ok := meta.ByGroup[args[0]]
			if !ok {
				return fmt.Errorf("unknown MCP group %q — groups: %v", args[0], helpcatalog.Groups())
			}
			if asJSON {
				return writeJSON(os.Stdout, map[string]any{
					"group": args[0],
					"tools": names,
				})
			}
			helpcatalog.RenderGroup(os.Stdout, args[0], names)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return c
}

func helpLookupCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "lookup <term>",
		Short: "Search MCP tools and CLI commands by keyword",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			term := args[0]
			if asJSON {
				return writeJSON(os.Stdout, map[string]any{
					"query": term,
					"tools": helpcatalog.Tools(helpcatalog.Filter{Query: term}),
					"cli":   helpcatalog.CLIs(helpcatalog.Filter{Query: term}),
				})
			}
			helpcatalog.RenderLookup(os.Stdout, term)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return c
}

func helpReferenceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "reference",
		Short: "Print full docs/MCP_TOOLS.md (or synthesized catalog)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			wd, _ := os.Getwd()
			return helpcatalog.RenderReference(os.Stdout, wd)
		},
	}
	return c
}

func renderHelpTopic(w io.Writer, topic string, asJSON bool) error {
	kind, tool, cli, group := helpcatalog.ResolveTopic(topic)
	switch kind {
	case "tool":
		if asJSON {
			return writeJSON(w, tool)
		}
		helpcatalog.RenderTools(w, []helpcatalog.Tool{*tool})
		return nil
	case "cli":
		if asJSON {
			return writeJSON(w, cli)
		}
		helpcatalog.RenderCLIs(w, []helpcatalog.CLI{*cli})
		return nil
	case "group":
		if asJSON {
			return writeJSON(w, map[string]any{"group": topic, "tools": group})
		}
		helpcatalog.RenderGroup(w, topic, group)
		return nil
	default:
		return fmt.Errorf("no catalog match for %q — try: codehelper help lookup %s", topic, topic)
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
