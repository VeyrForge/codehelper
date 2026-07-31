package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

var (
	reSQLTable   = regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w."` + "`" + `\[\]]+)`)
	reSQLView    = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?VIEW\s+([\w."` + "`" + `\[\]]+)`)
	reSQLFunc    = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+([\w."` + "`" + `\[\]]+)`)
	reSQLProc    = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?PROCEDURE\s+([\w."` + "`" + `\[\]]+)`)
	reSQLTrigger = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+([\w."` + "`" + `\[\]]+)`)
	reSQLIndex   = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w."` + "`" + `\[\]]+)`)
	reSQLRefs    = regexp.MustCompile(`(?i)\bREFERENCES\s+([\w."` + "`" + `\[\]]+)`)
	reHTMLTag    = regexp.MustCompile(`<\s*([a-zA-Z][\w:-]*)`)
	reCSSRule    = regexp.MustCompile(`(?m)(?:^|[\s,}])([.#]?[a-zA-Z_-][\w-]*)\s*\{`)
)

func sqlIdent(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, "`\"[]")
	if i := strings.LastIndex(raw, "."); i >= 0 {
		raw = raw[i+1:]
	}
	return raw
}

func parseSQLLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	s := string(buf)
	line := 1
	var curTable string
	var curTableSym string
	for _, seg := range strings.Split(s, "\n") {
		for _, pair := range []struct {
			re      *regexp.Regexp
			isTable bool
		}{
			{reSQLTable, true},
			{reSQLView, false},
			{reSQLFunc, false},
			{reSQLProc, false},
			{reSQLTrigger, false},
			{reSQLIndex, false},
		} {
			for _, m := range pair.re.FindAllStringSubmatch(seg, -1) {
				if len(m) < 2 || m[1] == "" {
					continue
				}
				name := sqlIdent(m[1])
				if name == "" {
					continue
				}
				sym := symbol(repoID, relPath, name, types.SymbolKindClass, line, line, "sql", "", "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				if pair.isTable {
					curTable = name
					curTableSym = sym.ID
				}
			}
		}
		// FOREIGN KEY … REFERENCES other — reads edge from current CREATE TABLE.
		if curTableSym != "" {
			for _, m := range reSQLRefs.FindAllStringSubmatch(seg, -1) {
				if len(m) < 2 {
					continue
				}
				tgtName := sqlIdent(m[1])
				if tgtName == "" || strings.EqualFold(tgtName, curTable) {
					continue
				}
				tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, tgtName)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, curTableSym, tgt, "reads"),
					RepoID:     repoID,
					Kind:       types.RefKindReads,
					SourceID:   curTableSym,
					TargetID:   tgt,
					Confidence: 0.7,
				})
			}
		}
		if strings.Contains(seg, ";") {
			curTable = ""
			curTableSym = ""
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
