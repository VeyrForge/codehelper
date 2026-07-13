package profile

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// This file refines a PHP/Node/Python/Ruby/Java project's type into a concrete
// framework, and classifies WordPress projects by structure (site/plugin/theme)
// from file headers — NOT from the repo path, which previously caused a plugin
// living under a "wordpress/" directory to be mislabeled "wordpress_woocommerce".

var (
	wpPluginNameRe = regexp.MustCompile(`(?mi)^[ \t/*#]*Plugin Name:\s*(.+?)\s*$`)
	wpThemeNameRe  = regexp.MustCompile(`(?mi)^[ \t/*#]*Theme Name:\s*(.+?)\s*$`)
	wpTemplateRe   = regexp.MustCompile(`(?mi)^[ \t/*#]*Template:\s*(.+?)\s*$`) // present => child theme (names the parent)
	wpVersionRe    = regexp.MustCompile(`(?mi)^[ \t/*#]*Version:\s*(.+?)\s*$`)
	wpRequiresRe   = regexp.MustCompile(`(?mi)^[ \t/*#]*Requires at least:\s*(.+?)\s*$`)
	wpTestedRe     = regexp.MustCompile(`(?mi)^[ \t/*#]*Tested up to:\s*(.+?)\s*$`)
	wpReqPHPRe     = regexp.MustCompile(`(?mi)^[ \t/*#]*Requires PHP:\s*(.+?)\s*$`)
	wpCoreVerRe    = regexp.MustCompile(`\$wp_version\s*=\s*'([^']+)'`)
	wpWooRe        = regexp.MustCompile(`(?mi)WC (requires|tested)|^[ \t/*#]*Woo:`)
)

func readHead(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, n)
	r, _ := f.Read(buf)
	return string(buf[:r])
}

// detectWordPress classifies a WordPress project from on-disk markers. Returns
// ("",...) when the repo is not WordPress. subtype is one of wordpress_site,
// wordpress_plugin, wordpress_theme, wordpress_child_theme; woo is true when
// WooCommerce markers exist; wpVers carries WordPress/PHP version info that is
// actually determinable (else empty): the running core version for a site
// (version.php), or the Requires/Tested/Requires-PHP headers for a plugin/theme.
func detectWordPress(repoRoot string) (subtype, version string, woo bool, wpVers map[string]string) {
	has := func(rel string) bool {
		_, err := os.Stat(filepath.Join(repoRoot, rel))
		return err == nil
	}
	wpVers = map[string]string{}
	// A full WordPress install/site — the running core version is in version.php.
	if has("wp-config.php") || has("wp-load.php") || has("wp-settings.php") || has("wp-includes") || has("wp-admin") {
		core := ""
		if m := wpCoreVerRe.FindStringSubmatch(readHead(filepath.Join(repoRoot, "wp-includes", "version.php"), 8192)); m != nil {
			core = strings.TrimSpace(m[1])
			wpVers["wordpress"] = core
		}
		woo := has("wp-content/plugins/woocommerce") || has("wp-content/plugins/woocommerce/woocommerce.php")
		return "wordpress_site", core, woo, wpVers
	}
	// A theme: style.css with a "Theme Name:" header. A "Template:" header names a
	// parent theme — i.e. this is a CHILD theme (different enqueue/override rules).
	if css := readHead(filepath.Join(repoRoot, "style.css"), 8192); css != "" {
		if m := wpThemeNameRe.FindStringSubmatch(css); m != nil {
			sub := "wordpress_theme"
			if wpTemplateRe.MatchString(css) {
				sub = "wordpress_child_theme"
			}
			wpHeaderVersions(css, wpVers)
			return sub, headerVersion(css), wpWooRe.MatchString(css), wpVers
		}
	}
	// A plugin: a root-level PHP file with a "Plugin Name:" header.
	entries, _ := os.ReadDir(repoRoot)
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".php") {
			continue
		}
		head := readHead(filepath.Join(repoRoot, e.Name()), 8192)
		if wpPluginNameRe.MatchString(head) {
			wpHeaderVersions(head, wpVers)
			return "wordpress_plugin", headerVersion(head), wpWooRe.MatchString(head), wpVers
		}
	}
	return "", "", false, wpVers
}

// wpHeaderVersions extracts the WordPress/PHP compatibility versions a plugin or
// theme declares in its header (only those actually present).
func wpHeaderVersions(head string, into map[string]string) {
	if m := wpRequiresRe.FindStringSubmatch(head); m != nil {
		into["wordpress_requires"] = cleanVer(m[1])
	}
	if m := wpTestedRe.FindStringSubmatch(head); m != nil {
		into["wordpress_tested"] = cleanVer(m[1])
	}
	if m := wpReqPHPRe.FindStringSubmatch(head); m != nil {
		into["php"] = cleanVer(m[1])
	}
}

func headerVersion(head string) string {
	if m := wpVersionRe.FindStringSubmatch(head); m != nil {
		return cleanVer(m[1])
	}
	return ""
}

