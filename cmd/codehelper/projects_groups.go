package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/contracts"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/spf13/cobra"
)

func projectsGroupCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "group",
		Short: "Manage workspace groups (sibling repos for cross-query)",
		Long: "Workspace groups link already-registered repos so agents can resolve " +
			"cross-repo import owners and sibling hints. See docs/WORKSPACE_GROUPS.md.",
	}
	c.AddCommand(projectsGroupListCmd(), projectsGroupShowCmd(), projectsGroupSetCmd(), projectsGroupRemoveCmd(), projectsGroupSnapshotCmd(), projectsGroupQueryCmd())
	return c
}

func projectsGroupListCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List workspace groups",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			groups := reg.ListGroups()
			if asJSON {
				b, _ := json.MarshalIndent(map[string]any{"groups": groups, "count": len(groups)}, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			if len(groups) == 0 {
				fmt.Println("No workspace groups. Create one with `codehelper projects group set <id> --member <repo>`.")
				return nil
			}
			for _, g := range groups {
				fmt.Printf("  %-16s %s  members=%s\n", g.ID, g.Name, strings.Join(g.Members, ","))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON")
	return c
}

func projectsGroupShowCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one workspace group and sibling entries",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			g, ok := reg.GetGroup(args[0])
			if !ok {
				return fmt.Errorf("workspace group %q not found", args[0])
			}
			var members []registry.Entry
			for _, m := range g.Members {
				if e, ok := reg.Get(m); ok {
					members = append(members, e)
				}
			}
			if asJSON {
				b, _ := json.MarshalIndent(map[string]any{"group": g, "entries": members}, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("%s (%s)\n", g.ID, g.Name)
			if g.Description != "" {
				fmt.Printf("  %s\n", g.Description)
			}
			for _, e := range members {
				fmt.Printf("  - %-16s %s  import_roots=%s\n", e.Name, e.RootPath, strings.Join(e.ImportRoots, ","))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON")
	return c
}

func projectsGroupSetCmd() *cobra.Command {
	var name, desc string
	var members []string
	c := &cobra.Command{
		Use:   "set <id>",
		Short: "Create or replace a workspace group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(members) == 0 {
				return fmt.Errorf("at least one --member is required")
			}
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			for _, m := range members {
				if _, ok := reg.Get(m); !ok {
					return fmt.Errorf("member %q is not registered; run codehelper init/analyze in that repo first", m)
				}
			}
			g := registry.WorkspaceGroup{
				ID:          args[0],
				Name:        name,
				Members:     members,
				Description: desc,
			}
			if err := reg.UpsertGroup(g); err != nil {
				return err
			}
			if err := reg.Save(); err != nil {
				return err
			}
			fmt.Printf("workspace group %q saved (%d members)\n", g.ID, len(members))
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "display name (defaults to id)")
	c.Flags().StringVar(&desc, "description", "", "optional description")
	c.Flags().StringArrayVar(&members, "member", nil, "registered repo name (repeatable)")
	return c
}

func projectsGroupRemoveCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "remove <id>",
		Short: "Delete a workspace group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			if _, ok := reg.GetGroup(args[0]); !ok {
				return fmt.Errorf("workspace group %q not found", args[0])
			}
			reg.RemoveGroup(args[0])
			if err := reg.Save(); err != nil {
				return err
			}
			fmt.Printf("removed workspace group %q\n", args[0])
			return nil
		},
	}
	return c
}

