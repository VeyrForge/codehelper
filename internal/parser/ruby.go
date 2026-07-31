package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	ruby "github.com/smacker/go-tree-sitter/ruby"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseRuby extracts methods, classes/modules, require/load import edges, and
// call edges. Methods record their enclosing class/module name in ParentID.
func ParseRuby(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(ruby.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	var classStack []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "call":
			if mod := rubyRequireModule(n, buf); mod != "" {
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
			// include/extend/prepend Module — mixin inbound for the enclosing class.
			if mixin := rubyMixinModule(n, buf); mixin != "" && len(classStack) > 0 {
				parentName := classStack[len(classStack)-1]
				var classSym string
				var classIdx = -1
				for i, s := range out.Symbols {
					if s.Name == parentName && s.Kind == types.SymbolKindClass {
						classSym = s.ID
						classIdx = i
					}
				}
				if classSym != "" {
					tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, mixin)
					out.Edges = append(out.Edges, types.Reference{
						ID:         edgeID(repoID, classSym, tgt, "implements"),
						RepoID:     repoID,
						Kind:       types.RefKindImplements,
						SourceID:   classSym,
						TargetID:   tgt,
						Confidence: 0.85,
					})
					if classIdx >= 0 {
						out.Symbols[classIdx].Signature = appendEmbedsSig(out.Symbols[classIdx].Signature, []string{mixin})
					}
				}
			}
			// Still walk children (nested calls are rare but harmless).
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
		case "method", "singleton_method":
			name := ChildName(n, "name", buf)
			if name != "" {
				parent := ""
				if len(classStack) > 0 {
					parent = classStack[len(classStack)-1]
				}
				sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "ruby", "", parent)
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				typeOf := rubySelfTypeOf(parent)
				extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, typeOf)
				rubyEmitConstantReads(n, buf, repoID, relPath, sym.ID, out)
			}
		case "class", "module":
			name := ChildName(n, "name", buf)
			if name != "" {
				var embeds []string
				if sc := rubySuperclassName(n, buf); sc != "" {
					embeds = append(embeds, sc)
				}
				sig := appendEmbedsSig("", embeds)
				sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "ruby", sig, "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				rubyEmitSuperclass(n, buf, repoID, relPath, sym.ID, out)
				classStack = append(classStack, name)
				for i := 0; i < int(n.ChildCount()); i++ {
					walk(n.Child(i))
				}
				classStack = classStack[:len(classStack)-1]
				return
			}
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
		default:
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
		}
	}
	walk(tree.RootNode())
	extractSinatraDSL(repoID, relPath, buf, out)
	extractRailsDSL(repoID, relPath, buf, out)
	return out, nil
}

// rubySelfTypeOf maps self receivers to the enclosing class/module name.
func rubySelfTypeOf(parent string) func(string) string {
	if parent == "" {
		return nil
	}
	return func(recv string) string {
		switch strings.TrimSpace(recv) {
		case "self":
			return parent
		}
		return ""
	}
}

// sinatraRouteDSL matches top-level / class-body Sinatra route registrations:
//
//	get '/' do … end
//	post "/x", :provides => :json do … end
var sinatraRouteDSL = regexp.MustCompile(`(?m)^\s*(get|post|put|patch|delete|options|head|link|unlink)\s+['"]`)

// railsRouteTo matches get/post… 'path', to: 'ctrl#action' or => 'ctrl#action'
// (namespaced controllers: admin/users#show).
var railsRouteTo = regexp.MustCompile(`(?i)^\s*(get|post|put|patch|delete|match)\s+.+?(?:to:\s*|=>\s*)['"]([a-z0-9_/-]+)#([a-z0-9_]+)['"]`)

// railsRootTo matches root to: 'home#index' / root 'home#index' (no path arg).
var railsRootTo = regexp.MustCompile(`(?i)^\s*root\s+(?:(?:to:\s*|=>\s*)?['"]([a-z0-9_/-]+)#([a-z0-9_]+)['"])`)

// railsResources matches resources/resource :name
var railsResources = regexp.MustCompile(`(?i)^\s*(resources|resource)\s+:([a-z0-9_]+)`)

// railsNamespace matches namespace :admin do (module path prefix for nested resources).
var railsNamespace = regexp.MustCompile(`(?i)^\s*namespace\s+(?::([a-z0-9_]+)|['"]([a-z0-9_]+)['"])`)

