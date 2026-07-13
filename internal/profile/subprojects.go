package profile

import (
	"os"
	"path/filepath"
	"strings"
)

const maxSubProjects = 16

// subProjectSkip are directory names never treated as (or descended for)
// sub-projects: vendored deps, VCS, engine caches, and codehelper's own dir.
var subProjectSkip = map[string]bool{
	"node_modules": true, "vendor": true, ".vendor": true, ".git": true, ".codehelper": true,
	"Library": true, "Temp": true, "PackageCache": true, "Binaries": true, "Intermediate": true,
	"dist": true, "build": true, "target": true, "__pycache__": true, ".venv": true, "venv": true,
}

// monorepoContainers are conventional directories that hold multiple sub-projects;
// their immediate children are scanned too (apps/web, packages/ui, crates/core …).
var monorepoContainers = map[string]bool{
	"apps": true, "packages": true, "services": true, "crates": true,
	"projects": true, "modules": true, "libs": true, "components": true,
}

// detectSubProjects finds self-contained nested stacks (a monorepo's frontend/,
// backend/, packages/*, etc.) and detects each one's stack independently, so a
// multi-stack repo reports all its parts rather than collapsing to the root.
func detectSubProjects(repoRoot string) []SubProject {
	skip := map[string]bool{}
	for k := range subProjectSkip {
		skip[k] = true
	}
	for k := range gitignoreSimpleDirs(repoRoot) {
		skip[k] = true
	}

	var dirs []string
	add := func(dir string) {
		if dirHasManifest(dir) {
			dirs = append(dirs, dir)
		}
	}
	entries, _ := os.ReadDir(repoRoot)
	for _, e := range entries {
		if !e.IsDir() || skip[e.Name()] || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(repoRoot, e.Name())
		add(child)
		if monorepoContainers[e.Name()] {
			sub, _ := os.ReadDir(child)
			for _, s := range sub {
				if s.IsDir() && !skip[s.Name()] && !strings.HasPrefix(s.Name(), ".") {
					add(filepath.Join(child, s.Name()))
				}
			}
		}
	}

	var out []SubProject
	for _, dir := range dirs {
		if len(out) >= maxSubProjects {
			break
		}
		p, err := generateProfile(dir, false) // no recursion into sub-sub-projects
		if err != nil || p.ProjectType == "" || p.ProjectType == "unknown" {
			continue
		}
		rel, _ := filepath.Rel(repoRoot, dir)
		out = append(out, SubProject{
			Path:            filepath.ToSlash(rel),
			ProjectType:     p.ProjectType,
			Framework:       p.Framework,
			Version:         p.Version,
			PrimaryLanguage: p.PrimaryLanguage,
		})
	}
	return out
}

// dirHasManifest reports whether a directory looks like its own project root.
func dirHasManifest(dir string) bool {
	for _, m := range []string{
		"go.mod", "package.json", "composer.json", "Cargo.toml",
		"pyproject.toml", "requirements.txt", "setup.py",
		"project.godot", "Gemfile", "pom.xml", "build.gradle",
	} {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "ProjectSettings", "ProjectVersion.txt")); err == nil {
		return true
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.uproject")); len(m) > 0 {
		return true
	}
	return false
}
