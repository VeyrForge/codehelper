package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Astro frontmatter between opening --- fences at file start.
var astroFrontmatterRe = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---`)

// Client island directives on capitalized component tags.
var astroClientDirectiveRe = regexp.MustCompile(
	`(?is)<([A-Z][A-Za-z0-9_]*)\b([^>]*?)\bclient:(load|idle|visible|media|only)\b`,
)

// ParseAstro densifies .astro files: frontmatter + <script> via TS/JS, markup
// component tags, scoped style classes, and client:* island markers. Full
// island / SSR runtime graphs are not claimed.
func ParseAstro(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	text := string(buf)

	comp, compID := sfcComponentSymbol(repoID, relPath, "astro")
	if compID != "" {
		out.Symbols = append(out.Symbols, comp)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, comp.ID))
	}

	if m := astroFrontmatterRe.FindStringSubmatchIndex(text); len(m) >= 4 {
		bodyStart, bodyEnd := m[2], m[3]
		body := text[bodyStart:bodyEnd]
		if strings.TrimSpace(body) != "" {
			lineOffset := 1 + strings.Count(text[:bodyStart], "\n")
			scriptPath := relPath + ".ts"
			part, err := ParseTypeScript(ctx, repoID, scriptPath, []byte(body))
			if err == nil && part != nil {
				mergeSFCScript(out, part, repoID, relPath, "astro", lineOffset)
			}
		}
	}

	extractSFCScripts(ctx, out, repoID, relPath, text, "astro")
	extractSFCMarkup(out, repoID, relPath, text, compID, "astro", astroFrontmatterRe)
	extractSFCStyle(out, repoID, relPath, text, compID, "astro")
	extractAstroClientIslands(out, repoID, relPath, text, compID)
	return out, nil
}

// extractAstroClientIslands marks client:* hydrated components as island symbols
// (role=island) and links the page component → island → child tag.
func extractAstroClientIslands(out *ParseResult, repoID, relPath, text, compID string) {
	if out == nil {
		return
	}
	markup := stripSFCBlocks(text)
	markup = astroFrontmatterRe.ReplaceAllString(markup, "")
	seen := map[string]bool{}
	for _, m := range astroClientDirectiveRe.FindAllStringSubmatchIndex(markup, -1) {
		if len(m) < 8 {
			continue
		}
		name := markup[m[2]:m[3]]
		directive := strings.ToLower(markup[m[6]:m[7]])
		if name == "" || directive == "" {
			continue
		}
		key := name + ":" + directive
		if seen[key] {
			continue
		}
		seen[key] = true
		line := 1 + strings.Count(markup[:m[0]], "\n")
		islandName := "island:" + name
		sig := frameworkSignature([]string{"astro"}, "island") + ";client=" + directive
		sym := symbol(repoID, relPath, islandName, types.SymbolKindVariable, line, line, "astro", sig, "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		child := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, sym.ID, child, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   sym.ID,
			TargetID:   child,
			Confidence: 0.85,
		})
		if compID != "" {
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, compID, sym.ID, "reads"),
				RepoID:     repoID,
				Kind:       types.RefKindReads,
				SourceID:   compID,
				TargetID:   sym.ID,
				Confidence: 0.85,
			})
		}
	}
}