// railsMemberCollection matches member/collection do blocks under resources.
var railsMemberCollection = regexp.MustCompile(`(?i)^\s*(member|collection)\s+do\b`)

// railsSymVerb matches get :preview / post :approve inside member/collection.
var railsSymVerb = regexp.MustCompile(`(?i)^\s*(get|post|put|patch|delete)\s+:([a-z0-9_]+)\b`)

// railsCallback matches before_action/after_action/around_action/skip_* :sym or 'sym'
var railsCallback = regexp.MustCompile(`(?i)^\s*(before_action|after_action|around_action|before_filter|after_filter|skip_before_action|skip_after_action)\s+(?::([a-z0-9_]+)|['"]([a-z0-9_]+)['"])`)

// railsAssociation matches has_many/belongs_to/has_one/has_and_belongs_to_many :Name
var railsAssociation = regexp.MustCompile(`(?i)^\s*(has_many|belongs_to|has_one|has_and_belongs_to_many)\s+:([a-z0-9_]+)`)

// railsResourceActions are the conventional REST actions densified from resources :x.
var railsResourceActions = []string{"index", "show", "new", "create", "edit", "update", "destroy"}

// railsSingularResourceActions are REST actions for singular resource :x.
var railsSingularResourceActions = []string{"show", "new", "create", "edit", "update", "destroy"}

func looksLikeRails(relPath, src string) bool {
	p := strings.ToLower(filepath.ToSlash(relPath))
	body := strings.ToLower(src)
	return strings.Contains(p, "config/routes") ||
		strings.Contains(body, "rails.application") ||
		strings.Contains(body, "applicationcontroller") ||
		strings.Contains(body, "activerecord::base") ||
		strings.Contains(body, "actioncontroller::base") ||
		strings.Contains(body, "before_action") ||
		strings.Contains(body, "after_action") ||
		strings.Contains(body, "around_action") ||
		strings.Contains(body, "skip_before_action") ||
		(strings.Contains(body, "has_many") && strings.Contains(body, "class ")) ||
		(strings.Contains(body, "belongs_to") && strings.Contains(body, "class ")) ||
		(strings.Contains(body, "has_one") && strings.Contains(body, "class ")) ||
		(strings.Contains(body, "has_and_belongs_to_many") && strings.Contains(body, "class "))
}

func looksLikeSinatra(relPath, src string) bool {
	p := strings.ToLower(filepath.ToSlash(relPath))
	body := strings.ToLower(src)
	return strings.Contains(body, "sinatra") ||
		strings.Contains(p, "sinatra") ||
		strings.Contains(body, "require 'sinatra") ||
		strings.Contains(body, `require "sinatra`)
}

// extractSinatraDSL indexes Sinatra HTTP verb DSL calls as entrypoints that
// call the matching Base method (get/post/…) so impact on get sees app routes.
func extractSinatraDSL(repoID, relPath string, buf []byte, out *ParseResult) {
	if out == nil {
		return
	}
	src := string(buf)
	// Rails owns get/post in config/routes.rb — don't mis-tag as Sinatra.
	if looksLikeRails(relPath, src) && !looksLikeSinatra(relPath, src) {
		return
	}
	if !looksLikeSinatra(relPath, src) && !sinatraRouteDSL.MatchString(src) {
		return
	}
	// Bare get '/' without Sinatra markers: only treat as Sinatra when not Rails.
	if !looksLikeSinatra(relPath, src) {
		return
	}
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		m := sinatraRouteDSL.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		verb := strings.ToLower(m[1])
		siteName := fmt.Sprintf("sinatra_%s_%d", verb, i+1)
		sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "ruby", "frameworks=sinatra;role=entrypoint", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, verb)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, sym.ID, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   sym.ID,
			TargetID:   tgt,
			Confidence: 0.85,
		})
		// Also call route — all verbs funnel through route().
		rt := fmt.Sprintf("symref:%s:%s:route", repoID, relPath)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, sym.ID, rt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   sym.ID,
			TargetID:   rt,
			Confidence: 0.7,
		})
	}
}

