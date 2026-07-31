package retrieval

import (
	"os"
	"path/filepath"
	"strings"
)

// discoverLikelyEntrypoints returns repo-root bootstrap files used to boost locate-
// style queries (main plugin file, main.go, extension entry, …).
func discoverLikelyEntrypoints(root string) []string {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	candidates := []string{
		"cmd/codehelper/main.go",
		"cmd/codehelper-mcp/main.go",
		"cmd/main.go",
		"main.go",
		"index.ts",
		"index.js",
		"vscode-extension/src/extension.ts",
		"internal/mcpsvc/register.go",
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	add := func(rel string) {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" {
			return
		}
		if _, ok := seen[rel]; ok {
			return
		}
		p := filepath.Join(root, filepath.FromSlash(rel))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			seen[rel] = struct{}{}
			out = append(out, rel)
		}
	}
	for _, c := range candidates {
		add(c)
	}
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			lower := strings.ToLower(e.Name())
			if strings.HasSuffix(lower, ".php") && lower != "uninstall.php" {
				add(e.Name())
			}
		}
	}
	return out
}

func queryMentionsLocate(toks []string) bool {
	for _, t := range toks {
		switch t {
		case "entry", "entrypoint", "startup", "bootstrap", "main", "hooks",
			"hook", "registered", "plugins_loaded", "autoload", "init", "activate":
			return true
		}
	}
	return false
}

func queryMentionsVocabExpansion(toks []string) bool {
	for _, t := range toks {
		switch t {
		case "vocabulary", "vocab", "terms", "jargon", "seed", "glossary":
			return true
		}
	}
	return false
}

func queryMentionsSemanticEmbed(toks []string) bool {
	hasEmbed := false
	hasRerank := false
	for _, t := range toks {
		switch t {
		case "embedding", "embed", "embeddings", "multilingual", "cosine", "semantic":
			hasEmbed = true
		case "rerank", "reranking":
			hasRerank = true
		}
	}
	return hasEmbed && hasRerank
}

func queryMentionsSimilarSearch(toks []string) bool {
	hasSimilar := false
	for _, t := range toks {
		switch t {
		case "similar", "similarity", "overlap", "signature":
			hasSimilar = true
		}
	}
	return hasSimilar
}

func pathMatchesEntrypointFile(symPath string, files []string) bool {
	symPath = strings.ToLower(filepath.ToSlash(symPath))
	for _, ep := range files {
		ep = strings.ToLower(filepath.ToSlash(strings.TrimSpace(ep)))
		if ep != "" && strings.HasSuffix(symPath, ep) {
			return true
		}
	}
	return false
}

func containsToken(toks []string, want string) bool {
	want = strings.ToLower(want)
	for _, t := range toks {
		if strings.ToLower(t) == want {
			return true
		}
	}
	return false
}

func queryMentionsAdminDiagnostics(toks []string) bool {
	for _, t := range toks {
		switch t {
		case "health", "diagnostics", "diagnostic", "checks", "admin":
			return true
		}
	}
	return false
}
