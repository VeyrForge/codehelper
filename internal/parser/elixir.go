package parser

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	ex "github.com/smacker/go-tree-sitter/elixir"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseElixir extracts modules, defs, import-like edges (alias/import/require/use),
// @behaviour/@impl implements, densified call edges (remote Mod.fun, pipes,
// alias-resolved Demo.Format.apply), and Phoenix Controller/LiveView/Router densify.
// Call graph is heuristic — Medium band.
//
// tree-sitter-elixir stores module names as `alias` nodes (e.g. Phoenix.Router)
// under an `arguments` child that is NOT a named field — ChildByFieldName("arguments")
// is nil. Def names live as identifier children of a nested call under arguments.
func ParseElixir(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(ex.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	content := string(buf)
	frameworks := DetectFrameworkPacks(relPath, nil, content)
	phoenixish := looksLikePhoenixFile(relPath, content)
	var modStack []string
	var modSymStack []string
	var modRoleStack []string
	// Short alias leaf → fully-qualified module (Format → Demo.Format).
	aliases := map[string]string{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "unary_operator" {
			elixirEmitBehaviour(n, buf, repoID, relPath, modSymStack, out)
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i))
			}
			return
		}
		if n.Type() != "call" {
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i))
			}
			return
		}
		target := n.ChildByFieldName("target")
		if target == nil {
			target = ChildByType(n, "identifier")
		}
		if target == nil || target.Type() != "identifier" {
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i))
			}
			return
		}
		id := target.Content(buf)
		args := n.ChildByFieldName("arguments")
		if args == nil {
			args = ChildByType(n, "arguments")
		}
		switch id {
		case "defmodule":
			name := elixirModuleName(args, buf)
			if name == "" {
				return
			}
			modText := n.Content(buf)
			role := ""
			if phoenixish || looksLikePhoenixFile(relPath, modText) {
				role = phoenixModuleRole(name, modText)
				frameworks = withFramework(frameworks, string(FrameworkPhoenix))
			}
			sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "elixir", frameworkSignature(frameworks, role), "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			modStack = append(modStack, name)
			modSymStack = append(modSymStack, sym.ID)
			modRoleStack = append(modRoleStack, role)
			// Walk the module body only — do NOT extractCalls on the whole
			// do_block (alias/import/def/doc look like call nodes and flood
			// the graph with keyword noise). Defs extract their own calls.
			if body := ChildByType(n, "do_block"); body != nil {
				walk(body)
			}
			modStack = modStack[:len(modStack)-1]
			modSymStack = modSymStack[:len(modSymStack)-1]
			modRoleStack = modRoleStack[:len(modRoleStack)-1]
			return
		case "def", "defp":
			fn := elixirDefName(args, buf)
			if fn == "" {
				return
			}
			parent := ""
			if len(modStack) > 0 {
				parent = modStack[len(modStack)-1]
			}
			modRole := ""
			if len(modRoleStack) > 0 {
				modRole = modRoleStack[len(modRoleStack)-1]
			}
			role := ""
			if id == "def" {
				role = phoenixDefRole(modRole, fn)
			}
			sym := symbol(repoID, relPath, fn, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "elixir", frameworkSignature(frameworks, role), parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if body := ChildByType(n, "do_block"); body != nil {
				elixirExtractCalls(body, buf, repoID, relPath, sym.ID, aliases, out)
			}
			// One-liner: def init(opts), do: …
			if kw := ChildByType(args, "keywords"); kw != nil {
				elixirExtractCalls(kw, buf, repoID, relPath, sym.ID, aliases, out)
			}
			return
		case "alias", "import", "require", "use":
			if mod := elixirImportModule(args, buf); mod != "" {
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.8,
				})
				if id == "alias" {
					elixirRememberAlias(aliases, mod)
				}
				// use GenServer → module implements behaviour (impact inbound).
				if id == "use" && len(modSymStack) > 0 {
					elixirEmitImplements(repoID, relPath, modSymStack[len(modSymStack)-1], mod, 0.7, out)
				}
			}
			return
		case "behaviour":
			// Bare behaviour X (without @) — rare but legal.
			if mod := elixirImportModule(args, buf); mod != "" && len(modSymStack) > 0 {
				elixirEmitImplements(repoID, relPath, modSymStack[len(modSymStack)-1], mod, 0.85, out)
			}
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	extractPhoenixDSL(repoID, relPath, buf, out)
	return out, nil
}

func elixirModuleName(args *sitter.Node, buf []byte) string {
	if args == nil {
		return ""
	}
	if alias := ChildByType(args, "alias"); alias != nil {
		return alias.Content(buf)
	}
	return FirstIdentifier(args, buf)
}

func elixirDefName(args *sitter.Node, buf []byte) string {
	if args == nil {
		return ""
	}
	// def foo(opts) → arguments → call → identifier "foo"
	if call := ChildByType(args, "call"); call != nil {
		if t := call.ChildByFieldName("target"); t != nil && t.Type() == "identifier" {
			return t.Content(buf)
		}
		if id := ChildByType(call, "identifier"); id != nil {
			return id.Content(buf)
		}
	}
	if alias := ChildByType(args, "alias"); alias != nil {
		return alias.Content(buf)
	}
	return FirstIdentifier(args, buf)
}

func elixirImportModule(args *sitter.Node, buf []byte) string {
	if args == nil {
		return ""
	}
	if alias := ChildByType(args, "alias"); alias != nil {
		return strings.TrimSpace(alias.Content(buf))
	}
	// use GenServer / import String, only: […] — first identifier or call target
	if call := ChildByType(args, "call"); call != nil {
		if t := call.ChildByFieldName("target"); t != nil {
			s := strings.TrimSpace(t.Content(buf))
			if s != "" {
				return s
			}
		}
	}
	return strings.TrimSpace(FirstIdentifier(args, buf))
}

