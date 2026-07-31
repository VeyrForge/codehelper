package security

import (
	"os"
	"path/filepath"
	"strings"
)

// scanVueSpaChecklist emits Nest-style split FRAMEWORK FOOTGUN rows for Vue
// SPA/app trees (not vuejs/core packages/*). Grounded file:line on a real
// component — never invent CVEs. When v-html / location / env sinks exist,
// cite those lines; otherwise cite an anchor component with honest checklist hints.
func scanVueSpaChecklist(root string, remaining int) []ContextFinding {
	if remaining <= 0 || isVueFrameworkCoreLayout(root) {
		return nil
	}
	files := listSpaSourceFiles(root, ".vue", 40)
	if len(files) == 0 {
		return nil
	}
	return emitSpaChecklist(root, files, remaining, spaChecklistSpec{
		xssNeedle:     []string{"v-html", "innerHTML"},
		xssRule:       "spa-xss-vhtml",
		xssPresent:    "[spa-footgun] v-html / innerHTML binding found — sanitize untrusted HTML at this call site (not a confirmed CVE by itself).",
		xssAbsent:     "[spa-footgun] Vue SPA: audit for v-html / innerHTML — unsanitized bindings are XSS. Prefer text interpolation; sanitize if raw HTML is required.",
		redirNeedle:   []string{"window.location", "location.href", "location.assign", "router.push", "router.replace"},
		redirRule:     "spa-open-redirect",
		redirPresent:  "[spa-footgun] Client navigation/location write found — allowlist redirect targets; never pass raw user URLs to location/router.",
		redirAbsent:   "[spa-footgun] Vue SPA: review router.push / window.location for open redirects — allowlist external targets.",
		secretNeedle:  []string{"import.meta.env", "VITE_", "process.env."},
		secretRule:    "spa-secret-frontend",
		secretPresent: "[spa-footgun] Frontend env access found — anything shipped via VITE_/import.meta.env is public; never put API secrets there.",
		secretAbsent:  "[spa-footgun] Vue SPA: do not put API keys/secrets in VITE_ / import.meta.env — they ship to every browser.",
		csrfRule:      "spa-csrf-client",
		csrfHint:      "[spa-footgun] Classic form CSRF is often N/A for JSON SPA APIs — verify cookie SameSite, CORS allowlist, and that auth tokens are not only in localStorage if XSS is possible.",
		anchorNeedles: []string{"<template>", "<script"},
	})
}

// scanSvelteSpaChecklist mirrors scanVueSpaChecklist for Svelte apps ({@html}).
func scanSvelteSpaChecklist(root string, remaining int) []ContextFinding {
	if remaining <= 0 || isSvelteFrameworkCoreLayout(root) {
		return nil
	}
	files := listSpaSourceFiles(root, ".svelte", 40)
	if len(files) == 0 {
		return nil
	}
	return emitSpaChecklist(root, files, remaining, spaChecklistSpec{
		xssNeedle:     []string{"{@html", "innerHTML"},
		xssRule:       "spa-xss-html",
		xssPresent:    "[spa-footgun] {@html} / innerHTML binding found — sanitize untrusted HTML at this call site (not a confirmed CVE by itself).",
		xssAbsent:     "[spa-footgun] Svelte SPA: audit for {@html} — unsanitized bindings are XSS. Prefer text nodes; sanitize if raw HTML is required.",
		redirNeedle:   []string{"window.location", "location.href", "location.assign", "goto("},
		redirRule:     "spa-open-redirect",
		redirPresent:  "[spa-footgun] Client navigation/location write found — allowlist redirect targets; never pass raw user URLs to location/goto.",
		redirAbsent:   "[spa-footgun] Svelte SPA: review goto / window.location for open redirects — allowlist external targets.",
		secretNeedle:  []string{"import.meta.env", "VITE_", "PUBLIC_", "process.env."},
		secretRule:    "spa-secret-frontend",
		secretPresent: "[spa-footgun] Frontend env access found — anything shipped via VITE_/PUBLIC_/import.meta.env is public; never put API secrets there.",
		secretAbsent:  "[spa-footgun] Svelte SPA: do not put API keys/secrets in VITE_ / PUBLIC_ / import.meta.env — they ship to every browser.",
		csrfRule:      "spa-csrf-client",
		csrfHint:      "[spa-footgun] Classic form CSRF is often N/A for JSON SPA APIs — verify cookie SameSite, CORS allowlist, and that auth tokens are not only in localStorage if XSS is possible.",
		anchorNeedles: []string{"<script", "<div", "<button"},
	})
}

