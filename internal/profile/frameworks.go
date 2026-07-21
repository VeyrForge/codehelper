package profile

import (
	"encoding/json"
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
		nestVer, isNest := detectNestJS(repoRoot, depVer)
		vueVer, isVue := detectVue(repoRoot, depVer)
		remixVer, isRemix := detectRemix(repoRoot, depVer)
		switch {
		case depVer["npm:next"] != "":
			set("nextjs", "nextjs", depVer["npm:next"])
		case depVer["npm:nuxt"] != "":
			set("nuxt", "nuxt", depVer["npm:nuxt"])
		case depVer["npm:@sveltejs/kit"] != "":
			set("sveltekit", "sveltekit", depVer["npm:@sveltejs/kit"])
		case depVer["npm:@angular/core"] != "":
			set("angular", "angular", depVer["npm:@angular/core"])
		case isNest:
			// Nest before express: @nestjs/* packages list express as a dep but
			// are Nest framework repos (root package.json often has no @nestjs/core dep).
			set("nestjs", "nestjs", nestVer)
		case depVer["npm:astro"] != "":
			set("astro", "astro", depVer["npm:astro"])
		case depVer["npm:gatsby"] != "":
			set("gatsby", "gatsby", depVer["npm:gatsby"])
		case isRemix:
			// Remix monorepos publish @remix-run/* packages without listing
			// @remix-run/react at the workspace root.
			set("remix", "remix", remixVer)
		case isVue:
			// Vue core monorepo names itself "vue" and lives under packages/vue.
			set("vue", "vue", vueVer)
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
		case pyProjectName(repoRoot) == "djangorestframework" || depVer["pip:djangorestframework"] != "" || has("rest_framework"):
			set("django-rest-framework", "django", firstNonEmpty(depVer["pip:djangorestframework"], depVer["pip:django"], depVer["pip:Django"]))
		case has("manage.py") || depVer["pip:django"] != "" || depVer["pip:Django"] != "":
			set("django", "django", firstNonEmpty(depVer["pip:django"], depVer["pip:Django"]))
		case depVer["pip:fastapi"] != "":
			set("fastapi", "fastapi", depVer["pip:fastapi"])
		case depVer["pip:flask"] != "" || depVer["pip:Flask"] != "":
			set("flask", "flask", firstNonEmpty(depVer["pip:flask"], depVer["pip:Flask"]))
		}
	}

	// --- Ruby ---
	if has("Gemfile") || gemspecPresent(repoRoot) {
		switch {
		case depVer["rubygems:rails"] != "" || has("bin/rails"):
			set("rails", "rails", depVer["rubygems:rails"])
		case depVer["rubygems:sinatra"] != "" || gemspecName(repoRoot) == "sinatra" || has("lib/sinatra.rb"):
			set("sinatra", "ruby", firstNonEmpty(depVer["rubygems:sinatra"], gemspecVersion(repoRoot)))
		}
	}

	// --- Rust web frameworks ---
	if has("Cargo.toml") {
		axumVer, isAxum := detectAxum(repoRoot, depVer)
		switch {
		case isAxum:
			// Apps depend on axum; the axum workspace itself is package name "axum".
			set("axum", "rust", axumVer)
		case depVer["cargo:actix-web"] != "":
			set("actix-web", "rust", depVer["cargo:actix-web"])
		case depVer["cargo:rocket"] != "":
			set("rocket", "rust", depVer["cargo:rocket"])
		}
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

	// --- Elixir / Phoenix ---
	if has("mix.exs") || p.ProjectType == "elixir" {
		mix := readHead(filepath.Join(repoRoot, "mix.exs"), 16384)
		isPhoenixLib := strings.Contains(mix, "Phoenix.MixProject") || strings.Contains(mix, "app: :phoenix")
		isPhoenixApp := strings.Contains(mix, "{:phoenix,") || strings.Contains(mix, "dep: :phoenix")
		if isPhoenixLib || isPhoenixApp {
			ver := ""
			if m := regexp.MustCompile(`@version\s+"([^"]+)"`).FindStringSubmatch(mix); m != nil {
				ver = cleanVer(m[1])
			} else if m := regexp.MustCompile(`\{:phoenix,\s*"([^"]+)"`).FindStringSubmatch(mix); m != nil {
				ver = cleanVer(m[1])
			}
			set("phoenix", "phoenix", ver)
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

// detectNestJS reports whether repoRoot is a NestJS app or @nestjs/* package.
// Root Nest monorepos often name themselves @nestjs/core and depend on express
// without listing @nestjs/core in dependencies — so dep-only detection mislabels
// them as express.
func detectNestJS(repoRoot string, depVer map[string]string) (version string, ok bool) {
	if v := depVer["npm:@nestjs/core"]; v != "" {
		return v, true
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "nest-cli.json")); err == nil {
		return "", true
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err != nil {
		return "", false
	}
	var pj struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &pj) != nil {
		return "", false
	}
	name := strings.TrimSpace(pj.Name)
	if strings.HasPrefix(name, "@nestjs/") || name == "@nestjs/core" {
		return strings.TrimSpace(pj.Version), true
	}
	return "", false
}

// detectVue reports Vue apps and the vuejs/core monorepo (package name "vue" or
// packages/vue present) even when root package.json has no "vue" dependency.
func detectVue(repoRoot string, depVer map[string]string) (version string, ok bool) {
	if v := depVer["npm:vue"]; v != "" {
		return v, true
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err == nil {
		var pj struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(b, &pj) == nil {
			name := strings.TrimSpace(pj.Name)
			if name == "vue" || strings.HasPrefix(name, "@vue/") {
				return strings.TrimSpace(pj.Version), true
			}
		}
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "packages", "vue", "package.json")); err == nil {
		vb, _ := os.ReadFile(filepath.Join(repoRoot, "packages", "vue", "package.json"))
		var vpj struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(vb, &vpj) == nil && strings.TrimSpace(vpj.Name) == "vue" {
			return strings.TrimSpace(vpj.Version), true
		}
		return "", true
	}
	return "", false
}

// detectRemix reports Remix apps and the remix-run/remix monorepo (@remix-run/*
// workspace packages) even when root omits @remix-run/react.
func detectRemix(repoRoot string, depVer map[string]string) (version string, ok bool) {
	if v := depVer["npm:@remix-run/react"]; v != "" {
		return v, true
	}
	for k, v := range depVer {
		if strings.HasPrefix(k, "npm:@remix-run/") {
			return v, true
		}
	}
	b, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err == nil {
		var pj struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &pj) == nil {
			name := strings.TrimSpace(pj.Name)
			if name == "remix" || name == "remix-the-web" || strings.HasPrefix(name, "@remix-run/") {
				return "", true
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(repoRoot, "packages"))
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pb, err := os.ReadFile(filepath.Join(repoRoot, "packages", e.Name(), "package.json"))
		if err != nil {
			continue
		}
		var pj struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(pb, &pj) != nil {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(pj.Name), "@remix-run/") {
			return strings.TrimSpace(pj.Version), true
		}
	}
	return "", false
}

func pyProjectName(repoRoot string) string {
	b, err := os.ReadFile(filepath.Join(repoRoot, "pyproject.toml"))
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
	if m := re.FindStringSubmatch(string(b)); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// detectAxum reports Axum apps (cargo dep) and the tokio-rs/axum workspace
// itself (package name "axum" at root or under axum/).
func detectAxum(repoRoot string, depVer map[string]string) (version string, ok bool) {
	if v := depVer["cargo:axum"]; v != "" {
		return v, true
	}
	if name, ver := cargoPackageMeta(repoRoot); name == "axum" {
		return ver, true
	}
	if name, ver := cargoPackageMeta(filepath.Join(repoRoot, "axum")); name == "axum" {
		return ver, true
	}
	return "", false
}

var cargoPkgNameRe = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
var cargoPkgVerRe = regexp.MustCompile(`(?m)^\s*version\s*=\s*"([^"]+)"`)

func cargoPackageMeta(dir string) (name, version string) {
	b, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		return "", ""
	}
	content := string(b)
	// Prefer [package] table values over workspace.package.
	section := ""
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			section = strings.Trim(t, "[]")
			continue
		}
		if section != "package" {
			continue
		}
		if name == "" {
			if m := cargoPkgNameRe.FindStringSubmatch(t); m != nil {
				name = strings.TrimSpace(m[1])
			}
		}
		if version == "" {
			if m := cargoPkgVerRe.FindStringSubmatch(t); m != nil {
				version = cleanVer(m[1])
			}
		}
	}
	return name, version
}

func gemspecPresent(repoRoot string) bool {
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".gemspec") {
			return true
		}
	}
	return false
}

func gemspecName(repoRoot string) string {
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`Gem::Specification\.new\s+['"]([^'"]+)['"]`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".gemspec") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repoRoot, e.Name()))
		if err != nil {
			continue
		}
		if m := re.FindStringSubmatch(string(b)); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

func gemspecVersion(repoRoot string) string {
	// Sinatra (and similar) store the version in a VERSION file next to the gemspec.
	if b, err := os.ReadFile(filepath.Join(repoRoot, "VERSION")); err == nil {
		return cleanVer(string(b))
	}
	return ""
}
