package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Matches <script>…</script> including lang="ts" / context="module".
var svelteScriptRe = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)

// Matches <style>…</style> blocks for CSS class symbols.
var svelteStyleRe = regexp.MustCompile(`(?is)<style([^>]*)>(.*?)</style>`)

// Capitalized component tags in markup (Svelte components, not HTML).
var svelteComponentTagRe = regexp.MustCompile(`</?([A-Z][A-Za-z0-9_]*)\b`)

// CSS class selectors inside <style>.
var svelteCSSClassRe = regexp.MustCompile(`\.([A-Za-z_][\w-]*)\s*[{,:]`)

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

	// Component symbol: basename without extension (e.g. Button.svelte → Button).
	base := strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
	var compID string
	if base != "" && base != "." {
		comp := symbol(repoID, relPath, base, types.SymbolKindClass, 1, 1, "svelte", "component", "")
		compID = comp.ID
		out.Symbols = append(out.Symbols, comp)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, comp.ID))
	}

	for _, m := range svelteScriptRe.FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 6 {
			continue
		}
		attrs := text[m[2]:m[3]]
		bodyStart, bodyEnd := m[4], m[5]
		body := text[bodyStart:bodyEnd]
		if strings.TrimSpace(body) == "" {
			continue
		}
		// Byte offset → 1-based line of the first body character.
		lineOffset := 1 + strings.Count(text[:bodyStart], "\n")
		scriptPath := relPath
		if svelteScriptIsTS(attrs) {
			scriptPath = relPath + ".ts"
		} else {
			scriptPath = relPath + ".js"
		}
		part, err := ParseTypeScript(ctx, repoID, scriptPath, []byte(body))
		if err != nil || part == nil {
			continue
		}
		mergeSvelteScript(out, part, repoID, relPath, lineOffset)
	}

	extractSvelteMarkup(out, repoID, relPath, text, compID)
	extractSvelteStyle(out, repoID, relPath, text, compID)
	extractSvelteEventsAndRunes(out, repoID, relPath, text, compID)
	return out, nil
}

func svelteScriptIsTS(attrs string) bool {
	a := strings.ToLower(attrs)
	return strings.Contains(a, `lang="ts"`) || strings.Contains(a, `lang='ts'`) ||
		strings.Contains(a, "lang=ts") || strings.Contains(a, `lang="typescript"`) ||
		strings.Contains(a, `lang='typescript'`)
}

// mergeSvelteScript remaps ParseTypeScript results from a synthetic script path
// onto the real .svelte path, shifting line numbers by lineOffset-1.
func mergeSvelteScript(dst, src *ParseResult, repoID, realPath string, lineOffset int) {
	if src == nil {
		return
	}
	delta := lineOffset - 1
	if delta < 0 {
		delta = 0
	}
	idMap := map[string]string{}

	for _, s := range src.Symbols {
		ls := s.LineStart + delta
		le := s.LineEnd + delta
		lang := s.Language
		if lang == "" || lang == "typescript" || lang == "javascript" {
			lang = "svelte"
		}
		ns := symbol(repoID, realPath, s.Name, s.Kind, ls, le, lang, s.Signature, "")
		// Preserve parent linkage when both ends remapped later.
		idMap[s.ID] = ns.ID
		dst.Symbols = append(dst.Symbols, ns)
		dst.Edges = append(dst.Edges, containsEdge(repoID, realPath, ns.ID))
	}

	remapID := func(id string) string {
		if id == "" {
			return id
		}
		if n, ok := idMap[id]; ok {
			return n
		}
		// Synthetic file:/mod: ids from the script path → real file.
		if strings.HasPrefix(id, "file:") {
			return FileNodeID(repoID, realPath)
		}
		if strings.HasPrefix(id, "symref:") {
			// symref:repo:scriptPath:name → symref:repo:realPath:name
			parts := strings.SplitN(id, ":", 4)
			if len(parts) == 4 {
				return fmt.Sprintf("symref:%s:%s:%s", repoID, realPath, parts[3])
			}
		}
		if strings.HasPrefix(id, "mod:") {
			return id
		}
		return id
	}

	for _, e := range src.Edges {
		if e.Kind == types.RefKindContains {
			// Already emitted contains for remapped symbols.
			continue
		}
		srcID := remapID(e.SourceID)
		dstID := remapID(e.TargetID)
		dst.Edges = append(dst.Edges, types.Reference{
			ID:         edgeID(repoID, srcID, dstID, string(e.Kind)),
			RepoID:     repoID,
			Kind:       e.Kind,
			SourceID:   srcID,
			TargetID:   dstID,
			Confidence: e.Confidence,
		})
	}
	dst.Imports = append(dst.Imports, src.Imports...)
}

// extractSvelteMarkup emits reads from the component to capitalized child
// component tags found outside <script>/<style> (markup template).
func extractSvelteMarkup(out *ParseResult, repoID, relPath, text, compID string) {
	if out == nil || compID == "" {
		return
	}
	markup := stripSvelteBlocks(text)
	seen := map[string]bool{}
	base := strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
	for _, m := range svelteComponentTagRe.FindAllStringSubmatch(markup, -1) {
		if len(m) < 2 {
			continue
		}
		name := m[1]
		if name == "" || name == base || seen[name] {
			continue
		}
		// Skip svelte: special elements (svelte:head, svelte:component, …).
		if strings.EqualFold(name, "svelte") {
			continue
		}
		seen[name] = true
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, compID, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   compID,
			TargetID:   tgt,
			Confidence: 0.75,
		})
	}
}

// extractSvelteStyle indexes CSS class selectors as symbols and contains edges
// under the component, so style hubs are path-scoped to the SFC (not global CSS).
func extractSvelteStyle(out *ParseResult, repoID, relPath, text, compID string) {
	if out == nil {
		return
	}
	for _, m := range svelteStyleRe.FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 6 {
			continue
		}
		bodyStart, bodyEnd := m[4], m[5]
		body := text[bodyStart:bodyEnd]
		lineOffset := 1 + strings.Count(text[:bodyStart], "\n")
		seen := map[string]bool{}
		for _, cm := range svelteCSSClassRe.FindAllStringSubmatchIndex(body, -1) {
			if len(cm) < 4 {
				continue
			}
			name := body[cm[2]:cm[3]]
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			line := lineOffset + strings.Count(body[:cm[2]], "\n")
			sym := symbol(repoID, relPath, "."+name, types.SymbolKindVariable, line, line, "svelte", "css-class", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if compID != "" {
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, compID, sym.ID, "reads"),
					RepoID:     repoID,
					Kind:       types.RefKindReads,
					SourceID:   compID,
					TargetID:   sym.ID,
					Confidence: 0.7,
				})
			}
		}
	}
}

// stripSvelteBlocks removes script/style bodies so markup regexes do not match
// TypeScript class names or CSS selectors inside those blocks.
func stripSvelteBlocks(text string) string {
	out := svelteScriptRe.ReplaceAllString(text, "")
	out = svelteStyleRe.ReplaceAllString(out, "")
	return out
}

// extractSvelteEventsAndRunes links markup event handlers to script symbols and
// indexes $props/$state/… plus export let as component→prop edges.
func extractSvelteEventsAndRunes(out *ParseResult, repoID, relPath, text, compID string) {
	if out == nil {
		return
	}
	markup := stripSvelteBlocks(text)
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
	for _, sm := range svelteScriptRe.FindAllStringSubmatchIndex(text, -1) {
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