func isVueFrameworkCoreLayout(root string) bool {
	return hasFile(root, "packages/compiler-dom/src/transforms/vHtml.ts") ||
		hasFile(root, "packages/runtime-core/src/renderer.ts") ||
		hasFile(root, "packages/runtime-dom/src/directives/vHtml.ts")
}

func isSvelteFrameworkCoreLayout(root string) bool {
	return hasFile(root, "packages/svelte/src/compiler/index.js") ||
		hasFile(root, "packages/svelte/src/compiler/phases/3-transform/client/visitors/HtmlTag.js")
}

// isFrontendSpaApp detects thin Vue/Svelte app trees (components without
// monorepo packages/* framework cores). Used so basename "vue"/"svelte" beds
// are not misclassified as framework_core when they are SPA skeletons.
func isFrontendSpaApp(root string) bool {
	if isVueFrameworkCoreLayout(root) || isSvelteFrameworkCoreLayout(root) {
		return false
	}
	return len(listSpaSourceFiles(root, ".vue", 4)) > 0 ||
		len(listSpaSourceFiles(root, ".svelte", 4)) > 0
}

func listSpaSourceFiles(root, ext string, limit int) []string {
	if limit <= 0 {
		limit = 40
	}
	ext = strings.ToLower(ext)
	seen := map[string]struct{}{}
	var out []string
	add := func(rel string) {
		rel = filepath.ToSlash(rel)
		if _, ok := seen[rel]; ok {
			return
		}
		seen[rel] = struct{}{}
		out = append(out, rel)
	}

	// Prefer ReadDir/Glob probes — filepath.WalkDir does not follow Windows
	// directory junctions/reparse points, and LIVE beds under .testbeds/active
	// are junctions into .stub-src / .eval-projects.
	root = resolveScanRoot(filepath.Clean(root))
	probeDirs := []string{
		"src", "lib", "app", "apps", "components", "pages", "views", "routes",
		"src/components", "src/pages", "src/views", "src/routes",
		"lib/components", "app/components", "app/routes",
	}
	for _, dir := range probeDirs {
		if len(out) >= limit {
			break
		}
		ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if len(out) >= limit {
				break
			}
			if e.IsDir() {
				continue
			}
			if strings.ToLower(filepath.Ext(e.Name())) != ext {
				continue
			}
			add(filepath.Join(dir, e.Name()))
		}
	}
	if len(out) > 0 {
		return out
	}

	// Non-junction trees: full walk. Skip the same nested beds / vendor trees as
	// repoScanSkipDirs so host apps (e.g. codehelper with .eval-projects/) never
	// inherit Vue/Svelte SPA checklist cites from foreign eval corpora.
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(out) >= limit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			name := d.Name()
			if spaWalkSkipDirs[name] || (strings.HasPrefix(name, ".") && name != "." && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ext {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if isRepoScanNoisePath(relSlash) {
			return nil
		}
		add(rel)
		return nil
	})
	return out
}

// spaWalkSkipDirs mirrors the high-signal repoScanSkipDirs entries that would
// otherwise pollute SPA discovery on monorepos / self-hosted eval trees.
var spaWalkSkipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true, "vendor": true,
	".codehelper": true, "coverage": true, "out": true, "tmp": true, "target": true,
	".eval-projects": true, ".testbeds": true, ".ci-testbeds": true, ".ci-testbeds-extended": true,
	"packages-private": true, "testdata": true, "fixtures": true, "third_party": true,
	"__pycache__": true, ".venv": true, "venv": true, ".next": true, ".nuxt": true,
	".svelte-kit": true, "site-packages": true,
}