func projectsGroupSnapshotCmd() *cobra.Command {
	var outPath string
	var asJSON bool
	c := &cobra.Command{
		Use:   "snapshot <id>",
		Short: "Merge member graph summaries + cross-repo import edges for a workspace group",
		Long: "Builds per-member portable snapshots, resolves import→owner edges across " +
			"group siblings (and other registered owners), and writes one merged JSON. " +
			"Summary-only — not a shared verified graph.db. See docs/WORKSPACE_GROUPS.md.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			g, ok := reg.GetGroup(args[0])
			if !ok {
				return fmt.Errorf("workspace group %q not found", args[0])
			}
			var memberSnaps []graph.Snapshot
			imps := map[string][]string{}
			for _, name := range g.Members {
				e, ok := reg.Get(name)
				if !ok {
					continue
				}
				dbPath := paths.DBPath(e.RootPath)
				st, err := graph.Open(dbPath)
				if err != nil {
					return fmt.Errorf("open graph for %s: %w", name, err)
				}
				snap, err := graph.BuildSnapshot(context.Background(), st, graph.ExportOptions{
					RepoID:        e.Name,
					SchemaVersion: e.SchemaVer,
					ImportRoots:   e.ImportRoots,
					GroupIDs:      e.GroupIDs,
					IncludeProcs:  true,
					IncludeClus:   true,
				})
				if err != nil {
					_ = st.Close()
					return fmt.Errorf("snapshot %s: %w", name, err)
				}
				modPaths, err := st.DistinctImportModulePaths(context.Background(), e.Name, 2000)
				_ = st.Close()
				if err != nil {
					return fmt.Errorf("imports %s: %w", name, err)
				}
				memberSnaps = append(memberSnaps, snap)
				imps[e.Name] = modPaths
			}
			if len(memberSnaps) == 0 {
				return graph.ErrMergedEmpty
			}
			merged := graph.BuildMergedGroupSnapshot(context.Background(), g.ID, g.Name, memberSnaps, imps,
				func(fromRepo string, importPaths []string) []graph.CrossRepoEdgeSummary {
					raw := reg.ResolveCrossRepoEdges(fromRepo, importPaths)
					out := make([]graph.CrossRepoEdgeSummary, 0, len(raw))
					for _, e := range raw {
						out = append(out, graph.AdaptRegistryEdges(fromRepo, e.ImportPath, e.OwnerName, e.OwnerRoot, e.ViaRoot, e.SameGroup))
					}
					return out
				})
			if outPath == "" {
				home, err := paths.RegistryDir()
				if err != nil {
					return err
				}
				outPath = filepath.Join(home, "groups", g.ID, "merged_snapshot.json")
			}
			if err := graph.WriteMergedSnapshotJSON(outPath, merged); err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(map[string]any{"path": outPath, "snapshot": merged}, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("wrote %s (members=%d symbols=%d cross_edges=%d processes=%d)\n",
				outPath, len(merged.Members), merged.Symbols, len(merged.CrossRepoEdges), len(merged.Processes))
			return nil
		},
	}
	c.Flags().StringVar(&outPath, "out", "", "output path (default: ~/.codehelper/groups/<id>/merged_snapshot.json)")
	c.Flags().BoolVar(&asJSON, "json", false, "print merged snapshot JSON to stdout as well")
	return c
}

