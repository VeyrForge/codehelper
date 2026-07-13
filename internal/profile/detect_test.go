package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLanguageStatsAndFallback(t *testing.T) {
	t.Run("no manifest falls back to dominant language, not unknown", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a.py", strings.Repeat("x = 1\n", 200)) // dominant
		writeFile(t, dir, "b.lua", "return 1\n")
		p, err := Generate(dir)
		if err != nil {
			t.Fatal(err)
		}
		if p.ProjectType != "python" {
			t.Errorf("ProjectType = %q, want python (dominant-language fallback)", p.ProjectType)
		}
		if p.PrimaryLanguage != "python" {
			t.Errorf("PrimaryLanguage = %q, want python", p.PrimaryLanguage)
		}
		if len(p.LanguageStats) == 0 || p.LanguageStats[0].Language != "python" {
			t.Fatalf("expected python as top language stat, got %+v", p.LanguageStats)
		}
		// Percentages are bytes-based and sum to ~100.
		var sum float64
		for _, s := range p.LanguageStats {
			sum += s.Percent
		}
		if sum < 99 || sum > 101 {
			t.Errorf("language percentages sum = %.1f, want ~100", sum)
		}
	})

	t.Run("truly empty repo stays unknown", func(t *testing.T) {
		p, _ := Generate(t.TempDir())
		if p.ProjectType != "unknown" {
			t.Errorf("ProjectType = %q, want unknown for an empty repo", p.ProjectType)
		}
	})
}

