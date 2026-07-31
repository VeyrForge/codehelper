package parser

import (
	"context"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var genericSymbolLine = regexp.MustCompile(`(?i)^\s*(?:export\s+)?(?:async\s+)?(?:function|func|def|class|interface|struct|trait)\s+([A-Za-z_][A-Za-z0-9_]*)`)

// parseGenericTextLite provides a safe low-fidelity fallback for textual files.
func parseGenericTextLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	text := string(buf)
	if !looksLikeText(text) {
		return &ParseResult{}, nil
	}
	lines := strings.Split(text, "\n")
	out := &ParseResult{}
	for i, line := range lines {
		m := genericSymbolLine.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		sym := symbol(repoID, relPath, name, types.SymbolKindUnknown, i+1, i+1, "text", "fallback=generic_text", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	}
	return out, nil
}

func looksLikeText(s string) bool {
	if s == "" {
		return true
	}
	bad := 0
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 32 {
			bad++
		}
	}
	return bad < len(s)/20+2
}
