package parser

import (
	"context"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Prisma schema densify (schema.prisma): model/enum symbols + relation-field
// call edges to related models. No tree-sitter grammar — line/regex lite.
//
// Client usage (prisma.user.findMany) lives in orm.go on the TS/JS graph.

var (
	prismaModelStart = regexp.MustCompile(`(?m)^\s*model\s+([A-Z][A-Za-z0-9_]*)\s*\{`)
	prismaEnumStart  = regexp.MustCompile(`(?m)^\s*enum\s+([A-Z][A-Za-z0-9_]*)\s*\{`)
	prismaFieldLine  = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s+([A-Za-z_][A-Za-z0-9_]*)(\[\])?`)
)

var prismaScalarTypes = map[string]bool{
	"String": true, "Int": true, "BigInt": true, "Float": true, "Decimal": true,
	"Boolean": true, "DateTime": true, "Json": true, "Bytes": true,
}

// ParsePrisma indexes Prisma schema models/enums and relation edges so agents
// can locate User↔Post from schema.prisma without reading the whole file.
func ParsePrisma(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	_ = ctx
	out := &ParseResult{}
	src := string(buf)
	lines := strings.Split(src, "\n")
	fid := FileNodeID(repoID, relPath)

	type block struct {
		kind string // model | enum
		name string
		sym  string
		end  int
	}
	var open *block

	closeBlock := func(endLine int) {
		if open == nil {
			return
		}
		// Patch LineEnd on the symbol we just appended.
		for i := len(out.Symbols) - 1; i >= 0; i-- {
			if out.Symbols[i].ID == open.sym {
				out.Symbols[i].LineEnd = endLine
				break
			}
		}
		open = nil
	}

	for i, line := range lines {
		lineNo := i + 1
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "//") {
			continue
		}
		if open != nil {
			if trim == "}" {
				closeBlock(lineNo)
				continue
			}
			if open.kind == "model" {
				prismaEmitFieldEdges(repoID, relPath, open.sym, line, out)
			}
			continue
		}
		if m := prismaModelStart.FindStringSubmatch(line); len(m) > 1 {
			name := m[1]
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, lineNo, lineNo, "prisma",
				"frameworks=prisma;role=model", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			open = &block{kind: "model", name: name, sym: sym.ID, end: lineNo}
			_ = fid
			continue
		}
		if m := prismaEnumStart.FindStringSubmatch(line); len(m) > 1 {
			name := m[1]
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, lineNo, lineNo, "prisma",
				"frameworks=prisma;role=enum", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			open = &block{kind: "enum", name: name, sym: sym.ID, end: lineNo}
			continue
		}
	}
	if open != nil {
		closeBlock(len(lines))
	}
	return out, nil
}

func prismaEmitFieldEdges(repoID, relPath, modelSym, line string, out *ParseResult) {
	trim := strings.TrimSpace(line)
	if trim == "" || strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "@@") || strings.HasPrefix(trim, "@") {
		return
	}
	m := prismaFieldLine.FindStringSubmatch(line)
	if len(m) < 3 {
		return
	}
	field, typ := m[1], m[2]
	if field == "" || typ == "" || prismaScalarTypes[typ] {
		return
	}
	// Relation / enum / composite type reference — edge to the type leaf.
	emitNestCall(repoID, relPath, modelSym, typ, 0.88, out)
	_ = field
}
