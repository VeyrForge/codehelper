package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// PascalCase default import: import Callout from './Callout.mdx'
var mdxDefaultImportRe = regexp.MustCompile(`(?m)^\s*import\s+([A-Z][A-Za-z0-9_]*)\s+from\s+`)

// Named imports may include PascalCase components: import { Note, Callout as C } from '…'
var mdxNamedImportRe = regexp.MustCompile(`(?m)^\s*import\s+\{([^}]+)\}\s+from\s+`)

// MDX expression form {Callout} / {Callout(...)} (no angle brackets).
var mdxExprComponentRe = regexp.MustCompile(`\{([A-Z][A-Za-z0-9_]*)\b`)

// ParseMDX is a Medium densifier for .mdx: JS/TS islands (imports, exports,
// fenced js/ts/tsx, brace-continued blocks) via ParseTypeScript, JSX/MDX
// component tags + imported/expression component reads. Markdown prose and
// full MDX expression graphs are not claimed.
func ParseMDX(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	text := string(buf)

	comp, compID := sfcComponentSymbol(repoID, relPath, "mdx")
	if compID != "" {
		out.Symbols = append(out.Symbols, comp)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, comp.ID))
	}

	masked := mdxMaskNonJS(text)
	if strings.TrimSpace(masked) != "" {
		part, err := ParseTypeScript(ctx, repoID, relPath+".tsx", []byte(masked))
		if err == nil && part != nil {
			mergeSFCScript(out, part, repoID, relPath, "mdx", 1)
		}
	}

	extractSFCMarkup(out, repoID, relPath, text, compID, "mdx")
	extractMDXComponents(out, repoID, relPath, text, compID)
	return out, nil
}

func extractMDXComponents(out *ParseResult, repoID, relPath, text, compID string) {
	if out == nil {
		return
	}
	seenSym := map[string]bool{}
	for _, s := range out.Symbols {
		seenSym[s.Name] = true
	}
	addComponent := func(name string, line int) {
		if name == "" || seenSym[name] {
			return
		}
		seenSym[name] = true
		sig := frameworkSignature([]string{"mdx"}, "component")
		sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "mdx", sig, "")
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

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lineNo := i + 1
		if m := mdxDefaultImportRe.FindStringSubmatch(line); len(m) >= 2 {
			addComponent(m[1], lineNo)
		}
		if m := mdxNamedImportRe.FindStringSubmatch(line); len(m) >= 2 {
			for _, part := range strings.Split(m[1], ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				// "Foo as Bar" → local binding Bar; plain Foo → Foo.
				name := part
				if asIdx := strings.LastIndex(strings.ToLower(part), " as "); asIdx >= 0 {
					name = strings.TrimSpace(part[asIdx+4:])
				}
				if name == "" {
					continue
				}
				r := []rune(name)
				if unicode.IsUpper(r[0]) {
					addComponent(name, lineNo)
				}
			}
		}
	}

	if compID == "" {
		return
	}
	seenRead := map[string]bool{}
	trailingName := func(id string) string {
		if i := strings.LastIndex(id, ":"); i >= 0 {
			return id[i+1:]
		}
		return id
	}
	for _, e := range out.Edges {
		if e.SourceID == compID && e.Kind == types.RefKindReads {
			seenRead[trailingName(e.TargetID)] = true
			if strings.HasPrefix(e.TargetID, "sym:") {
				for _, s := range out.Symbols {
					if s.ID == e.TargetID {
						seenRead[s.Name] = true
					}
				}
			}
		}
	}
	for _, m := range mdxExprComponentRe.FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 4 {
			continue
		}
		name := text[m[2]:m[3]]
		if name == "" || seenRead[name] {
			continue
		}
		seenRead[name] = true
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

func mdxMaskNonJS(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, len(lines))
	inFence := false
	fenceJS := false
	braceDepth := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inFence {
				inFence = true
				lang := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "```")))
				fenceJS = lang == "" || lang == "js" || lang == "jsx" || lang == "ts" || lang == "tsx" ||
					lang == "javascript" || lang == "typescript"
			} else {
				inFence = false
				fenceJS = false
			}
			out[i] = ""
			continue
		}
		if inFence {
			if fenceJS {
				out[i] = line
				braceDepth += mdxBraceDelta(line)
			} else {
				out[i] = ""
			}
			continue
		}
		if braceDepth > 0 || mdxLooksLikeJSLine(trimmed) {
			out[i] = line
			braceDepth += mdxBraceDelta(line)
			if braceDepth < 0 {
				braceDepth = 0
			}
			continue
		}
		out[i] = ""
	}
	return strings.Join(out, "\n")
}

func mdxBraceDelta(line string) int {
	return strings.Count(line, "{") - strings.Count(line, "}")
}

func mdxLooksLikeJSLine(trimmed string) bool {
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, p := range []string{
		"import ", "export ", "const ", "let ", "var ", "function ", "class ",
		"async ", "type ", "interface ", "return ", "await ",
	} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
		return true
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "}") {
		return true
	}
	// JSX / MDX component usage on its own line.
	if strings.HasPrefix(trimmed, "<") && len(trimmed) > 1 {
		r := rune(trimmed[1])
		if unicode.IsUpper(r) || trimmed[1] == '/' {
			return true
		}
	}
	return false
}