type spaChecklistSpec struct {
	xssNeedle, redirNeedle, secretNeedle, anchorNeedles []string
	xssRule, redirRule, secretRule, csrfRule            string
	xssPresent, xssAbsent                               string
	redirPresent, redirAbsent                           string
	secretPresent, secretAbsent, csrfHint               string
}

type spaHit struct {
	file, evidence string
	line           int
}

func emitSpaChecklist(root string, files []string, remaining int, spec spaChecklistSpec) []ContextFinding {
	if remaining <= 0 || len(files) == 0 {
		return nil
	}
	anchor := files[0]
	anchorBody := readFileTrunc(filepath.Join(root, filepath.FromSlash(anchor)), 8000)
	anchorLine := 1
	for _, n := range spec.anchorNeedles {
		if ln := firstLineContaining(anchorBody, n); ln > 0 {
			anchorLine = ln
			break
		}
	}
	anchorEvidence := truncate(strings.TrimSpace(lineAt(anchorBody, anchorLine)), 200)
	if anchorEvidence == "" {
		anchorEvidence = filepath.Base(anchor) + " — SPA component surface"
	}

	var xss, redir, secret *spaHit
	for _, rel := range files {
		body := readFileTrunc(filepath.Join(root, filepath.FromSlash(rel)), 8000)
		if body == "" {
			continue
		}
		if xss == nil {
			if h := firstSpaHit(rel, body, spec.xssNeedle); h != nil {
				xss = h
			}
		}
		if redir == nil {
			if h := firstSpaHit(rel, body, spec.redirNeedle); h != nil {
				redir = h
			}
		}
		if secret == nil {
			if h := firstSpaHit(rel, body, spec.secretNeedle); h != nil {
				secret = h
			}
		}
	}

	var out []ContextFinding
	appendRow := func(rule, sev, hint string, h *spaHit) {
		if len(out) >= remaining {
			return
		}
		f, line, ev := anchor, anchorLine, anchorEvidence
		conf, exploit := "medium", "framework-api"
		if h != nil {
			f, line, ev = h.file, h.line, h.evidence
			if strings.TrimSpace(ev) == "" {
				ev = anchorEvidence
			}
			conf, exploit = "high", "possible"
		}
		out = append(out, ContextFinding{
			Tool: "codehelper-spa-security", Severity: sev, Rule: rule,
			File: f, Line: line, Evidence: ev,
			Kind: "library_guidance", Confidence: conf, Exploitability: exploit,
			Hint: hint,
		})
	}

	if xss != nil {
		appendRow(spec.xssRule, "medium", spec.xssPresent, xss)
	} else {
		appendRow(spec.xssRule, "medium", spec.xssAbsent, nil)
	}
	if redir != nil {
		appendRow(spec.redirRule, "medium", spec.redirPresent, redir)
	} else {
		appendRow(spec.redirRule, "low", spec.redirAbsent, nil)
	}
	if secret != nil {
		appendRow(spec.secretRule, "medium", spec.secretPresent, secret)
	} else {
		appendRow(spec.secretRule, "low", spec.secretAbsent, nil)
	}
	appendRow(spec.csrfRule, "low", spec.csrfHint, nil)
	return out
}

func firstSpaHit(rel, body string, needles []string) *spaHit {
	lower := strings.ToLower(body)
	for _, needle := range needles {
		n := strings.ToLower(needle)
		if !strings.Contains(lower, n) && !strings.Contains(body, needle) {
			continue
		}
		line := firstLineContaining(body, needle)
		if line <= 0 {
			continue
		}
		ev := truncate(strings.TrimSpace(lineAt(body, line)), 200)
		return &spaHit{file: rel, line: line, evidence: ev}
	}
	return nil
}
