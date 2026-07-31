package mcpsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/retrieval"
)

// runWorkspaceGroupQuery fans out symbol search across workspace-group member graphs.
func runWorkspaceGroupQuery(ctx context.Context, reg *registry.Registry, groupID, name, pathFilter string, limit int) (graph.GroupQueryResult, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return graph.GroupQueryResult{}, fmt.Errorf("group id is required — pass group=<id> from project_context workspace_groups[]")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return graph.GroupQueryResult{}, fmt.Errorf("query must not be empty for group fan-out")
	}
	if reg == nil {
		return graph.GroupQueryResult{}, fmt.Errorf("registry unavailable")
	}
	g, ok := reg.GetGroup(groupID)
	if !ok {
		_ = reg.Reload()
		g, ok = reg.GetGroup(groupID)
	}
	if !ok {
		return graph.GroupQueryResult{}, fmt.Errorf("workspace group %q not found — list with `codehelper projects group list`", groupID)
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
	if len(members) == 0 {
		return graph.GroupQueryResult{}, fmt.Errorf("workspace group %q has no indexed members", groupID)
	}
	return graph.FanOutGroupQuery(ctx, groupID, name, pathFilter, limit, members), nil
}

func enrichGroupQueryResponse(out *queryToolResponse, gq graph.GroupQueryResult) {
	out.GroupQuery = &gq
	out.RecommendedNextTools = []string{"context", "impact", "change_kit"}
	if gq.Ambiguous {
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("ambiguous group hits for %q — pass path= and repo= from group_hits on context/impact", gq.Query))
	}
	if len(gq.Hits) == 0 {
		out.RetrievalNote = fmt.Sprintf(
			"No symbols matching %q in workspace group %q. Next: widen the name, drop path=, or run `codehelper projects group query %s %s`.",
			gq.Query, gq.GroupID, gq.GroupID, gq.Query)
		out.Confidence = 0.5
		return
	}
	paths := make([]string, 0, len(gq.Hits))
	for _, h := range gq.Hits {
		paths = append(paths, h.Path)
	}
	out.EvidencePaths = dedupePathsCap(paths, 8)
	out.Confidence = 0.84
	if gq.WhatNext != "" {
		out.RetrievalNote = gq.Note
	}
}

func filterHitsByPath(hits []retrieval.RankedSymbol, pathFilter string) []retrieval.RankedSymbol {
	pathFilter = strings.TrimSpace(pathFilter)
	if pathFilter == "" {
		return hits
	}
	out := make([]retrieval.RankedSymbol, 0, len(hits))
	for _, h := range hits {
		if graph.PathMatches(h.Symbol.Path, pathFilter) {
			out = append(out, h)
		}
	}
	return out
}
