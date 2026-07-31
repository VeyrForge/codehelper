package parser

import (
	"path/filepath"
	"sort"
	"strings"
)

// FrameworkPack indicates a detected framework family for a file.
type FrameworkPack string

const (
	FrameworkReact             FrameworkPack = "react"
	FrameworkNextJS            FrameworkPack = "nextjs"
	FrameworkNuxt              FrameworkPack = "nuxt"
	FrameworkAngular           FrameworkPack = "angular"
	FrameworkSvelte            FrameworkPack = "svelte"
	FrameworkSvelteKit         FrameworkPack = "sveltekit"
	FrameworkRemix             FrameworkPack = "remix"
	FrameworkElectron          FrameworkPack = "electron"
	FrameworkVue               FrameworkPack = "vue"
	FrameworkAstro             FrameworkPack = "astro"
	FrameworkCapacitor         FrameworkPack = "capacitor"
	FrameworkLaravel           FrameworkPack = "laravel"
	FrameworkSymfony           FrameworkPack = "symfony"
	FrameworkWordPress         FrameworkPack = "wordpress"
	FrameworkRails             FrameworkPack = "rails"
	FrameworkSinatra           FrameworkPack = "sinatra"
	FrameworkDjango            FrameworkPack = "django"
	FrameworkDjangoREST        FrameworkPack = "djangorest"
	FrameworkFastAPI           FrameworkPack = "fastapi"
	FrameworkFlask             FrameworkPack = "flask"
	FrameworkNestJS            FrameworkPack = "nestjs"
	FrameworkExpress           FrameworkPack = "express"
	FrameworkFastify           FrameworkPack = "fastify"
	FrameworkHono              FrameworkPack = "hono"
	FrameworkSpring            FrameworkPack = "spring"
	FrameworkJPA               FrameworkPack = "jpa"
	FrameworkHibernate         FrameworkPack = "hibernate"
	FrameworkPhoenix           FrameworkPack = "phoenix"
	FrameworkAspNetCore        FrameworkPack = "aspnetcore"
	FrameworkDeno              FrameworkPack = "deno"
	FrameworkBun               FrameworkPack = "bun"
	FrameworkCloudflareWorkers FrameworkPack = "cloudflare_workers"
	FrameworkEdge              FrameworkPack = "edge"
	FrameworkFlutter           FrameworkPack = "flutter"
	FrameworkReactNative       FrameworkPack = "react_native"
	FrameworkPrisma            FrameworkPack = "prisma"
	FrameworkTypeORM           FrameworkPack = "typeorm"
	FrameworkSequelize         FrameworkPack = "sequelize"
	FrameworkDrizzle           FrameworkPack = "drizzle"
	FrameworkIonic             FrameworkPack = "ionic"
	FrameworkSwiftUI           FrameworkPack = "swiftui"
	FrameworkGin               FrameworkPack = "gin"
	FrameworkFiber             FrameworkPack = "fiber"
	FrameworkEcho              FrameworkPack = "echo"
	FrameworkChi               FrameworkPack = "chi"
	FrameworkBeego             FrameworkPack = "beego"
)

// DetectFrameworkPacks detects likely framework families from path/imports/content.
// Rules live in frameworkPackRules (framework_detect_rules.go).
func DetectFrameworkPacks(relPath string, imports []string, content string) []string {
	p := strings.ToLower(filepath.ToSlash(relPath))
	imps := strings.ToLower(strings.Join(imports, "\n"))
	body := strings.ToLower(content)
	ext := strings.ToLower(filepath.Ext(p))
	in := frameworkDetectInput{
		p:    p,
		imps: imps,
		body: body,
		ext:  ext,
		isJS: ext == ".js" || ext == ".jsx" || ext == ".ts" || ext == ".tsx" || ext == ".mjs" || ext == ".cjs",
	}
	out := map[string]struct{}{}
	for _, rule := range frameworkPackRules {
		if !rule.match(in) {
			continue
		}
		for _, pack := range rule.packs {
			addFrameworkPack(out, pack)
		}
		if rule.after != nil {
			rule.after(in, out)
		}
	}
	return sortedKeys(out)
}

// isNextAppRouterRelPath reports App Router convention files under app/
// (including nested routes like app/api/health/route.ts).
func isNextAppRouterRelPath(p string) bool {
	p = strings.ToLower(filepath.ToSlash(p))
	underApp := strings.Contains(p, "/app/") || strings.HasPrefix(p, "app/")
	if !underApp {
		return false
	}
	base := filepath.Base(p)
	for _, prefix := range []string{
		"page.", "layout.", "route.", "loading.", "error.",
		"template.", "default.", "not-found.",
	} {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

func baseNameNoExt(p string) string {
	base := filepath.Base(p)
	ext := filepath.Ext(base)
	if ext == "" {
		return base
	}
	return strings.TrimSuffix(base, ext)
}

func electronConventionPath(p string) bool {
	p = strings.ToLower(filepath.ToSlash(p))
	base := strings.ToLower(baseNameNoExt(p))
	// Host bleed: bare main.ts/js is Deno/Nest/Node entry; require dir/suffix markers.
	return base == "preload" || base == "renderer" ||
		strings.HasSuffix(base, "preload") || strings.HasSuffix(base, "-main") ||
		strings.Contains(p, "/main/") || strings.HasPrefix(p, "main/") ||
		strings.Contains(p, "/preload/") || strings.HasPrefix(p, "preload/") ||
		strings.Contains(p, "/renderer/") || strings.HasPrefix(p, "renderer/") ||
		strings.Contains(p, "/electron/") || strings.HasPrefix(p, "electron/")
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
