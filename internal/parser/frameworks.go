package parser

import (
	"path/filepath"
	"sort"
	"strings"
)

// FrameworkPack indicates a detected framework family for a file.
type FrameworkPack string

const (
	FrameworkReact     FrameworkPack = "react"
	FrameworkNextJS    FrameworkPack = "nextjs"
	FrameworkNuxt      FrameworkPack = "nuxt"
	FrameworkSvelte    FrameworkPack = "svelte"
	FrameworkSvelteKit FrameworkPack = "sveltekit"
	FrameworkCapacitor FrameworkPack = "capacitor"
	FrameworkLaravel   FrameworkPack = "laravel"
	FrameworkWordPress FrameworkPack = "wordpress"
	FrameworkDjango    FrameworkPack = "django"
	FrameworkFastAPI   FrameworkPack = "fastapi"
	FrameworkNestJS    FrameworkPack = "nestjs"
	FrameworkExpress   FrameworkPack = "express"
)

// DetectFrameworkPacks detects likely framework families from path/imports/content.
func DetectFrameworkPacks(relPath string, imports []string, content string) []string {
	p := strings.ToLower(filepath.ToSlash(relPath))
	imps := strings.ToLower(strings.Join(imports, "\n"))
	body := strings.ToLower(content)
	out := map[string]struct{}{}
	ext := strings.ToLower(filepath.Ext(p))
	isJS := ext == ".js" || ext == ".jsx" || ext == ".ts" || ext == ".tsx" || ext == ".mjs" || ext == ".cjs"

	if strings.Contains(imps, "from \"react\"") || strings.Contains(imps, "from 'react'") || strings.Contains(p, ".jsx") || strings.Contains(p, ".tsx") {
		out[string(FrameworkReact)] = struct{}{}
	}
	// Next.js: require JS/TS paths or Next-specific markers. Do NOT treat bare
	// Laravel/PHP `app/` directories as Next.js (that mis-tagged Eloquent models).
	if strings.Contains(p, "next.config.") || strings.Contains(body, "next/") || strings.Contains(body, "nextresponse") ||
		(isJS && (strings.Contains(p, "/pages/") || strings.HasPrefix(p, "pages/") ||
			strings.Contains(p, "/app/page.") || strings.HasPrefix(p, "app/page.") ||
			strings.Contains(p, "/app/layout.") || strings.HasPrefix(p, "app/layout.") ||
			strings.Contains(p, "/app/route.") || strings.HasPrefix(p, "app/route.") ||
			strings.Contains(p, "/app/loading.") || strings.HasPrefix(p, "app/loading.") ||
			strings.Contains(p, "/app/error.") || strings.HasPrefix(p, "app/error.") ||
			strings.Contains(p, "/app/template.") || strings.HasPrefix(p, "app/template.") ||
			strings.Contains(p, "/app/default.") || strings.HasPrefix(p, "app/default.") ||
			strings.Contains(p, "/app/not-found.") || strings.HasPrefix(p, "app/not-found."))) {
		out[string(FrameworkNextJS)] = struct{}{}
	}
	if strings.Contains(p, "nuxt.config.") || strings.Contains(body, "from '#app'") || strings.Contains(body, "defineNuxt") {
		out[string(FrameworkNuxt)] = struct{}{}
	}
	if strings.Contains(p, ".svelte") {
		out[string(FrameworkSvelte)] = struct{}{}
	}
	if strings.Contains(p, "+page.") || strings.Contains(p, "+layout.") || strings.Contains(p, "+server.") {
		out[string(FrameworkSvelteKit)] = struct{}{}
	}
	if strings.Contains(body, "@capacitor/") || strings.Contains(body, "registerplugin(") || strings.Contains(p, "capacitor.config.") {
		out[string(FrameworkCapacitor)] = struct{}{}
	}
	if strings.Contains(p, "routes/") || strings.Contains(body, "route::") || strings.Contains(body, "app\\http\\controllers") {
		out[string(FrameworkLaravel)] = struct{}{}
	}
	if strings.Contains(body, "add_action(") || strings.Contains(body, "add_filter(") || strings.Contains(p, "wp-content/") || strings.Contains(p, "functions.php") {
		out[string(FrameworkWordPress)] = struct{}{}
	}
	if strings.Contains(body, "urlpatterns") || strings.Contains(body, "django.urls") || strings.Contains(p, "urls.py") {
		out[string(FrameworkDjango)] = struct{}{}
	}
	if strings.Contains(body, "from fastapi import") || strings.Contains(body, "fastapi(") || strings.Contains(body, "apirouter(") {
		out[string(FrameworkFastAPI)] = struct{}{}
	}
	if strings.Contains(body, "@nestjs/") || strings.Contains(body, "@module(") ||
		strings.Contains(body, "@injectable(") || strings.Contains(body, "@controller(") ||
		strings.Contains(p, "nest-cli.json") || strings.Contains(p, ".module.ts") ||
		strings.Contains(p, ".controller.ts") || strings.Contains(p, ".service.ts") {
		out[string(FrameworkNestJS)] = struct{}{}
	}
	if strings.Contains(body, "express()") || strings.Contains(body, "require('express") ||
		strings.Contains(body, `require("express`) || strings.Contains(body, "from 'express'") ||
		strings.Contains(body, `from "express"`) || strings.Contains(p, "lib/application.js") ||
		strings.Contains(p, "lib/express.js") {
		out[string(FrameworkExpress)] = struct{}{}
	}

	return sortedKeys(out)
}

func frameworkSignature(frameworks []string, role string) string {
	if len(frameworks) == 0 && strings.TrimSpace(role) == "" {
		return ""
	}
	parts := make([]string, 0, 2)
	if len(frameworks) > 0 {
		parts = append(parts, "frameworks="+strings.Join(frameworks, ","))
	}
	if strings.TrimSpace(role) != "" {
		parts = append(parts, "role="+strings.TrimSpace(role))
	}
	return strings.Join(parts, ";")
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func withFramework(frameworks []string, fw string) []string {
	out := make([]string, 0, len(frameworks)+1)
	out = append(out, frameworks...)
	for _, f := range frameworks {
		if f == fw {
			return out
		}
	}
	out = append(out, fw)
	return out
}