// elixirRememberAlias maps Format → Demo.Format for remote call densify.
func elixirRememberAlias(aliases map[string]string, mod string) {
	mod = strings.TrimSpace(mod)
	if mod == "" || aliases == nil {
		return
	}
	leaf := mod
	if i := strings.LastIndexByte(mod, '.'); i >= 0 && i+1 < len(mod) {
		leaf = mod[i+1:]
	}
	if leaf != "" {
		aliases[leaf] = mod
	}
}

func elixirEmitBehaviour(n *sitter.Node, buf []byte, repoID, relPath string, modSymStack []string, out *ParseResult) {
	if len(modSymStack) == 0 || n == nil {
		return
	}
	call := ChildByType(n, "call")
	if call == nil {
		return
	}
	id := ""
	if t := call.ChildByFieldName("target"); t != nil && t.Type() == "identifier" {
		id = t.Content(buf)
	} else if t := ChildByType(call, "identifier"); t != nil {
		id = t.Content(buf)
	}
	if id != "behaviour" {
		return
	}
	args := call.ChildByFieldName("arguments")
	if args == nil {
		args = ChildByType(call, "arguments")
	}
	mod := elixirImportModule(args, buf)
	if mod == "" {
		return
	}
	elixirEmitImplements(repoID, relPath, modSymStack[len(modSymStack)-1], mod, 0.85, out)
}

func elixirEmitImplements(repoID, relPath, fromSym, mod string, conf float64, out *ParseResult) {
	mod = strings.TrimSpace(mod)
	if mod == "" || fromSym == "" {
		return
	}
	tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, mod)
	out.Edges = append(out.Edges, types.Reference{
		ID:         edgeID(repoID, fromSym, tgt, "implements"),
		RepoID:     repoID,
		Kind:       types.RefKindImplements,
		SourceID:   fromSym,
		TargetID:   tgt,
		Confidence: conf,
	})
}

var elixirCallSkip = map[string]struct{}{
	"def": {}, "defp": {}, "defmodule": {}, "defprotocol": {}, "defimpl": {},
	"defmacro": {}, "defmacrop": {}, "alias": {}, "import": {}, "require": {},
	"use": {}, "quote": {}, "unquote": {}, "if": {}, "unless": {}, "case": {},
	"cond": {}, "with": {}, "for": {}, "try": {}, "receive": {}, "fn": {},
	"do": {}, "end": {}, "true": {}, "false": {}, "nil": {}, "when": {},
	"and": {}, "or": {}, "not": {}, "in": {}, "behaviour": {}, "impl": {},
}

// elixirExtractCalls densifies def-body calls: bare normalize(), remote
// Format.apply → apply + Format.apply + Demo.Format.apply (via alias map),
// and module reads for impact inbound.
func elixirExtractCalls(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, aliases map[string]string, out *ParseResult) {
	if root == nil {
		return
	}
	emitCall := func(name string, conf float64) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, skip := elixirCallSkip[name]; skip {
			return
		}
		if !isCallableName(name) && !strings.Contains(name, ".") {
			return
		}
		// Dotted names: validate leaf.
		leaf := name
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			leaf = name[i+1:]
		}
		if !isCallableName(leaf) {
			return
		}
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: conf,
		})
	}
	emitRead := func(name string, conf float64) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		leaf := name
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			leaf = name[i+1:]
		}
		if !isCallableName(leaf) {
			return
		}
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, name)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: conf,
		})
	}
	Walk(root, func(n *sitter.Node) {
		if n.Type() != "call" {
			return
		}
		if recv, meth, ok := elixirRemoteCall(n, buf); ok {
			emitCall(meth, 0.5)
			emitCall(recv+"."+meth, 0.7)
			emitRead(recv, 0.75)
			if aliases != nil {
				if full, ok := aliases[recv]; ok && full != "" && full != recv {
					emitCall(full+"."+meth, 0.8)
					emitRead(full, 0.8)
				}
			}
			return
		}
		// Bare call / GenServer.start_link already handled as remote when dotted.
		nm := rubyCallCallee(n, buf)
		if nm == "" {
			return
		}
		if _, skip := elixirCallSkip[nm]; skip {
			return
		}
		emitCall(nm, 0.5)
	})
}

// elixirRemoteCall resolves Format.apply / GenServer.start_link from a call→dot.
func elixirRemoteCall(n *sitter.Node, buf []byte) (recv, meth string, ok bool) {
	if n == nil {
		return "", "", false
	}
	var dot *sitter.Node
	if t := n.ChildByFieldName("target"); t != nil && t.Type() == "dot" {
		dot = t
	}
	if dot == nil {
		dot = ChildByType(n, "dot")
	}
	if dot == nil {
		return "", "", false
	}
	var aliasNode, idNode *sitter.Node
	for i := 0; i < int(dot.NamedChildCount()); i++ {
		c := dot.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "alias":
			aliasNode = c
		case "identifier", "simple_identifier":
			idNode = c
		}
	}
	if aliasNode == nil || idNode == nil {
		return "", "", false
	}
	recv = strings.TrimSpace(aliasNode.Content(buf))
	meth = strings.TrimSpace(idNode.Content(buf))
	if recv == "" || meth == "" {
		return "", "", false
	}
	// Alias may be Demo.Format — keep full left side as recv for Demo.Format.apply.
	return recv, meth, true
}
