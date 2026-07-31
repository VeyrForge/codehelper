package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	bash "github.com/smacker/go-tree-sitter/bash"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// bashSkipCommand filters shell builtins and common CLI noise from call edges
// so extractCalls only keeps likely user-defined helpers.
func bashSkipCommand(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || strings.ContainsAny(n, "/\\") {
		return true
	}
	switch n {
	case "echo", "printf", "read", "cd", "pwd", "export", "unset", "set", "shift",
		"test", "[", "[[", ":", "true", "false", "exit", "return", "break", "continue",
		"source", ".", "eval", "exec", "command", "builtin", "type", "hash", "help",
		"alias", "unalias", "local", "declare", "typeset", "readonly", "let", "mapfile",
		"wait", "jobs", "fg", "bg", "kill", "trap", "ulimit", "umask", "getopts",
		"pushd", "popd", "dirs", "shopt", "complete", "compgen", "caller",
		"ls", "cat", "grep", "sed", "awk", "cut", "tr", "sort", "uniq", "head", "tail",
		"find", "xargs", "cp", "mv", "rm", "mkdir", "rmdir", "chmod", "chown", "touch",
		"curl", "wget", "git", "docker", "npm", "yarn", "pnpm", "go", "make", "cmake",
		"python", "python3", "pip", "node", "ruby", "perl", "php", "java", "javac",
		"sudo", "ssh", "scp", "rsync", "tar", "zip", "unzip", "jq", "yq":
		return true
	}
	return false
}

// ParseBash extracts function definitions and command call edges from bodies
// (user-defined helpers; common builtins/CLIs are filtered as noise).
func ParseBash(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(bash.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	Walk(tree.RootNode(), func(n *sitter.Node) {
		if n.Type() != "function_definition" {
			return
		}
		name := ChildName(n, "name", buf)
		if name == "" {
			name = FirstIdentifier(n.ChildByFieldName("name"), buf)
		}
		if name == "" {
			return
		}
		sym := symbol(repoID, relPath, name, types.SymbolKindFunction, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "bash", "", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		extractCalls(n, buf, repoID, relPath, sym.ID, out)
	})
	return out, nil
}
