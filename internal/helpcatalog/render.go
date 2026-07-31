package helpcatalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RenderOverview prints the default help overview.
func RenderOverview(w fmtWriter) {
	o := OverviewData()
	fmt.Fprintf(w, "Codehelper — local repo intelligence for AI coding assistants\n\n")
	fmt.Fprintf(w, "Quick start:\n  codehelper setup\n  cd your-repo && codehelper init\n\n")
	fmt.Fprintf(w, "Catalog (%d MCP tools, %d CLI commands):\n", o.ToolCount, len(cliRefs))
	fmt.Fprintf(w, "  codehelper help tools [name]     MCP tools (add --main for the top 8)\n")
	fmt.Fprintf(w, "  codehelper help cli [name]       CLI commands\n")
	fmt.Fprintf(w, "  codehelper help group <name>     MCP tools in one group\n")
	fmt.Fprintf(w, "  codehelper help lookup <term>    Search tools + CLI by keyword\n")
	fmt.Fprintf(w, "  codehelper help reference        Full docs/MCP_TOOLS.md when available\n\n")
	fmt.Fprintf(w, "MCP param keys: %s\n\n", o.ParamKeys)
	fmt.Fprintf(w, "Main MCP tools: %s\n\n", strings.Join(o.MainTools, ", "))
	fmt.Fprintf(w, "MCP groups: %s\n", strings.Join(Groups(), ", "))
	fmt.Fprintf(w, "CLI groups: %s\n", strings.Join(sortedKeys(o.CLIGroups), ", "))
	fmt.Fprintf(w, "\nMore: %s · AGENTS.md · README.md\n", o.ReferencePath)
}

// RenderTools prints MCP tool catalog entries.
func RenderTools(w fmtWriter, tools []Tool) {
	if len(tools) == 0 {
		fmt.Fprintln(w, "no MCP tools matched")
		return
	}
	if len(tools) == 1 {
		renderOneTool(w, tools[0])
		return
	}
	cur := ""
	for _, t := range tools {
		if t.Group != cur {
			cur = t.Group
			fmt.Fprintf(w, "\n[%s]\n", cur)
		}
		main := ""
		if t.Main {
			main = " *"
		}
		fmt.Fprintf(w, "  %-22s%s %s\n", t.Name, main, t.Summary)
		if t.Params != "" {
			fmt.Fprintf(w, "    params: %s\n", t.Params)
		}
	}
	fmt.Fprintln(w, "\n(* = main tool · full reference: codehelper help reference)")
}

// RenderCLIs prints CLI command catalog entries.
func RenderCLIs(w fmtWriter, cmds []CLI) {
	if len(cmds) == 0 {
		fmt.Fprintln(w, "no CLI commands matched")
		return
	}
	if len(cmds) == 1 {
		renderOneCLI(w, cmds[0])
		return
	}
	cur := ""
	for _, c := range cmds {
		if c.Group != cur {
			cur = c.Group
			fmt.Fprintf(w, "\n[%s]\n", cur)
		}
		fmt.Fprintf(w, "  %-18s %s\n", c.Name, c.Summary)
		if c.Related != "" {
			fmt.Fprintf(w, "    MCP: %s\n", c.Related)
		}
	}
}

// RenderGroup prints tools in one MCP group.
func RenderGroup(w fmtWriter, group string, names []string) {
	fmt.Fprintf(w, "MCP group %q (%d tools):\n", group, len(names))
	tools := make([]Tool, 0, len(names))
	for _, n := range names {
		if t := ToolByName(n); t != nil {
			tools = append(tools, *t)
		} else {
			tools = append(tools, Tool{Name: n, Group: group, Summary: "(catalog entry pending)"})
		}
	}
	RenderTools(w, tools)
}

// RenderLookup prints combined search results.
func RenderLookup(w fmtWriter, query string) {
	tools := Tools(Filter{Query: query})
	cmds := CLIs(Filter{Query: query})
	if len(tools) == 0 && len(cmds) == 0 {
		fmt.Fprintf(w, "no matches for %q\n\n", query)
		fmt.Fprintln(w, "Try: codehelper help tools · codehelper help cli · codehelper help group graph")
		return
	}
	if len(tools) > 0 {
		fmt.Fprintf(w, "MCP tools matching %q:\n", query)
		RenderTools(w, tools)
	}
	if len(cmds) > 0 {
		if len(tools) > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "CLI commands matching %q:\n", query)
		RenderCLIs(w, cmds)
	}
}

