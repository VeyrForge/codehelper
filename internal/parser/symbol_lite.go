package parser

import (
	"context"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	reSQLTable = regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)`)
	reSQLView  = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?VIEW\s+(\w+)`)
	reSQLFunc  = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+(\w+)`)
	reHTMLTag  = regexp.MustCompile(`<\s*([a-zA-Z][\w:-]*)`)
	reCSSRule  = regexp.MustCompile(`(?m)(?:^|[\s,}])([.#]?[a-zA-Z_-][\w-]*)\s*\{`)
	reDartDecl = regexp.MustCompile(`(?m)\b(?:class|mixin|enum|extension|typedef)\s+(\w+)|\b(?:void|Future|int|String|bool|double|dynamic)\s+(\w+)\s*\(`)
)

func parseSQLLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	s := string(buf)
	line := 1
	for _, seg := range strings.Split(s, "\n") {
		for _, re := range []*regexp.Regexp{reSQLTable, reSQLView, reSQLFunc} {
			for _, m := range re.FindAllStringSubmatch(seg, -1) {
				if len(m) < 2 || m[1] == "" {
					continue
				}
				name := m[1]
				sym := symbol(repoID, relPath, name, types.SymbolKindClass, line, line, "sql", "", "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			}
		}
		line++
	}
	return out, nil
}

func parseHTMLLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	seen := map[string]struct{}{}
	s := string(buf)
	line := 1
	for _, ln := range strings.Split(s, "\n") {
		for _, m := range reHTMLTag.FindAllStringSubmatch(ln, -1) {
			tag := strings.ToLower(m[1])
			if tag == "br" || tag == "meta" || tag == "link" {
				continue
			}
			if _, ok := seen[tag]; ok {
				continue
			}
			seen[tag] = struct{}{}
			sym := symbol(repoID, relPath, "tag:"+tag, types.SymbolKindVariable, line, line, "html", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
		line++
	}
	return out, nil
}

func parseCSSLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	s := string(buf)
	line := 1
	for _, ln := range strings.Split(s, "\n") {
		for _, m := range reCSSRule.FindAllStringSubmatch(ln, -1) {
			name := m[1]
			if name == "" {
				continue
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "css", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
		line++
	}
	return out, nil
}

func parseDartLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	s := string(buf)
	line := 1
	for _, ln := range strings.Split(s, "\n") {
		for _, m := range reDartDecl.FindAllStringSubmatch(ln, -1) {
			name := m[1]
			if name == "" {
				name = m[2]
			}
			if name == "" {
				continue
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "dart", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		}
		line++
	}
	return out, nil
}