func projectsGroupQueryCmd() *cobra.Command {
	var asJSON bool
	var limit int
	var pathFilter string
	c := &cobra.Command{
		Use:   "query <id> <name>",
		Short: "Fan-out symbol name search across workspace-group member graphs",
		Long: "Opens each member's local graph.db and searches for symbols matching name " +
			"(FTS / path substring). Closest agent-queryable multi-root merge — not a shared " +
			"verified graph.db. Optional --path filters like MCP path= (Nest sample/ / Express lib/). " +
			"Prefer `projects group snapshot` for architecture summaries.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			g, ok := reg.GetGroup(args[0])
			if !ok {
				return fmt.Errorf("workspace group %q not found", args[0])
			}
			name := strings.TrimSpace(args[1])
			if name == "" {
				return fmt.Errorf("symbol name is required")
			}
			if limit <= 0 {
				limit = 20
			}
			var members []graph.GroupMemberDB
			for _, member := range g.Members {
				e, ok := reg.Get(member)
				if !ok {
					continue
				}
				st, err := graph.Open(paths.DBPath(e.RootPath))
				if err != nil {
					continue
				}
				members = append(members, graph.GroupMemberDB{Repo: e.Name, Store: st})
			}
			defer func() {
				for _, m := range members {
					if m.Store != nil {
						_ = m.Store.Close()
					}
				}
			}()
			result := graph.FanOutGroupQuery(context.Background(), g.ID, name, pathFilter, limit, members)
			hits := result.Hits
			if asJSON {
				payload := map[string]any{
					"group_id": result.GroupID, "query": result.Query, "hits": hits, "count": result.Count,
					"note": result.Note,
				}
				if result.Path != "" {
					payload["path"] = result.Path
				}
				if result.Ambiguous {
					payload["ambiguous"] = true
					payload["ambiguous_names"] = graph.GroupQueryAmbiguousNames(hits)
					payload["what_next"] = result.WhatNext
				}
				b, _ := json.MarshalIndent(payload, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			if len(hits) == 0 {
				if pathFilter != "" {
					fmt.Printf("No symbols matching %q path=%q in group %s\n", name, pathFilter, g.ID)
				} else {
					fmt.Printf("No symbols matching %q in group %s\n", name, g.ID)
				}
				return nil
			}
			fmt.Printf("%d hit(s) in group %s for %q", len(hits), g.ID, name)
			if pathFilter != "" {
				fmt.Printf(" path=%q", pathFilter)
			}
			fmt.Println(":")
			for _, h := range hits {
				fmt.Printf("  %-16s %-10s %-28s %s\n", h.Repo, h.Kind, h.Name, h.Path)
			}
			if result.Ambiguous {
				fmt.Println("note: duplicate names — pass --path= (MCP path=) on follow-up context/impact")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON")
	c.Flags().IntVar(&limit, "limit", 20, "max hits across all members")
	c.Flags().StringVar(&pathFilter, "path", "", "optional path substring filter (same idea as MCP path=)")
	return c
}

func projectsContractsCmd() *cobra.Command {
	var asJSON bool
	var cross bool
	var groupID string
	c := &cobra.Command{
		Use:   "contracts [path]",
		Short: "Discover OpenAPI, GraphQL, event, and Protobuf/gRPC contracts (optional cross-repo links)",
		Long: "Discovers OpenAPI/Swagger, GraphQL schemas, event contracts " +
			"(AsyncAPI / CloudEvents / simple event-name patterns), and Protobuf/gRPC " +
			"(.proto services, messages, RPCs, imports). " +
			"With --cross, also scans sibling registered repos (or --group) and reports shared keys.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return err
			}
			primary := contracts.DiscoverAll(root)
			var bundles []contracts.Bundle
			sameGroup := map[string]struct{}{}
			repoName := ""
			if cross || groupID != "" {
				reg, err := registry.Load()
				if err != nil {
					return err
				}
				if e, ok := reg.EntryForWorkspace(root); ok {
					repoName = e.Name
					primary = primary.AnnotateRepo(repoName)
				}
				bundles = append(bundles, primary)
				var siblings []registry.Entry
				if groupID != "" {
					g, ok := reg.GetGroup(groupID)
					if !ok {
						return fmt.Errorf("workspace group %q not found", groupID)
					}
					for _, m := range g.Members {
						if e, ok := reg.Get(m); ok {
							siblings = append(siblings, e)
							sameGroup[m] = struct{}{}
						}
					}
				} else if repoName != "" {
					siblings = reg.SiblingEntries(repoName)
					sameGroup[repoName] = struct{}{}
					for _, e := range siblings {
						sameGroup[e.Name] = struct{}{}
					}
				}
				seenRoot := map[string]struct{}{filepath.Clean(root): {}}
				for _, e := range siblings {
					r := filepath.Clean(e.RootPath)
					if _, dup := seenRoot[r]; dup {
						continue
					}
					seenRoot[r] = struct{}{}
					bundles = append(bundles, contracts.DiscoverAll(e.RootPath).AnnotateRepo(e.Name))
				}
			} else {
				bundles = []contracts.Bundle{primary}
			}
			links := contracts.LinkAcrossBundles(bundles, contracts.LinkOptions{SameGroupRepos: sameGroup})
			if asJSON {
				payload := map[string]any{
					"root":       root,
					"openapi":    primary.OpenAPI,
					"graphql":    primary.GraphQL,
					"events":     primary.Events,
					"protobuf":   primary.Protobuf,
					"count":      primary.Count(),
					"links":      links,
					"link_count": len(links),
				}
				if cross || groupID != "" {
					payload["bundles"] = bundles
				}
				b, _ := json.MarshalIndent(payload, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			if primary.Count() == 0 && len(links) == 0 {
				fmt.Printf("No OpenAPI / GraphQL / event / Protobuf contracts found under %s\n", root)
				return nil
			}
			fmt.Printf("%d contract file(s) under %s:\n", primary.Count(), root)
			for _, c := range primary.OpenAPI {
				rel, _ := filepath.Rel(root, c.Path)
				fmt.Printf("  openapi  %-24s format=%s title=%q paths=%d\n", rel, c.Format, c.Title, len(c.APIPaths))
			}
			for _, c := range primary.GraphQL {
				rel, _ := filepath.Rel(root, c.Path)
				fmt.Printf("  graphql  %-24s types=%d ops=%d\n", rel, len(c.Types), len(c.Operations))
			}
			for _, c := range primary.Events {
				rel, _ := filepath.Rel(root, c.Path)
				fmt.Printf("  event    %-24s source=%s channels=%d events=%d\n",
					rel, c.Source, len(c.Channels), len(c.EventNames))
			}
			for _, c := range primary.Protobuf {
				rel, _ := filepath.Rel(root, c.Path)
				fmt.Printf("  protobuf %-24s pkg=%s services=%d msgs=%d rpcs=%d imports=%d\n",
					rel, c.Package, len(c.Services), len(c.Messages), len(c.RPCs), len(c.Imports))
			}
			if len(links) > 0 {
				fmt.Printf("\n%d cross-contract link(s):\n", len(links))
				for _, l := range links {
					repos := make([]string, 0, len(l.Occurrences))
					for _, o := range l.Occurrences {
						tag := o.Repo
						if tag == "" {
							tag = filepath.Base(filepath.Dir(o.Path))
						}
						if o.SameGroup {
							tag += "*"
						}
						repos = append(repos, tag)
					}
					fmt.Printf("  %-14s %s  → %s\n", l.Kind, l.Key, strings.Join(repos, ", "))
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON")
	c.Flags().BoolVar(&cross, "cross", false, "scan sibling registered repos and link shared contract keys")
	c.Flags().StringVar(&groupID, "group", "", "scan all members of a workspace group (implies cross-repo linking)")
	return c
}

func projectsSnapshotCmd() *cobra.Command {
	var outPath string
	var asJSON bool
	c := &cobra.Command{
		Use:   "snapshot [path]",
		Short: "Export a portable graph summary snapshot (not full graph.db)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return err
			}
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			e, ok := reg.EntryForWorkspace(root)
			if !ok {
				return fmt.Errorf("no registry entry for %s; run codehelper init first", root)
			}
			dbPath := paths.DBPath(e.RootPath)
			st, err := graph.Open(dbPath)
			if err != nil {
				return fmt.Errorf("open graph: %w", err)
			}
			defer st.Close()
			snap, err := graph.BuildSnapshot(context.Background(), st, graph.ExportOptions{
				RepoID:        e.Name,
				SchemaVersion: e.SchemaVer,
				ImportRoots:   e.ImportRoots,
				GroupIDs:      e.GroupIDs,
				IncludeProcs:  true,
				IncludeClus:   true,
			})
			if err != nil {
				return err
			}
			if outPath == "" {
				outPath = filepath.Join(paths.RepoIndexDir(e.RootPath), "graph_snapshot.json")
			}
			if err := graph.WriteSnapshotJSON(outPath, snap); err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(map[string]any{"path": outPath, "snapshot": snap}, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("wrote %s (symbols=%d edges=%d files=%d)\n", outPath, snap.Symbols, snap.Edges, snap.Files)
			return nil
		},
	}
	c.Flags().StringVar(&outPath, "out", "", "output path (default: .codehelper/graph_snapshot.json)")
	c.Flags().BoolVar(&asJSON, "json", false, "print snapshot JSON to stdout as well")
	return c
}

func projectsCrossQueryCmd() *cobra.Command {
	var asJSON bool
	var imports []string
	c := &cobra.Command{
		Use:   "cross-query [repo]",
		Short: "Resolve import paths to sibling/other registered repos",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := registry.Load()
			if err != nil {
				return err
			}
			from := ""
			if len(args) > 0 {
				from = args[0]
			} else {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				if e, ok := reg.EntryForWorkspace(wd); ok {
					from = e.Name
				}
			}
			if from == "" {
				return fmt.Errorf("pass a registered repo name or run from an indexed workspace")
			}
			if len(imports) == 0 {
				return fmt.Errorf("pass at least one --import")
			}
			edges := reg.ResolveCrossRepoEdges(from, imports)
			sibs := reg.SiblingEntries(from)
			if asJSON {
				b, _ := json.MarshalIndent(map[string]any{
					"from": from, "siblings": sibs, "edges": edges,
				}, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Printf("from=%s siblings=%d edges=%d\n", from, len(sibs), len(edges))
			for _, e := range edges {
				tag := ""
				if e.SameGroup {
					tag = " [same-group]"
				}
				fmt.Printf("  %s → %s (%s)%s\n", e.ImportPath, e.OwnerName, e.ViaRoot, tag)
			}
			return nil
		},
	}
	c.Flags().StringArrayVar(&imports, "import", nil, "import/module path to resolve (repeatable)")
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable JSON")
	return c
}