func TestGenerateTypeAndVersion(t *testing.T) {
	t.Run("godot overrides go and reports engine version", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "project.godot", "config_version=5\nconfig/features=PackedStringArray(\"4.7\", \"Mobile\")\n")
		writeFile(t, dir, "go.mod", "module game\n\ngo 1.25.0\n")
		p, err := Generate(dir)
		if err != nil {
			t.Fatal(err)
		}
		if p.ProjectType != "godot" {
			t.Errorf("ProjectType = %q, want godot", p.ProjectType)
		}
		if p.Version != "4.7" {
			t.Errorf("Version = %q, want 4.7", p.Version)
		}
		if p.Versions["go"] != "1.25.0" {
			t.Errorf("Versions[go] = %q, want 1.25.0 (secondary stack kept)", p.Versions["go"])
		}
	})

	t.Run("unity", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "ProjectSettings/ProjectVersion.txt", "m_EditorVersion: 6000.4.11f1\n")
		p, _ := Generate(dir)
		if p.ProjectType != "unity" || p.Version != "6000.4.11f1" {
			t.Errorf("got %q/%q, want unity/6000.4.11f1", p.ProjectType, p.Version)
		}
	})

	t.Run("unreal", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "MyGame.uproject", `{"EngineAssociation":"5.4","Modules":[]}`)
		p, _ := Generate(dir)
		if p.ProjectType != "unreal" || p.Version != "5.4" {
			t.Errorf("got %q/%q, want unreal/5.4", p.ProjectType, p.Version)
		}
	})

	t.Run("laravel with php + framework versions", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "composer.json", `{"require":{"php":"^8.2","laravel/framework":"^12.0"}}`)
		writeFile(t, dir, "artisan", "#!/usr/bin/env php\n")
		p, _ := Generate(dir)
		if p.ProjectType != "laravel" {
			t.Errorf("ProjectType = %q, want laravel", p.ProjectType)
		}
		if p.Version != "12.0" {
			t.Errorf("Version = %q, want 12.0 (laravel framework)", p.Version)
		}
		if p.Versions["php"] != "8.2" {
			t.Errorf("Versions[php] = %q, want 8.2", p.Versions["php"])
		}
	})

	t.Run("go", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "go.mod", "module x\n\ngo 1.25.5\n")
		p, _ := Generate(dir)
		if p.ProjectType != "go" || p.Version != "1.25.5" {
			t.Errorf("got %q/%q, want go/1.25.5", p.ProjectType, p.Version)
		}
	})

	t.Run("react app resolves to react framework + version", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "package.json", `{"dependencies":{"react":"^18.2.0"}}`)
		p, _ := Generate(dir)
		if p.ProjectType != "react" || p.Framework != "react" || p.Version != "18.2.0" {
			t.Errorf("got type=%q framework=%q version=%q, want react/react/18.2.0", p.ProjectType, p.Framework, p.Version)
		}
	})

	t.Run("next.js wins over react", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "package.json", `{"dependencies":{"react":"^18.2.0","next":"^14.1.0"}}`)
		p, _ := Generate(dir)
		if p.ProjectType != "nextjs" || p.Version != "14.1.0" {
			t.Errorf("got %q/%q, want nextjs/14.1.0", p.ProjectType, p.Version)
		}
	})

	t.Run("wordpress plugin detected by header, not path", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "my-plugin.php", "<?php\n/**\n * Plugin Name: My Plugin\n * Version: 2.3.1\n */\n")
		p, _ := Generate(dir)
		if p.ProjectType != "wordpress_plugin" || p.Version != "2.3.1" || p.Framework != "wordpress" {
			t.Errorf("got type=%q framework=%q version=%q, want wordpress_plugin/wordpress/2.3.1", p.ProjectType, p.Framework, p.Version)
		}
	})

	t.Run("filament on laravel + tailwind library gotcha", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "composer.json", `{"require":{"laravel/framework":"^11.0","filament/filament":"^3.2"}}`)
		writeFile(t, dir, "artisan", "#!/usr/bin/env php\n")
		writeFile(t, dir, "package.json", `{"devDependencies":{"tailwindcss":"^3.4"}}`)
		p, _ := Generate(dir)
		if p.ProjectType != "laravel" || p.Framework != "filament" {
			t.Errorf("got type=%q framework=%q, want laravel/filament", p.ProjectType, p.Framework)
		}
		joined := strings.Join(p.Gotchas, "\n")
		if !strings.Contains(joined, "formatStateUsing") {
			t.Errorf("expected Filament gotcha, got: %v", p.Gotchas)
		}
		if !strings.Contains(joined, "config:clear") {
			t.Errorf("expected Laravel base gotcha alongside Filament, got: %v", p.Gotchas)
		}
		if !strings.Contains(joined, "purged") {
			t.Errorf("expected Tailwind library gotcha, got: %v", p.Gotchas)
		}
	})

	t.Run("wordpress child theme + asset-versioning gotcha", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "style.css", "/*\nTheme Name: My Child\nTemplate: parenttheme\nVersion: 1.0.0\n*/\n")
		writeFile(t, dir, "functions.php", "<?php\n")
		p, _ := Generate(dir)
		if p.ProjectType != "wordpress_child_theme" {
			t.Errorf("ProjectType = %q, want wordpress_child_theme", p.ProjectType)
		}
		joined := strings.Join(p.Gotchas, "\n")
		if !strings.Contains(joined, "filemtime") {
			t.Errorf("expected asset-versioning (filemtime) gotcha, got: %v", p.Gotchas)
		}
		if !strings.Contains(joined, "PARENT stylesheet") {
			t.Errorf("expected child-theme parent-enqueue gotcha, got: %v", p.Gotchas)
		}
	})

	t.Run("monorepo: frontend + backend reported as sub-projects", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "README.md", "# monorepo\n")
		writeFile(t, dir, "frontend/package.json", `{"dependencies":{"next":"^14.0.0"}}`)
		writeFile(t, dir, "frontend/app.tsx", "export default function App(){return null}\n")
		writeFile(t, dir, "backend/go.mod", "module api\n\ngo 1.22\n")
		writeFile(t, dir, "backend/main.go", "package main\nfunc main(){}\n")
		p, _ := Generate(dir)
		got := map[string]string{}
		for _, s := range p.SubProjects {
			got[s.Path] = s.ProjectType
		}
		if got["frontend"] != "nextjs" {
			t.Errorf("frontend sub-project = %q, want nextjs (subs=%+v)", got["frontend"], p.SubProjects)
		}
		if got["backend"] != "go" {
			t.Errorf("backend sub-project = %q, want go (subs=%+v)", got["backend"], p.SubProjects)
		}
	})

	t.Run("wordpress theme detected by style.css header", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "style.css", "/*\nTheme Name: My Theme\nVersion: 1.4.0\n*/\n")
		writeFile(t, dir, "index.php", "<?php\n")
		p, _ := Generate(dir)
		if p.ProjectType != "wordpress_theme" || p.Version != "1.4.0" {
			t.Errorf("got %q/%q, want wordpress_theme/1.4.0", p.ProjectType, p.Version)
		}
	})

	t.Run("laravel + dependencies extracted", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "composer.json", `{"require":{"php":"^8.2","laravel/framework":"^12.0","guzzlehttp/guzzle":"^7.8"}}`)
		writeFile(t, dir, "artisan", "#!/usr/bin/env php\n")
		p, _ := Generate(dir)
		if p.ProjectType != "laravel" || p.Framework != "laravel" {
			t.Errorf("got type=%q framework=%q, want laravel/laravel", p.ProjectType, p.Framework)
		}
		var sawGuzzle bool
		for _, d := range p.Dependencies {
			if d.Name == "guzzlehttp/guzzle" && d.Version == "7.8" && d.Ecosystem == "composer" {
				sawGuzzle = true
			}
		}
		if !sawGuzzle {
			t.Errorf("expected guzzle dependency extracted, got %+v", p.Dependencies)
		}
	})
}