// extractRailsDSL indexes Rails routes, controller filters, and AR associations
// so locate/impact can reach UsersController#show from config/routes.rb.
// Densifies: namespaced to:, root, namespace blocks, resources→REST actions,
// and member/collection get :action sites.
func extractRailsDSL(repoID, relPath string, buf []byte, out *ParseResult) {
	if out == nil {
		return
	}
	src := string(buf)
	if !looksLikeRails(relPath, src) {
		return
	}
	fw := "frameworks=rails"
	lines := strings.Split(src, "\n")
	var nsStack []string
	var resStack []string
	var blockKinds []string // "namespace" | "resources" | "member" | "collection" | "other"
	inMemberOrCollection := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if trim == "end" {
			if len(blockKinds) > 0 {
				kind := blockKinds[len(blockKinds)-1]
				blockKinds = blockKinds[:len(blockKinds)-1]
				switch kind {
				case "namespace":
					if len(nsStack) > 0 {
						nsStack = nsStack[:len(nsStack)-1]
					}
				case "resources":
					if len(resStack) > 0 {
						resStack = resStack[:len(resStack)-1]
					}
				case "member", "collection":
					inMemberOrCollection = false
					for _, k := range blockKinds {
						if k == "member" || k == "collection" {
							inMemberOrCollection = true
							break
						}
					}
				}
			}
			continue
		}
		if m := railsNamespace.FindStringSubmatch(line); len(m) > 1 {
			ns := m[1]
			if ns == "" {
				ns = m[2]
			}
			if ns != "" {
				nsStack = append(nsStack, ns)
				if strings.HasSuffix(trim, " do") {
					blockKinds = append(blockKinds, "namespace")
				}
			}
			continue
		}
		if m := railsMemberCollection.FindStringSubmatch(line); len(m) > 1 {
			blockKinds = append(blockKinds, strings.ToLower(m[1]))
			inMemberOrCollection = true
			continue
		}
		if m := railsRootTo.FindStringSubmatch(line); len(m) > 2 {
			emitRailsRouteSite(repoID, relPath, fw, "root", m[1], m[2], i+1, nsStack, out)
			continue
		}
		if m := railsRouteTo.FindStringSubmatch(line); len(m) > 3 {
			emitRailsRouteSite(repoID, relPath, fw, strings.ToLower(m[1]), m[2], m[3], i+1, nsStack, out)
			continue
		}
		if inMemberOrCollection && len(resStack) > 0 {
			if m := railsSymVerb.FindStringSubmatch(line); len(m) > 2 {
				verb, action := strings.ToLower(m[1]), m[2]
				resPath := railsJoinPath(nsStack, resStack[len(resStack)-1])
				ctrl := railsControllerName(resPath)
				siteName := fmt.Sprintf("rails_%s_%s_%s_%d", verb, strings.ReplaceAll(resPath, "/", "_"), action, i+1)
				sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "ruby", fw+";role=entrypoint", "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				emitRubyCall(repoID, relPath, sym.ID, ctrl, 0.9, out)
				emitRubyCall(repoID, relPath, sym.ID, action, 0.85, out)
				railsEmitPathModules(repoID, relPath, sym.ID, resPath, ctrl, out)
				continue
			}
		}
		if m := railsResources.FindStringSubmatch(line); len(m) > 2 {
			kind, name := strings.ToLower(m[1]), m[2]
			resPath := railsJoinPath(nsStack, name)
			ctrl := railsControllerName(resPath)
			siteName := fmt.Sprintf("rails_%s_%s_%d", kind, strings.ReplaceAll(resPath, "/", "_"), i+1)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "ruby", fw+";role=entrypoint", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitRubyCall(repoID, relPath, sym.ID, ctrl, 0.9, out)
			railsEmitPathModules(repoID, relPath, sym.ID, resPath, ctrl, out)
			actions := railsResourceActions
			if kind == "resource" {
				actions = railsSingularResourceActions
			}
			for _, action := range actions {
				emitRubyCall(repoID, relPath, sym.ID, action, 0.8, out)
			}
			if strings.HasSuffix(trim, " do") {
				resStack = append(resStack, name)
				blockKinds = append(blockKinds, "resources")
			}
			continue
		}
		if strings.HasSuffix(trim, " do") {
			blockKinds = append(blockKinds, "other")
		}
		if m := railsCallback.FindStringSubmatch(line); len(m) > 1 {
			filter := m[2]
			if filter == "" {
				filter = m[3]
			}
			if filter == "" {
				continue
			}
			kind := strings.ToLower(m[1])
			role := "filter"
			if strings.HasPrefix(kind, "skip_") {
				role = "skip_filter"
			}
			siteName := fmt.Sprintf("rails_%s_%s_%d", kind, filter, i+1)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "ruby", fw+";role="+role, "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitRubyCall(repoID, relPath, sym.ID, filter, 0.9, out)
			continue
		}
		if m := railsAssociation.FindStringSubmatch(line); len(m) > 2 {
			kind, name := strings.ToLower(m[1]), m[2]
			siteName := fmt.Sprintf("rails_%s_%s_%d", kind, name, i+1)
			sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, i+1, i+1, "ruby", fw+";role=association", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			emitRubyCall(repoID, relPath, sym.ID, kind, 0.7, out)
			emitRubyCall(repoID, relPath, sym.ID, railsModelName(name), 0.85, out)
		}
	}
}