// detectFramework refines p.ProjectType into a concrete framework and sets
// p.Framework (+ a framework version in p.Versions) from the project's manifests
// and structure. It runs only when no game engine already claimed the type.
func detectFramework(repoRoot string, p *ProjectProfile, deps []Dependency) {
	has := func(rel string) bool {
		_, err := os.Stat(filepath.Join(repoRoot, rel))
		return err == nil
	}
	// Index deps by name for quick lookup, keyed within ecosystem.
	depVer := map[string]string{}
	for _, d := range deps {
		depVer[d.Ecosystem+":"+d.Name] = d.Version
	}
	set := func(framework, projectType, version string) {
		p.Framework = framework
		if projectType != "" {
			p.ProjectType = projectType
		}
		if version != "" {
			p.Versions[framework] = version
		}
	}

	// --- WordPress (site / plugin / theme) --- detected from file headers, so it
	// works for a plugin or theme that has no composer.json at all.
	if sub, ver, woo, wpVers := detectWordPress(repoRoot); sub != "" {
		fw := "wordpress"
		if woo {
			fw = "woocommerce"
		}
		set(fw, sub, "") // do NOT store the plugin/theme version under versions["wordpress"]
		// The headline version is the plugin/theme's own version (or the site's core
		// version); the WordPress/PHP compatibility versions come from wpVers.
		p.Version = ver
		for k, v := range wpVers {
			if v != "" {
				p.Versions[k] = v
			}
		}
		if v := depVer["composer:php"]; v != "" {
			p.Versions["php"] = v
		}
		p.DangerZones = appendUniq(p.DangerZones, "auth", "nonces", "sanitization", "sql ($wpdb->prepare)", "capabilities")
		p.CodingRules = appendUniq(p.CodingRules,
			"Validate input.", "Sanitize before storage/use.", "Escape output late.",
			"Use nonces for state-changing requests.", "Use $wpdb->prepare for SQL.")
		return
	}

	// --- PHP frameworks ---
	if has("composer.json") || p.ProjectType == "php_composer" || p.ProjectType == "php" {
		if has("artisan") || depVer["composer:laravel/framework"] != "" {
			set("laravel", "laravel", depVer["composer:laravel/framework"])
			// Stacked Laravel frameworks claim the more specific framework label
			// (type stays laravel, so both rule sets surface).
			switch {
			case depVer["composer:filament/filament"] != "":
				p.Framework = "filament"
				p.Versions["filament"] = depVer["composer:filament/filament"]
			case depVer["composer:livewire/livewire"] != "":
				p.Framework = "livewire"
				p.Versions["livewire"] = depVer["composer:livewire/livewire"]
			}
			return
		}
		if has("bin/console") || depVer["composer:symfony/framework-bundle"] != "" {
			set("symfony", "symfony", depVer["composer:symfony/framework-bundle"])
			return
		}
	}

	// --- Node ---
	if has("package.json") {
		switch {
		case depVer["npm:next"] != "":
			set("nextjs", "nextjs", depVer["npm:next"])
		case depVer["npm:nuxt"] != "":
			set("nuxt", "nuxt", depVer["npm:nuxt"])
		case depVer["npm:@sveltejs/kit"] != "":
			set("sveltekit", "sveltekit", depVer["npm:@sveltejs/kit"])
		case depVer["npm:@angular/core"] != "":
			set("angular", "angular", depVer["npm:@angular/core"])
		case depVer["npm:@nestjs/core"] != "":
			set("nestjs", "nestjs", depVer["npm:@nestjs/core"])
		case depVer["npm:astro"] != "":
			set("astro", "astro", depVer["npm:astro"])
		case depVer["npm:gatsby"] != "":
			set("gatsby", "gatsby", depVer["npm:gatsby"])
		case depVer["npm:@remix-run/react"] != "":
			set("remix", "remix", depVer["npm:@remix-run/react"])
		case depVer["npm:vue"] != "":
			set("vue", "vue", depVer["npm:vue"])
		case depVer["npm:svelte"] != "":
			set("svelte", "svelte", depVer["npm:svelte"])
		case depVer["npm:react"] != "":
			set("react", "react", depVer["npm:react"])
		case depVer["npm:express"] != "":
			set("express", "", depVer["npm:express"]) // keep node type; express is a lib
		}
	}

	// --- Python ---
	if has("requirements.txt") || has("pyproject.toml") || has("setup.py") || has("manage.py") {
		switch {
		case has("manage.py") || depVer["pip:django"] != "" || depVer["pip:Django"] != "":
			set("django", "django", firstNonEmpty(depVer["pip:django"], depVer["pip:Django"]))
		case depVer["pip:fastapi"] != "":
			set("fastapi", "fastapi", depVer["pip:fastapi"])
		case depVer["pip:flask"] != "" || depVer["pip:Flask"] != "":
			set("flask", "flask", firstNonEmpty(depVer["pip:flask"], depVer["pip:Flask"]))
		}
	}

	// --- Ruby ---
	if has("Gemfile") && (depVer["rubygems:rails"] != "" || has("bin/rails")) {
		set("rails", "rails", depVer["rubygems:rails"])
	}

	// --- Java (Spring Boot) ---
	if has("pom.xml") || has("build.gradle") || has("build.gradle.kts") {
		for _, d := range deps {
			if d.Ecosystem == "maven" && strings.Contains(d.Name, "spring-boot") {
				set("spring-boot", "spring", d.Version)
				break
			}
		}
		if p.Framework == "" {
			if c := readHead(filepath.Join(repoRoot, "build.gradle"), 8192) + readHead(filepath.Join(repoRoot, "build.gradle.kts"), 8192); strings.Contains(c, "spring-boot") {
				set("spring-boot", "spring", "")
			}
		}
	}
}

func appendUniq(list []string, items ...string) []string {
	seen := map[string]bool{}
	for _, s := range list {
		seen[s] = true
	}
	for _, s := range items {
		if !seen[s] {
			list = append(list, s)
			seen[s] = true
		}
	}
	return list
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
