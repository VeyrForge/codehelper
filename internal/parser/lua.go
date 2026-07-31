package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	lua "github.com/smacker/go-tree-sitter/lua"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseLua extracts function declarations, require() imports, and call edges.
// tree-sitter-lua uses function_statement (not function_declaration). Call
// edges are name-only — Low/Medium honesty band.
func ParseLua(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(lua.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	Walk(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "function_statement", "function_declaration", "function_definition":
			name := luaFunctionName(n, buf)
			if name == "" {
				return
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "lua", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			extractCalls(n, buf, repoID, relPath, sym.ID, out)
		case "function_call":
			// Top-level require("mod") → import edge (also emitted as calls from
			// enclosing functions via extractCalls).
			if mod := luaRequireModule(n, buf); mod != "" {
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.85,
				})
			}
		}
	})
	return out, nil
}

func luaFunctionName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	if name := ChildName(n, "name", buf); name != "" {
		// M:method or t.fn → trailing identifier
		if i := strings.LastIndexAny(name, ":."); i >= 0 && i+1 < len(name) {
			return strings.TrimSpace(name[i+1:])
		}
		return strings.TrimSpace(name)
	}
	if fn := ChildByType(n, "function_name"); fn != nil {
		s := strings.TrimSpace(fn.Content(buf))
		if i := strings.LastIndexAny(s, ":."); i >= 0 && i+1 < len(s) {
			return strings.TrimSpace(s[i+1:])
		}
		return s
	}
	return FirstIdentifier(n.ChildByFieldName("name"), buf)
}

func luaRequireModule(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	// require("helpers") — name field is identifier "require", args hold string.
	name := ChildName(n, "name", buf)
	if name == "" {
		if id := ChildByType(n, "identifier"); id != nil {
			name = id.Content(buf)
		}
	}
	// Strip table.method prefix: package.require still rare; accept bare require.
	if base := name; base != "" {
		if i := strings.LastIndexAny(base, ":."); i >= 0 {
			base = base[i+1:]
		}
		if base != "require" {
			return ""
		}
	} else {
		return ""
	}
	var str string
	Walk(n, func(c *sitter.Node) {
		if str != "" {
			return
		}
		if c.Type() == "string" || c.Type() == "string_content" {
			s := strings.Trim(c.Content(buf), `"'`)
			if s != "" && c.Type() == "string" {
				str = s
			}
		}
	})
	return str
}