func emitRailsRouteSite(repoID, relPath, fw, verb, ctrlPath, action string, line int, nsStack []string, out *ParseResult) {
	path := strings.Trim(ctrlPath, "/")
	if path == "" {
		return
	}
	// Explicit admin/users in to: wins; otherwise prefix active namespace stack.
	if !strings.Contains(path, "/") && len(nsStack) > 0 {
		path = railsJoinPath(nsStack, path)
	}
	ctrl := railsControllerName(path)
	safe := strings.ReplaceAll(strings.ToLower(path), "/", "_")
	siteName := fmt.Sprintf("rails_%s_%s_%s_%d", verb, safe, action, line)
	sym := symbol(repoID, relPath, siteName, types.SymbolKindFunction, line, line, "ruby", fw+";role=entrypoint", "")
	out.Symbols = append(out.Symbols, sym)
	out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	emitRubyCall(repoID, relPath, sym.ID, ctrl, 0.9, out)
	emitRubyCall(repoID, relPath, sym.ID, action, 0.85, out)
	railsEmitPathModules(repoID, relPath, sym.ID, path, ctrl, out)
}

func railsEmitPathModules(repoID, relPath, fromSym, path, ctrl string, out *ParseResult) {
	leaf := strings.TrimSuffix(strings.ToLower(ctrl), "controller")
	for _, part := range strings.Split(path, "/") {
		if part == "" || strings.EqualFold(part, leaf) {
			continue
		}
		emitRubyCall(repoID, relPath, fromSym, railsCamelize(part), 0.7, out)
	}
}

func railsJoinPath(ns []string, leaf string) string {
	leaf = strings.Trim(leaf, "/")
	if len(ns) == 0 {
		return leaf
	}
	parts := append([]string{}, ns...)
	parts = append(parts, leaf)
	return strings.Join(parts, "/")
}

func railsControllerName(resource string) string {
	s := strings.TrimSpace(resource)
	if s == "" {
		return "ApplicationController"
	}
	// users → UsersController; user → UsersController (resources plural)
	parts := strings.Split(s, "/")
	leaf := parts[len(parts)-1]
	return railsCamelize(leaf) + "Controller"
}

func railsModelName(assoc string) string {
	s := strings.TrimSpace(assoc)
	if s == "" {
		return ""
	}
	// posts → Post; user → User (naive singularize: strip trailing s)
	if len(s) > 1 && strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") {
		s = s[:len(s)-1]
	}
	return railsCamelize(s)
}

func railsCamelize(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		if len(p) > 1 {
			b.WriteString(p[1:])
		}
	}
	return b.String()
}

func emitRubyCall(repoID, relPath, fromSym, name string, conf float64, out *ParseResult) {
	name = strings.TrimSpace(name)
	if name == "" || !isCallableName(name) {
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

// rubyRequireModule extracts the module path from require / require_relative / load.
func rubyRequireModule(n *sitter.Node, buf []byte) string {
	if n == nil || n.Type() != "call" {
		return ""
	}
	ident := n.Child(0)
	if ident == nil || ident.Type() != "identifier" {
		return ""
	}
	fn := strings.TrimSpace(ident.Content(buf))
	switch fn {
	case "require", "require_relative", "load":
	default:
		return ""
	}
	var args *sitter.Node
	if a := n.ChildByFieldName("arguments"); a != nil {
		args = a
	} else {
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && c.Type() == "argument_list" {
				args = c
				break
			}
		}
	}
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.ChildCount()); i++ {
		c := args.Child(i)
		if c == nil {
			continue
		}
		if mod := rubyStringContent(c, buf); mod != "" {
			return mod
		}
	}
	return ""
}

