package parser

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Clojure has no tree-sitter binding in smacker/go-tree-sitter; this is a
// line-oriented lite extractor: ns/defn/macros/types, require/use imports,
// and heuristic call edges inside defn bodies.
// Confidence: Low / lite — prefer lexical query; empty fanout ≠ isolation.

var (
	reClojureNS       = regexp.MustCompile(`(?m)^\s*\(\s*ns\s+([\w.+/!?*-]+)`)
	reClojureDefn     = regexp.MustCompile(`(?m)^\s*\(\s*(defn-?|defmacro|defmethod)\s+([\w.+/!?*-]+)`)
	reClojureType     = regexp.MustCompile(`(?m)^\s*\(\s*(defprotocol|defrecord|deftype|definterface|defmulti)\s+([\w.+/!?*-]+)`)
	reClojureDef      = regexp.MustCompile(`(?m)^\s*\(\s*def\s+(?:\^[^\s]+\s+)?([\w.+/!?*-]+)`)
	reClojureRequire  = regexp.MustCompile(`(?m)\[\s*([\w]+\.[\w.]+)`)
	reClojureRequireL = regexp.MustCompile(`(?m)^\s*\(\s*(?:require|use)\s+'?\[?([\w.]+)`)
	reClojureRequireK = regexp.MustCompile(`(?m):\s*require\b`)
	reClojureCall     = regexp.MustCompile(`\(\s*([A-Za-z_*!?+-][\w*!?+-]*)`)
)

var clojureKeywordSkip = map[string]struct{}{
	"ns": {}, "defn": {}, "defn-": {}, "defmacro": {}, "defmethod": {},
	"def": {}, "defprotocol": {}, "defrecord": {}, "deftype": {},
	"definterface": {}, "defmulti": {}, "declare": {}, "if": {}, "when": {},
	"when-not": {}, "when-let": {}, "if-let": {}, "let": {}, "loop": {},
	"recur": {}, "do": {}, "fn": {}, "quote": {}, "var": {}, "new": {},
	"throw": {}, "try": {}, "catch": {}, "finally": {}, "case": {}, "cond": {},
	"and": {}, "or": {}, "not": {}, "nil?": {}, "some?": {}, "map": {},
	"reduce": {}, "filter": {}, "into": {}, "assoc": {}, "get": {}, "str": {},
	"require": {}, "use": {}, "import": {}, "refer": {}, "in-ns": {},
	"true": {}, "false": {}, "nil": {},
}

func parseClojureLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	lines := strings.Split(string(buf), "\n")
	line := 0
	var currentFuncID string
	nsName := clojureFileStem(relPath)

	for _, ln := range lines {
		line++
		ln = strings.TrimRight(ln, "\r")
		trim := strings.TrimSpace(ln)

		if m := reClojureNS.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, line, line, "clojure", "", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			nsName = name
			currentFuncID = ""
			// (ns … (:require [foo.bar :as fb] …))
			for _, rm := range reClojureRequire.FindAllStringSubmatch(ln, -1) {
				mod := rm[1]
				if mod == "" || strings.EqualFold(mod, name) {
					continue
				}
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.8,
				})
			}
			continue
		}

		if m := reClojureRequireL.FindStringSubmatch(ln); len(m) > 1 {
			mod := m[1]
			if mod != "" {
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.8,
				})
			}
			continue
		}

		// (:require [demo.helpers :as h]) / :require vectors inside ns forms.
		if reClojureRequireK.MatchString(ln) || strings.Contains(ln, ":require") {
			for _, rm := range reClojureRequire.FindAllStringSubmatch(ln, -1) {
				mod := rm[1]
				if mod == "" {
					continue
				}
				out.Imports = append(out.Imports, mod)
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, fid, moduleNodeID(repoID, mod), "imports"),
					RepoID:     repoID,
					Kind:       types.RefKindImports,
					SourceID:   fid,
					TargetID:   moduleNodeID(repoID, mod),
					Confidence: 0.8,
				})
			}
			continue
		}

		if m := reClojureType.FindStringSubmatch(ln); len(m) > 2 {
			name := m[2]
			kind := types.SymbolKindClass
			if m[1] == "defprotocol" || m[1] == "definterface" {
				kind = types.SymbolKindInterface
			}
			sym := symbol(repoID, relPath, name, kind, line, line, "clojure", "", nsName)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = ""
			continue
		}

		if m := reClojureDefn.FindStringSubmatch(ln); len(m) > 2 {
			name := m[2]
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "clojure", "", nsName)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			currentFuncID = sym.ID
			continue
		}

		if m := reClojureDef.FindStringSubmatch(ln); len(m) > 1 {
			name := m[1]
			if _, skip := clojureKeywordSkip[name]; !skip {
				sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "clojure", "", nsName)
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			}
			currentFuncID = ""
			continue
		}

		if currentFuncID != "" && trim != "" && !strings.HasPrefix(trim, ";") {
			emitClojureCalls(repoID, relPath, currentFuncID, ln, out)
		}
	}
	return out, nil
}

func emitClojureCalls(repoID, relPath, fromSym, ln string, out *ParseResult) {
	for _, m := range reClojureCall.FindAllStringSubmatch(ln, -1) {
		name := m[1]
		if name == "" {
			continue
		}
		if _, skip := clojureKeywordSkip[name]; skip {
			continue
		}
		if !isCallableName(name) {
			continue
		}
		tgt := "symref:" + repoID + ":" + relPath + ":" + name
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "calls"),
			RepoID:     repoID,
			Kind:       types.RefKindCalls,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.4,
		})
	}
}

func clojureFileStem(relPath string) string {
	base := filepath.Base(relPath)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return strings.ReplaceAll(base, "_", "-")
}
