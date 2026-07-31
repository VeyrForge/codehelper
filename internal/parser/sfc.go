package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Shared SFC / island extractors for Svelte, Vue, Astro (and MDX markup tags).
// Script bodies reuse ParseTypeScript with line remapping onto the real path.

var sfcScriptRe = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)
var sfcStyleRe = regexp.MustCompile(`(?is)<style([^>]*)>(.*?)</style>`)
var sfcComponentTagRe = regexp.MustCompile(`</?([A-Z][A-Za-z0-9_]*)\b`)
var sfcCSSClassRe = regexp.MustCompile(`\.([A-Za-z_][\w-]*)\s*[{,:]`)

func sfcScriptIsTS(attrs string) bool {
	a := strings.ToLower(attrs)
	return strings.Contains(a, `lang="ts"`) || strings.Contains(a, `lang='ts'`) ||
		strings.Contains(a, "lang=ts") || strings.Contains(a, `lang="typescript"`) ||
		strings.Contains(a, `lang='typescript'`) ||
		strings.Contains(a, `lang="tsx"`) || strings.Contains(a, `lang='tsx'`)
}

func sfcComponentSymbol(repoID, relPath, lang string) (types.Symbol, string) {
	base := strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
	if base == "" || base == "." {
		return types.Symbol{}, ""
	}
	sig := "component"
	p := strings.ToLower(filepath.ToSlash(relPath))
	if lang == "svelte" && (strings.Contains(p, "+page.") || strings.Contains(p, "+layout.") ||
		strings.Contains(p, "+error.")) {
		role := "page"
		if strings.Contains(p, "+layout.") {
			role = "layout"
		} else if strings.Contains(p, "+error.") {
			role = "error"
		}
		sig = frameworkSignature([]string{string(FrameworkSvelte), string(FrameworkSvelteKit)}, role)
	}
	comp := symbol(repoID, relPath, base, types.SymbolKindClass, 1, 1, lang, sig, "")
	return comp, comp.ID
}

// extractSFCScripts runs ParseTypeScript on each <script> body and remaps onto relPath.
func extractSFCScripts(ctx context.Context, out *ParseResult, repoID, relPath, text, lang string) {
	if out == nil {
		return
	}
	for _, m := range sfcScriptRe.FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 6 {
			continue
		}
		attrs := text[m[2]:m[3]]
		bodyStart, bodyEnd := m[4], m[5]
		body := text[bodyStart:bodyEnd]
		if strings.TrimSpace(body) == "" {
			continue
		}
		lineOffset := 1 + strings.Count(text[:bodyStart], "\n")
		scriptPath := relPath + ".js"
		if sfcScriptIsTS(attrs) {
			scriptPath = relPath + ".ts"
		}
		part, err := ParseTypeScript(ctx, repoID, scriptPath, []byte(body))
		if err != nil || part == nil {
			continue
		}
		mergeSFCScript(out, part, repoID, relPath, lang, lineOffset)
	}
}

// mergeSFCScript remaps ParseTypeScript results from a synthetic script path onto
// the real SFC path, shifting line numbers by lineOffset-1.
func mergeSFCScript(dst, src *ParseResult, repoID, realPath, lang string, lineOffset int) {
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
		symLang := s.Language
		if lang != "" && (symLang == "" || symLang == "typescript" || symLang == "javascript") {
			symLang = lang
		}
		ns := symbol(repoID, realPath, s.Name, s.Kind, ls, le, symLang, s.Signature, "")
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
		if strings.HasPrefix(id, "file:") {
			return FileNodeID(repoID, realPath)
		}
		if strings.HasPrefix(id, "symref:") {
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

// extractSFCMarkup emits reads from the component to capitalized child tags
// found outside <script>/<style> (and optional extra strips).
func extractSFCMarkup(out *ParseResult, repoID, relPath, text, compID, lang string, extraStrip ...*regexp.Regexp) {
	if out == nil || compID == "" {
		return
	}
	markup := stripSFCBlocks(text)
	for _, re := range extraStrip {
		if re != nil {
			markup = re.ReplaceAllString(markup, "")
		}
	}
	seen := map[string]bool{}
	base := strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
	for _, m := range sfcComponentTagRe.FindAllStringSubmatch(markup, -1) {
		if len(m) < 2 {
			continue
		}
		name := m[1]
		if name == "" || name == base || seen[name] {
			continue
		}
		// Skip framework special elements (svelte:head, vue:… if ever capitalized).
		if strings.EqualFold(name, "svelte") || strings.EqualFold(name, "vue") || strings.EqualFold(name, "astro") {
			continue
		}
		seen[name] = true
		_ = lang
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

// extractSFCStyle indexes CSS class selectors as symbols under the component.
func extractSFCStyle(out *ParseResult, repoID, relPath, text, compID, lang string) {
	if out == nil {
		return
	}
	if lang == "" {
		lang = "css"
	}
	for _, m := range sfcStyleRe.FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 6 {
			continue
		}
		bodyStart, bodyEnd := m[4], m[5]
		body := text[bodyStart:bodyEnd]
		lineOffset := 1 + strings.Count(text[:bodyStart], "\n")
		seen := map[string]bool{}
		for _, cm := range sfcCSSClassRe.FindAllStringSubmatchIndex(body, -1) {
			if len(cm) < 4 {
				continue
			}
			name := body[cm[2]:cm[3]]
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			line := lineOffset + strings.Count(body[:cm[2]], "\n")
			sym := symbol(repoID, relPath, "."+name, types.SymbolKindVariable, line, line, lang, "css-class", "")
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

func stripSFCBlocks(text string) string {
	out := sfcScriptRe.ReplaceAllString(text, "")
	out = sfcStyleRe.ReplaceAllString(out, "")
	return out
}
