package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Event handlers in markup: onclick={fn} / on:click={fn} / on:click|preventDefault={fn}.
var svelteEventHandlerRe = regexp.MustCompile(`(?i)\bon:?([a-z][\w-]*)(?:\|[\w|]+)?=\{([A-Za-z_$][\w$]*)\}`)

// Runes / legacy reactive: $props $state $derived $effect $bindable.
var svelteRuneCallRe = regexp.MustCompile(`\$(props|state|derived|effect|bindable)\s*\(`)

// export let propName — legacy Svelte props.
var svelteExportLetRe = regexp.MustCompile(`(?m)^\s*export\s+let\s+([A-Za-z_$][\w$]*)`)

// ParseSvelte extracts symbols and call edges from Svelte SFC <script> blocks by
// reusing the TypeScript/JS extractor on each script body (with line offsets
// remapped to the .svelte file). Also indexes markup component refs and style
// class selectors so the SFC is not script-only.
func ParseSvelte(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	text := string(buf)

	comp, compID := sfcComponentSymbol(repoID, relPath, "svelte")
	if compID != "" {
		out.Symbols = append(out.Symbols, comp)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, comp.ID))
	}

	extractSFCScripts(ctx, out, repoID, relPath, text, "svelte")
	extractSFCMarkup(out, repoID, relPath, text, compID, "svelte")
	extractSFCStyle(out, repoID, relPath, text, compID, "svelte")
	extractSvelteEventsAndRunes(out, repoID, relPath, text, compID)
	return out, nil
}

// extractSvelteEventsAndRunes links markup event handlers to script symbols and
// indexes $props/$state/… plus export let as component→prop edges.
func extractSvelteEventsAndRunes(out *ParseResult, repoID, relPath, text, compID string) {
	if out == nil {
		return
	}
	markup := stripSFCBlocks(text)
	seenEvt := map[string]bool{}
	for _, m := range svelteEventHandlerRe.FindAllStringSubmatch(markup, -1) {
		if len(m) < 3 {
			continue
		}
		handler := m[2]
		if handler == "" || seenEvt[handler] {
			continue
		}
		seenEvt[handler] = true
		if compID != "" {
			tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, handler)
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, compID, tgt, "calls"),
				RepoID:     repoID,
				Kind:       types.RefKindCalls,
				SourceID:   compID,
				TargetID:   tgt,
				Confidence: 0.8,
			})
		}
	}
	// Runes inside script blocks.
	for _, sm := range sfcScriptRe.FindAllStringSubmatchIndex(text, -1) {
		if len(sm) < 6 {
			continue
		}
		bodyStart, bodyEnd := sm[4], sm[5]
		body := text[bodyStart:bodyEnd]
		lineOffset := 1 + strings.Count(text[:bodyStart], "\n")
		seenRune := map[string]bool{}
		for _, rm := range svelteRuneCallRe.FindAllStringSubmatchIndex(body, -1) {
			if len(rm) < 4 {
				continue
			}
			runeName := "$" + body[rm[2]:rm[3]]
			if seenRune[runeName] {
				continue
			}
			seenRune[runeName] = true
			line := lineOffset + strings.Count(body[:rm[2]], "\n")
			sym := symbol(repoID, relPath, runeName, types.SymbolKindFunction, line, line, "svelte", "rune", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if compID != "" {
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, compID, sym.ID, "calls"),
					RepoID:     repoID,
					Kind:       types.RefKindCalls,
					SourceID:   compID,
					TargetID:   sym.ID,
					Confidence: 0.85,
				})
			}
		}
		seenProp := map[string]bool{}
		for _, pm := range svelteExportLetRe.FindAllStringSubmatchIndex(body, -1) {
			if len(pm) < 4 {
				continue
			}
			prop := body[pm[2]:pm[3]]
			if prop == "" || seenProp[prop] {
				continue
			}
			seenProp[prop] = true
			line := lineOffset + strings.Count(body[:pm[2]], "\n")
			sym := symbol(repoID, relPath, prop, types.SymbolKindVariable, line, line, "svelte", "prop", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if compID != "" {
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, compID, sym.ID, "reads"),
					RepoID:     repoID,
					Kind:       types.RefKindReads,
					SourceID:   compID,
					TargetID:   sym.ID,
					Confidence: 0.8,
				})
			}
		}
	}
}

// Aliases kept for ast_query svelte mode and any external references.
var svelteScriptRe = sfcScriptRe