// RenderReference prints docs/MCP_TOOLS.md from disk or a synthesized fallback.
func RenderReference(w fmtWriter, searchDirs ...string) error {
	for _, dir := range searchDirs {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, "docs", "MCP_TOOLS.md")
		b, err := os.ReadFile(p)
		if err == nil && len(b) > 0 {
			fmt.Fprint(w, string(b))
			return nil
		}
	}
	fmt.Fprintln(w, "docs/MCP_TOOLS.md not found — synthesized catalog:")
	fmt.Fprintln(w)
	meta := CatalogMeta()
	fmt.Fprintf(w, "# MCP tools (%d)\n\n", meta.Count)
	fmt.Fprintf(w, "Param keys: %s\n\n", meta.ParamKeys)
	for _, g := range Groups() {
		names := meta.ByGroup[g]
		fmt.Fprintf(w, "## %s\n\n", g)
		for _, n := range names {
			if t := ToolByName(n); t != nil {
				fmt.Fprintf(w, "- **%s** — %s", t.Name, t.Summary)
				if t.Params != "" {
					fmt.Fprintf(w, " (`%s`)", t.Params)
				}
				fmt.Fprintln(w)
			}
		}
		fmt.Fprintln(w)
	}
	if len(meta.CLIONly) > 0 {
		fmt.Fprintf(w, "## CLI-only\n\n%s\n", strings.Join(meta.CLIONly, ", "))
	}
	return nil
}

// WriteProjectReference writes a synthesized MCP tools catalog into the project's
// .codehelper/ directory (gitignored) so agents always have an on-disk reference
// even when docs/MCP_TOOLS.md is absent from the repo.
func WriteProjectReference(repoRoot string) error {
	if repoRoot == "" {
		return fmt.Errorf("repo root is required")
	}
	dir := filepath.Join(repoRoot, ".codehelper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var buf strings.Builder
	meta := CatalogMeta()
	fmt.Fprintf(&buf, "# Codehelper MCP tools — project reference\n\n")
	fmt.Fprintf(&buf, "**Tool count:** %d (generated from `internal/mcpsvc/toolcatalog.go`).\n\n", meta.Count)
	fmt.Fprintf(&buf, "**Param keys:** %s\n\n", meta.ParamKeys)
	fmt.Fprintf(&buf, "This file is written by `codehelper init` / setup. Prefer `project_context` for live routing.\n\n")
	for _, g := range Groups() {
		names := meta.ByGroup[g]
		if len(names) == 0 {
			continue
		}
		fmt.Fprintf(&buf, "## %s\n\n", g)
		for _, n := range names {
			if t := ToolByName(n); t != nil {
				fmt.Fprintf(&buf, "- **%s** — %s", t.Name, t.Summary)
				if t.Params != "" {
					fmt.Fprintf(&buf, " (`%s`)", t.Params)
				}
				fmt.Fprintln(&buf)
			} else {
				fmt.Fprintf(&buf, "- **%s**\n", n)
			}
		}
		fmt.Fprintln(&buf)
	}
	return os.WriteFile(filepath.Join(dir, "MCP_TOOLS.md"), []byte(buf.String()), 0o644)
}

func renderOneTool(w fmtWriter, t Tool) {
	fmt.Fprintf(w, "MCP tool: %s\n", t.Name)
	if t.Main {
		fmt.Fprintln(w, "main tool: yes")
	}
	fmt.Fprintf(w, "group: %s\n", t.Group)
	fmt.Fprintln(w, "\n"+t.Summary)
	if t.Params != "" {
		fmt.Fprintf(w, "\nparams: %s\n", t.Params)
	}
	fmt.Fprintf(w, "\nSee also: codehelper help group %s · codehelper help reference\n", t.Group)
}

func renderOneCLI(w fmtWriter, c CLI) {
	fmt.Fprintf(w, "CLI command: codehelper %s\n", c.Name)
	fmt.Fprintf(w, "group: %s\n", c.Group)
	fmt.Fprintln(w, "\n"+c.Summary)
	if c.Related != "" {
		fmt.Fprintf(w, "\nrelated MCP: %s\n", c.Related)
	}
	fmt.Fprintf(w, "\nDetail: codehelper %s --help\n", c.Name)
}

type fmtWriter interface {
	Write([]byte) (int, error)
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}
