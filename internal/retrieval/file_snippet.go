package retrieval

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
)

// FileSnippet is a non-symbol text hit — config keys, env vars, prose in README,
// or other file content that never became a symbol row.
type FileSnippet struct {
	Path  string  `json:"path"`
	Line  int     `json:"line"`
	Text  string  `json:"text"`
	Score float64 `json:"score,omitempty"`
}

const (
	maxSnippetFiles   = 20
	maxLinesPerFile   = 250
	maxSnippetLineLen = 240
)

// SearchFileSnippets scans indexed file paths (and their on-disk content) for query
// terms when symbol search returns too few hits. Bounded: at most maxSnippetFiles
// files, maxLinesPerFile lines each — never a full-repo grep.
func SearchFileSnippets(ctx context.Context, st *graph.Store, repoRoot, repoID string, terms []string, limit int) ([]FileSnippet, error) {
	if limit <= 0 {
		limit = 8
	}
	repoRoot = filepath.Clean(repoRoot)
	terms = meaningfulSnippetTerms(terms)
	if len(terms) == 0 {
		return nil, nil
	}
	paths, err := st.SearchFilePaths(ctx, repoID, terms, maxSnippetFiles*2)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		paths, err = st.ListFilesBySuffix(ctx, repoID, []string{".yaml", ".yml", ".json", ".toml", ".ini", ".env", ".md", ".cfg", ".conf"}, maxSnippetFiles*2)
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(paths, func(i, j int) bool {
		return snippetPathPriority(paths[i]) > snippetPathPriority(paths[j])
	})
	if len(paths) > maxSnippetFiles {
		paths = paths[:maxSnippetFiles]
	}

	var out []FileSnippet
	for _, p := range paths {
		if len(out) >= limit {
			break
		}
		abs := filepath.Join(repoRoot, filepath.FromSlash(p))
		f, err := os.Open(abs)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		lineNo := 0
		for sc.Scan() && lineNo < maxLinesPerFile && len(out) < limit {
			lineNo++
			line := sc.Text()
			score, ok := snippetLineScore(line, terms)
			if !ok {
				continue
			}
			text := strings.TrimSpace(line)
			if len(text) > maxSnippetLineLen {
				text = text[:maxSnippetLineLen] + "…"
			}
			out = append(out, FileSnippet{Path: p, Line: lineNo, Text: text, Score: score})
		}
		_ = f.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func meaningfulSnippetTerms(terms []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, t := range terms {
		t = strings.ToLower(strings.TrimSpace(t))
		if len(t) < 3 || IsCommonWord(t) {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func snippetPathPriority(path string) int {
	low := strings.ToLower(path)
	switch {
	case strings.Contains(low, "config"), strings.HasSuffix(low, ".env"), strings.HasSuffix(low, ".yaml"), strings.HasSuffix(low, ".yml"), strings.HasSuffix(low, ".toml"), strings.HasSuffix(low, ".ini"), strings.HasSuffix(low, ".json"):
		return 3
	case strings.HasSuffix(low, ".md"), strings.Contains(low, "readme"):
		return 2
	default:
		return 1
	}
}

func snippetLineScore(line string, terms []string) (float64, bool) {
	low := strings.ToLower(line)
	hits := 0
	for _, t := range terms {
		if strings.Contains(low, t) {
			hits++
		}
	}
	if hits == 0 {
		return 0, false
	}
	return float64(hits) / float64(len(terms)), true
}
