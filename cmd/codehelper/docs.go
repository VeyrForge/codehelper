package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/docs"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/research"
	"github.com/spf13/cobra"
)

func docsCmd() *cobra.Command {
	var (
		topic     string
		version   string
		maxTokens int
		network   bool
		noCache   bool
		asJSON    bool
		listDeps  bool
		repoPath  string
	)
	c := &cobra.Command{
		Use:   "docs <library> [--topic ...]",
		Short: "Fetch up-to-date official docs for a library (local-first Context7 alternative)",
		Long: "Resolve the version this project pins from its manifests and fetch version-correct\n" +
			"official documentation, preferring the llms.txt/llms-full.txt standard before HTML.\n" +
			"Network access is privacy-gated: pass --network, set research.enabled in\n" +
			".codehelper/learning.json, or export CODEHELPER_DOCS_NETWORK=1.\n\n" +
			"For codehelper's own MCP/CLI tool catalog (not third-party library docs), use:\n" +
			"  codehelper help · codehelper help reference\n\n" +
			"The doc catalog is extensible without recompiling: add or override entries in\n" +
			"~/.codehelper/docs-registry.json (global) or <repo>/.codehelper/docs-overrides.json\n" +
			"(project, takes precedence). Each is a JSON array of\n" +
			`{"match":["name","alias"],"doc_base":"https://...","trust":0-10,"ecosystem":"go|npm|pip|composer|cargo"}.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := repoPath
			if root == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				root = wd
			}
			root, _ = filepath.Abs(root)

			if listDeps {
				deps := docs.ListDependencies(root)
				if asJSON {
					b, _ := json.MarshalIndent(deps, "", "  ")
					fmt.Println(string(b))
					return nil
				}
				if len(deps) == 0 {
					fmt.Println("no manifest dependencies found in", root)
					return nil
				}
				for _, d := range deps {
					dev := ""
					if d.Dev {
						dev = " (dev)"
					}
					fmt.Printf("%-12s %-30s %s%s\n", d.Ecosystem, d.Name, d.Version, dev)
				}
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("library name is required (or use --list-deps)")
			}
			library := args[0]

			allow := network || research.NetworkEnabled(root) || os.Getenv("CODEHELPER_DOCS_NETWORK") == "1"
			// Allow any public HTTPS host: doc sources are resolved dynamically
			// (overrides, registry metadata, direct URLs), so a pre-computed host
			// allowlist would block user-added and long-tail sources. netguard in
			// the fetcher denies loopback/RFC1918/cloud-metadata for SSRF safety.
			eng := &docs.Engine{
				Fetcher: docs.NewHTTPFetcher(12*time.Second, nil),
				Cache:   docs.NewCache(filepath.Join(paths.RepoIndexDir(root), "docs-cache"), 24*time.Hour),
			}
			res, err := eng.Lookup(cmd.Context(), docs.LookupOptions{
				RepoRoot:    root,
				Library:     library,
				Version:     version,
				Topic:       topic,
				MaxTokens:   maxTokens,
				Network:     allow,
				FollowLinks: true,
				NoCache:     noCache,
			})
			if err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(res, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			printDocsResult(res)
			return nil
		},
	}
	c.Flags().StringVar(&topic, "topic", "", "focus the docs on a topic")
	c.Flags().StringVar(&version, "version", "", "override version (default: detected from manifest)")
	c.Flags().IntVar(&maxTokens, "max-tokens", 5000, "approx token budget for returned docs")
	c.Flags().BoolVar(&network, "network", false, "allow network fetch for this call")
	c.Flags().BoolVar(&noCache, "no-cache", false, "bypass the on-disk docs cache")
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	c.Flags().BoolVar(&listDeps, "list-deps", false, "list this project's manifest dependencies and exit")
	c.Flags().StringVar(&repoPath, "path", "", "project root (default: current directory)")
	c.AddCommand(docsAddCmd(), docsListCmd(), docsRemoveCmd())
	return c
}

// docsAddCmd lets a user register a documentation source for a library (or any
// docs/API-reference site) that the curated index is missing, without editing
// JSON by hand. Writes to the global catalog by default, or the project catalog
// with --project.
func docsAddCmd() *cobra.Command {
	var (
		aliases   string
		llmsTxt   string
		llmsFull  string
		trust     int
		ecosystem string
		project   bool
		repoPath  string
	)
	c := &cobra.Command{
		Use:   "add <name> <doc-base-url>",
		Short: "Register a missing documentation source (framework, library, or API reference)",
		Long: "Add or replace a documentation source so `docs <name>` resolves to it.\n" +
			"Writes the global catalog (~/.codehelper/docs-registry.json) by default, or the\n" +
			"project catalog (.codehelper/docs-overrides.json) with --project. Re-adding the\n" +
			"same name replaces the prior entry. doc-base-url should be the docs site root;\n" +
			"llms.txt/llms-full.txt are probed automatically unless you pass them explicitly.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, docBase := args[0], args[1]
			match := append([]string{name}, splitCSV(aliases)...)
			o := docs.Override{
				Match: match, DocBase: docBase, LLMSTxt: llmsTxt,
				LLMSFull: llmsFull, Trust: trust, Ecosystem: ecosystem,
			}
			path, err := docsCatalogPath(project, repoPath)
			if err != nil {
				return err
			}
			if err := docs.AddOverride(path, o); err != nil {
				return err
			}
			scope := "global"
			if project {
				scope = "project"
			}
			fmt.Printf("added %s -> %s (%s catalog: %s)\n", name, strings.TrimRight(docBase, "/"), scope, path)
			fmt.Printf("try: codehelper docs %s --network\n", name)
			return nil
		},
	}
	c.Flags().StringVar(&aliases, "alias", "", "comma-separated extra names that resolve to this source (e.g. package + import path)")
	c.Flags().StringVar(&llmsTxt, "llms-txt", "", "explicit llms.txt URL (default: probe <doc-base>/llms.txt)")
	c.Flags().StringVar(&llmsFull, "llms-full", "", "explicit llms-full.txt URL")
	c.Flags().IntVar(&trust, "trust", 0, "curation confidence 0-10 (default 7)")
	c.Flags().StringVar(&ecosystem, "ecosystem", "", "go|npm|pip|composer|cargo (optional hint)")
	c.Flags().BoolVar(&project, "project", false, "write the project catalog instead of the global one")
	c.Flags().StringVar(&repoPath, "path", "", "project root for --project (default: current directory)")
	return c
}

// docsListCmd shows the user-extensible catalog entries (global + project) so a
// user can see what they have added and where.
func docsListCmd() *cobra.Command {
	var repoPath string
	c := &cobra.Command{
		Use:   "list",
		Short: "List user-added documentation sources (global + project)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := resolveRoot(repoPath)
			printed := false
			if gp, err := docs.GlobalOverridePath(); err == nil {
				printed = printCatalog("global", gp) || printed
			}
			if pp, err := docs.ProjectOverridePath(root); err == nil {
				printed = printCatalog("project", pp) || printed
			}
			if !printed {
				fmt.Println("no user-added doc sources yet. Add one with: codehelper docs add <name> <url>")
			}
			return nil
		},
	}
	c.Flags().StringVar(&repoPath, "path", "", "project root (default: current directory)")
	return c
}

// docsRemoveCmd deletes a user-added documentation source by name.
func docsRemoveCmd() *cobra.Command {
	var (
		project  bool
		repoPath string
	)
	c := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a user-added documentation source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := docsCatalogPath(project, repoPath)
			if err != nil {
				return err
			}
			n, err := docs.RemoveOverride(path, args[0])
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Printf("no entry named %q in %s\n", args[0], path)
				return nil
			}
			fmt.Printf("removed %d entr%s for %q from %s\n", n, plural(n), args[0], path)
			return nil
		},
	}
	c.Flags().BoolVar(&project, "project", false, "operate on the project catalog instead of the global one")
	c.Flags().StringVar(&repoPath, "path", "", "project root for --project (default: current directory)")
	return c
}

func docsCatalogPath(project bool, repoPath string) (string, error) {
	if project {
		return docs.ProjectOverridePath(resolveRoot(repoPath))
	}
	return docs.GlobalOverridePath()
}

func printCatalog(scope, path string) bool {
	entries, err := docs.ReadOverridesFile(path)
	if err != nil || len(entries) == 0 {
		return false
	}
	fmt.Printf("%s catalog (%s):\n", scope, path)
	for _, e := range entries {
		fmt.Printf("  %-24s -> %s\n", strings.Join(e.Match, ", "), e.DocBase)
	}
	return true
}

func resolveRoot(repoPath string) string {
	if strings.TrimSpace(repoPath) != "" {
		abs, _ := filepath.Abs(repoPath)
		return abs
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	abs, _ := filepath.Abs(wd)
	return abs
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func printDocsResult(res *docs.Result) {
	fmt.Printf("library: %s", res.Library)
	if res.Version != "" {
		fmt.Printf("@%s", res.Version)
	}
	fmt.Printf("  [%s, trust %d]\n", res.Resolved.Origin, res.Resolved.TrustScore)
	if res.Topic != "" {
		fmt.Println("topic:", res.Topic)
	}
	if res.Offline {
		fmt.Println("\noffline — resolved doc sources (enable network to fetch):")
		for _, s := range res.Resolved.Sources {
			fmt.Printf("  - [%s] %s\n", s.Kind, s.URL)
		}
		if res.Note != "" {
			fmt.Println("\nnote:", res.Note)
		}
		fmt.Println("\ntip: add or override doc sources in ~/.codehelper/docs-registry.json (global) or")
		fmt.Println("     .codehelper/docs-overrides.json (project, takes precedence).")
		return
	}
	if res.SourceUsed != "" {
		fmt.Printf("source: %s (%s)  ~%d tokens\n", res.SourceUsed, res.SourceKind, res.Tokens)
	}
	if res.FromCache {
		fmt.Println("(from cache)")
	}
	if res.Note != "" {
		fmt.Println("note:", res.Note)
	}
	fmt.Println()
	for i, c := range res.Chunks {
		if c.Heading != "" {
			fmt.Printf("## %s\n", c.Heading)
		}
		fmt.Println(c.Text)
		if i < len(res.Chunks)-1 {
			fmt.Println()
		}
	}
}