// rubyMixinModule returns the module constant for include/extend/prepend calls.
func rubyMixinModule(n *sitter.Node, buf []byte) string {
	if n == nil || n.Type() != "call" {
		return ""
	}
	ident := n.Child(0)
	if ident == nil || ident.Type() != "identifier" {
		return ""
	}
	fn := strings.TrimSpace(ident.Content(buf))
	switch fn {
	case "include", "extend", "prepend":
	default:
		return ""
	}
	var args *sitter.Node
	if a := n.ChildByFieldName("arguments"); a != nil {
		args = a
	} else {
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && c.Type() == "argument_list" {
				args = c
				break
			}
		}
	}
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		tok := strings.TrimSpace(c.Content(buf))
		if i := strings.LastIndex(tok, "::"); i >= 0 {
			tok = tok[i+2:]
		}
		if tok == "" || tok[0] < 'A' || tok[0] > 'Z' {
			continue
		}
		switch tok {
		case "Kernel", "Object", "Module", "Class", "BasicObject", "Enumerable":
			return ""
		}
		return tok
	}
	return ""
}

// rubyEmitSuperclass emits an inherits edge for `class Foo < Bar`.
func rubyEmitSuperclass(n *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	tok := rubySuperclassName(n, buf)
	if tok == "" || out == nil {
		return
	}
	tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, tok)
	out.Edges = append(out.Edges, types.Reference{
		ID:         edgeID(repoID, fromSym, tgt, "inherits"),
		RepoID:     repoID,
		Kind:       types.RefKindInherits,
		SourceID:   fromSym,
		TargetID:   tgt,
		Confidence: 0.9,
	})
}

// rubySuperclassName returns the bare superclass leaf for `class Foo < Bar`.
func rubySuperclassName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	var sc *sitter.Node
	if s := n.ChildByFieldName("superclass"); s != nil {
		sc = s
	} else {
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && c.Type() == "superclass" {
				sc = c
				break
			}
		}
	}
	if sc == nil {
		return ""
	}
	tok := strings.TrimSpace(sc.Content(buf))
	tok = strings.TrimPrefix(tok, "<")
	tok = strings.TrimSpace(tok)
	if i := strings.LastIndex(tok, "::"); i >= 0 {
		tok = tok[i+2:]
	}
	if tok == "" || tok[0] < 'A' || tok[0] > 'Z' {
		return ""
	}
	return tok
}

func rubyStringContent(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	if n.Type() == "string_content" {
		return strings.TrimSpace(n.Content(buf))
	}
	if n.Type() == "string" {
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && c.Type() == "string_content" {
				return strings.TrimSpace(c.Content(buf))
			}
		}
	}
	return ""
}

// rubyEmitConstantReads emits reads for Constant / Foo::Bar references so class
// inbound works when methods only call through constants (Sinatra helpers, etc.).
func rubyEmitConstantReads(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	seen := map[string]bool{}
	Walk(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		var tok string
		switch n.Type() {
		case "constant":
			tok = strings.TrimSpace(n.Content(buf))
		case "scope_resolution":
			// Foo::Bar — take the rightmost constant.
			tok = strings.TrimSpace(n.Content(buf))
			if i := strings.LastIndex(tok, "::"); i >= 0 {
				tok = tok[i+2:]
			}
		default:
			return
		}
		if tok == "" || tok[0] < 'A' || tok[0] > 'Z' || seen[tok] {
			return
		}
		switch tok {
		case "TrueClass", "FalseClass", "NilClass", "Object", "Class", "Module",
			"String", "Integer", "Float", "Array", "Hash", "Symbol", "Proc",
			"Enumerable", "Kernel", "BasicObject":
			return
		}
		seen[tok] = true
		tgt := "symref:" + repoID + ":" + relPath + ":" + tok
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.6,
		})
	})
}
