package indexer

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// LoadGitExcludeMatcher loads root `.gitignore` and `.git/info/exclude` (same rules as tracked-by-git exclusions at repo root).
// Nested `.gitignore` files are not layered yet — patterns apply to paths relative to the git work tree root.
func LoadGitExcludeMatcher(gitRoot string) (*gitignore.GitIgnore, error) {
	gitRoot = filepath.Clean(gitRoot)
	var chunks []string
	paths := []string{
		filepath.Join(gitRoot, ".gitignore"),
		filepath.Join(gitRoot, ".git", "info", "exclude"),
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			chunks = append(chunks, line)
		}
	}
	if len(chunks) == 0 {
		return nil, nil
	}
	return gitignore.CompileIgnoreLines(chunks...), nil
}

// GitIgnoreSkipFunc returns a skip callback for WalkSourceFiles: rel is relative to indexRoot (POSIX slashes).
func GitIgnoreSkipFunc(gitRoot, indexRoot string, gi *gitignore.GitIgnore) func(rel string) bool {
	if gi == nil {
		return nil
	}
	gitRoot = filepath.Clean(gitRoot)
	indexRoot = filepath.Clean(indexRoot)
	return func(rel string) bool {
		rel = filepath.ToSlash(rel)
		abs := filepath.Join(indexRoot, filepath.FromSlash(rel))
		repoRel, err := filepath.Rel(gitRoot, abs)
		if err != nil {
			return false
		}
		repoRel = filepath.ToSlash(repoRel)
		if repoRel == "." {
			return false
		}
		return gi.MatchesPath(repoRel)
	}
}

// collectNestedGitignorePaths lists .gitignore files under gitRoot, pruning
// directories that are well-known noise (defaultSkipDirs) or that prune()
// reports as ignored by the root rules. Pruning matters for correctness AND
// speed: without it the collector descends into gitignored fixture trees (a
// vendored linux/kubernetes checkout under .testbeds/, say) and pulls their
// thousands of nested .gitignore patterns into the layered matcher — bloating
// it until matching catastrophically backtracks. We never index those trees, so
// their ignore rules are irrelevant.
func collectNestedGitignorePaths(gitRoot string, prune func(absDir string) bool) ([]string, error) {
	var out []string
	err := filepath.WalkDir(gitRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if _, ok := defaultSkipDirs[d.Name()]; ok {
				return filepath.SkipDir
			}
			if prune != nil && filepath.Clean(path) != gitRoot && prune(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".gitignore" {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

// LoadLayeredGitIgnoreMatcher loads root rules plus every nested `.gitignore` by prefixing patterns with their directory
// relative to git root (approximation of git behavior for path-only patterns).
func LoadLayeredGitIgnoreMatcher(gitRoot string) (*gitignore.GitIgnore, error) {
	gitRoot = filepath.Clean(gitRoot)
	var lines []string
	rootGI := filepath.Join(gitRoot, ".gitignore")
	if b, err := os.ReadFile(rootGI); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			lines = append(lines, line)
		}
	}
	if b, err := os.ReadFile(filepath.Join(gitRoot, ".git", "info", "exclude")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			lines = append(lines, line)
		}
	}
	// Build a root-only matcher from the lines gathered so far (root .gitignore +
	// .git/info/exclude) and use it to prune ignored directories while collecting
	// nested .gitignores — so a gitignored fixture tree never contributes its rules.
	var rootMatcher *gitignore.GitIgnore
	if len(lines) > 0 {
		rootMatcher = gitignore.CompileIgnoreLines(lines...)
	}
	prune := func(absDir string) bool {
		if rootMatcher == nil {
			return false
		}
		rel, err := filepath.Rel(gitRoot, absDir)
		if err != nil {
			return false
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return false
		}
		return rootMatcher.MatchesPath(rel) || rootMatcher.MatchesPath(rel+"/")
	}
	nested, err := collectNestedGitignorePaths(gitRoot, prune)
	if err != nil {
		return nil, err
	}
	for _, abs := range nested {
		if filepath.Clean(abs) == rootGI {
			continue
		}
		relDir, err := filepath.Rel(gitRoot, filepath.Dir(abs))
		if err != nil {
			continue
		}
		relDir = filepath.ToSlash(relDir)
		b, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			neg := strings.HasPrefix(line, "!")
			raw := line
			if neg {
				raw = strings.TrimPrefix(raw, "!")
			}
			pref := prefixNestedPattern(relDir, raw)
			if neg {
				pref = "!" + pref
			}
			lines = append(lines, pref)
		}
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return gitignore.CompileIgnoreLines(lines...), nil
}

func prefixNestedPattern(relDir, pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return pattern
	}
	// Leave include-from-negation and comments already stripped.
	if strings.HasPrefix(pattern, "/") {
		// Anchored within this subdirectory.
		tail := strings.TrimPrefix(pattern, "/")
		if relDir == "." {
			return tail
		}
		return relDir + "/" + tail
	}
	if strings.Contains(pattern, "/") {
		// Already path-qualified within subdir semantics — prefix directory.
		if relDir == "." {
			return pattern
		}
		return relDir + "/" + pattern
	}
	// Pathname-less pattern: applies at any depth under this .gitignore's directory (use **).
	if relDir == "." {
		return "**/" + pattern
	}
	return relDir + "/**/" + pattern
}
