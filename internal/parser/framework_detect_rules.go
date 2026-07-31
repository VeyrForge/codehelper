package parser

import (
	"path/filepath"
	"strings"
)

// frameworkDetectInput is the normalized probe for DetectFrameworkPacks rules.
type frameworkDetectInput struct {
	p, imps, body, ext string
	isJS               bool
}

// frameworkPackRule is one detection check. Unlike demotion tables, every matching
// rule applies (packs accumulate); order is stable for readability only.
type frameworkPackRule struct {
	id    string
	match func(in frameworkDetectInput) bool
	packs []FrameworkPack
	// after runs when match is true; may add conditional secondary packs.
	after func(in frameworkDetectInput, out map[string]struct{})
}

func addFrameworkPack(out map[string]struct{}, pack FrameworkPack) {
	out[string(pack)] = struct{}{}
}

// frameworkPackRules is the data-driven table for DetectFrameworkPacks.
// Adding a pack: append a rule here instead of extending the if-ladder.
var frameworkPackRules = []frameworkPackRule{
	{
		id: "react",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.imps, "from \"react\"") || strings.Contains(in.imps, "from 'react'") ||
				strings.Contains(in.p, ".jsx") || strings.Contains(in.p, ".tsx")
		},
		packs: []FrameworkPack{FrameworkReact},
	},
	{
		// Next.js: config / next/* APIs / App Router files. Pages Router only under
		// pages/ or src/pages/ with Next page signals — not Ionic/React "pages" dirs.
		// Do NOT treat bare Laravel/PHP `app/` as Next.js.
		id: "nextjs",
		match: func(in frameworkDetectInput) bool {
			if strings.Contains(in.p, "next.config.") || hasNextJSMarkers(in) {
				return true
			}
			if !in.isJS {
				return false
			}
			if isNextAppRouterRelPath(in.p) {
				return true
			}
			return isNextPagesRouterRelPath(in.p) && looksLikeNextPagesFile(in)
		},
		packs: []FrameworkPack{FrameworkNextJS},
	},
	{
		// Nuxt: config, #app/#imports, defineNuxt*, pages/layouts SFCs, or
		// composables/plugins/server/api/middleware with Nuxt-specific markers.
		id: "nuxt",
		match: func(in frameworkDetectInput) bool {
			if strings.Contains(in.p, "nuxt.config.") || hasNuxtBodyMarkers(in) {
				return true
			}
			// File-based routing: pages/*.vue and layouts/*.vue are Nuxt (not Vite "views/").
			if strings.HasSuffix(in.p, ".vue") && isNuxtPagesOrLayoutsPath(in.p) {
				return true
			}
			if strings.HasSuffix(in.p, "app.vue") || strings.HasSuffix(in.p, "/app.vue") {
				return true
			}
			if !in.isJS {
				return false
			}
			if isNuxtServerAPIPath(in.p) && strings.Contains(in.body, "defineeventhandler(") {
				return true
			}
			if isNuxtComposablesPath(in.p) && looksLikeNuxtComposable(in) {
				return true
			}
			if isNuxtPluginsPath(in.p) && (strings.Contains(in.body, "definenuxtplugin") ||
				strings.Contains(in.body, "from '#")) {
				return true
			}
			if isNuxtMiddlewarePath(in.p) && (strings.Contains(in.body, "definenuxt") ||
				strings.Contains(in.body, "navigateto(")) {
				return true
			}
			return false
		},
		packs: []FrameworkPack{FrameworkNuxt},
	},
	{
		// Angular: @angular/* imports, NgModule/Component decorators, conventional paths.
		// Do NOT treat Nest @Injectable/@Module alone as Angular (.module.ts is shared).
		id: "angular",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "@angular/") || strings.Contains(in.body, "@ngmodule(") ||
				strings.Contains(in.p, "angular.json") || strings.HasSuffix(in.p, ".component.ts") ||
				strings.HasSuffix(in.p, ".component.tsx") ||
				(strings.Contains(in.body, "@component(") && (strings.Contains(in.body, "selector:") ||
					strings.Contains(in.body, "templateurl:") || strings.Contains(in.body, "standalone:"))) ||
				(strings.Contains(in.body, "@injectable(") && strings.Contains(in.body, "@angular/"))
		},
		packs: []FrameworkPack{FrameworkAngular},
	},
	{
		id: "svelte",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.p, ".svelte")
		},
		packs: []FrameworkPack{FrameworkSvelte},
	},
	{
		id: "vue",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.p, ".vue") || strings.Contains(in.body, "defineprops(") ||
				strings.Contains(in.body, "<script setup")
		},
		packs: []FrameworkPack{FrameworkVue},
	},
	{
		id: "astro",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.p, ".astro") || strings.Contains(in.p, "astro.config.")
		},
		packs: []FrameworkPack{FrameworkAstro},
	},
	{
		// SvelteKit: +page/+layout/+server conventions, @sveltejs/kit, $app/*, hooks.
		id: "sveltekit",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.p, "+page.") || strings.Contains(in.p, "+layout.") ||
				strings.Contains(in.p, "+server.") || strings.Contains(in.p, "hooks.server.") ||
				strings.Contains(in.p, "hooks.client.") || strings.Contains(in.p, "svelte.config.") ||
				strings.Contains(in.body, "@sveltejs/kit") ||
				strings.Contains(in.body, "from \"$app/") || strings.Contains(in.body, "from '$app/") ||
				strings.Contains(in.body, "from \"$env/") || strings.Contains(in.body, "from '$env/")
		},
		packs: []FrameworkPack{FrameworkSvelteKit},
		after: func(in frameworkDetectInput, out map[string]struct{}) {
			if strings.Contains(in.p, ".svelte") || strings.Contains(in.body, "svelte") {
				addFrameworkPack(out, FrameworkSvelte)
			}
		},
	},
	{
		// Remix: @remix-run/*, remix.config, app/routes loaders/actions.
		id: "remix",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "@remix-run/") || strings.Contains(in.p, "remix.config.") ||
				strings.Contains(in.body, "from \"@remix-run") || strings.Contains(in.body, "from '@remix-run") ||
				(in.isJS && (strings.Contains(in.p, "/app/routes/") || strings.HasPrefix(in.p, "app/routes/")) &&
					(strings.Contains(in.body, "export async function loader") || strings.Contains(in.body, "export function loader") ||
						strings.Contains(in.body, "export const loader") || strings.Contains(in.body, "export async function action") ||
						strings.Contains(in.body, "export function action") || strings.Contains(in.body, "export const action") ||
						strings.Contains(in.body, "clientloader") || strings.Contains(in.body, "clientaction") ||
						strings.Contains(in.body, "@remix-run/")))
		},
		packs: []FrameworkPack{FrameworkRemix, FrameworkReact},
	},
	{
		// Electron: electron imports, IPC/contextBridge, main/preload/renderer paths.
		id: "electron",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "from \"electron\"") || strings.Contains(in.body, "from 'electron'") ||
				strings.Contains(in.body, "require(\"electron\")") || strings.Contains(in.body, "require('electron')") ||
				strings.Contains(in.body, "ipcmain.") || strings.Contains(in.body, "ipcrenderer.") ||
				strings.Contains(in.body, "contextbridge.") || strings.Contains(in.p, "electron.") ||
				strings.Contains(in.body, "window.api") ||
				(in.isJS && electronConventionPath(in.p))
		},
		packs: []FrameworkPack{FrameworkElectron},
	},
	{
		id: "capacitor",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "@capacitor/") || strings.Contains(in.body, "registerplugin(") ||
				strings.Contains(in.p, "capacitor.config.")
		},
		packs: []FrameworkPack{FrameworkCapacitor},
	},
	{
		// Ionic: @ionic/* + IonPage/IonRouterOutlet (often paired with Capacitor).
		id: "ionic",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "@ionic/") || strings.Contains(in.body, "ionpage") ||
				strings.Contains(in.body, "ionrouteroutlet") || strings.Contains(in.body, "ionicmodule") ||
				strings.Contains(in.p, "ionic.config.")
		},
		packs: []FrameworkPack{FrameworkIonic},
		after: func(in frameworkDetectInput, out map[string]struct{}) {
			if strings.Contains(in.body, "@capacitor/") || strings.Contains(in.body, "registerplugin(") {
				addFrameworkPack(out, FrameworkCapacitor)
			}
		},
	},
	{
		// Laravel: real apps keep most code outside routes/ and rarely mention
		// Route:: there — recognise the framework's own directory layout, its
		// Illuminate imports, and Blade templates, or models/services/jobs go
		// untagged and framework-aware ranking never fires for them.
		id: "laravel",
		match: func(in frameworkDetectInput) bool {
			if !(in.ext == ".php" || strings.HasSuffix(in.p, "artisan")) {
				return false
			}
			if strings.Contains(in.p, "routes/") || strings.Contains(in.body, "route::") ||
				strings.Contains(in.body, "app\\http\\controllers") ||
				strings.HasSuffix(in.p, ".blade.php") {
				return true
			}
			for _, marker := range []string{
				"illuminate\\", "laravel\\", "livewire\\", "filament\\", "eloquent",
			} {
				if strings.Contains(in.body, marker) {
					return true
				}
			}
			// Eloquent-style bases only under Laravel app trees (avoid bare `extends Model`).
			if isLaravelAppTreePath(in.p) {
				for _, marker := range []string{
					"extends model", "extends controller", "extends formrequest",
					"extends serviceprovider", "extends middleware", "extends authenticatable",
				} {
					if strings.Contains(in.body, marker) {
						return true
					}
				}
				return true // conventional Laravel path even without body markers
			}
			for _, seg := range []string{
				"database/migrations/", "database/seeders/", "database/factories/",
				"bootstrap/app.php", "resources/views/",
			} {
				if strings.HasPrefix(in.p, seg) || strings.Contains(in.p, "/"+seg) {
					return true
				}
			}
			return false
		},
		packs: []FrameworkPack{FrameworkLaravel},
	},
	{
		// Symfony: attribute routes, AbstractController, framework-bundle, bin/console.
		// Do NOT treat Laravel Route:: as Symfony.
		id: "symfony",
		match: func(in frameworkDetectInput) bool {
			if !(in.ext == ".php" || strings.HasSuffix(in.p, "bin/console") || strings.Contains(in.p, "composer.json")) {
				return false
			}
			return strings.Contains(in.body, "symfony\\") || strings.Contains(in.body, "symfony/") ||
				strings.Contains(in.body, "abstractcontroller") || strings.Contains(in.body, "#[route") ||
				strings.Contains(in.body, "ascontroller") || strings.Contains(in.body, "frameworkbundle") ||
				strings.HasSuffix(in.p, "bin/console") ||
				(strings.Contains(in.p, "/controller/") || strings.Contains(in.p, "/controllers/")) &&
					(strings.Contains(in.body, "#[route") || strings.Contains(in.body, "abstractcontroller") ||
						strings.Contains(in.body, "symfony\\"))
		},
		packs: []FrameworkPack{FrameworkSymfony},
	},
	{
		// WordPress: plugins and themes are largely files with no hook call of
		// their own (class files, REST controllers, templates), so recognise the
		// core API surface and the wp-* trees too. Bare functions.php alone is
		// not enough (too many non-WP PHP helpers share that name).
		id: "wordpress",
		match: func(in frameworkDetectInput) bool {
			if strings.Contains(in.p, "wp-content/") || strings.Contains(in.p, "wp-includes/") ||
				strings.Contains(in.p, "wp-admin/") {
				return true
			}
			if in.ext != ".php" && in.ext != "" {
				return false
			}
			return hasWordPressBodyMarkers(in.body)
		},
		packs: []FrameworkPack{FrameworkWordPress},
	},
	{
		id: "rails",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "rails.application") || strings.Contains(in.body, "applicationcontroller") ||
				strings.Contains(in.body, "activerecord::base") || strings.Contains(in.p, "config/routes") ||
				strings.Contains(in.body, "before_action") || strings.Contains(in.body, "has_many") ||
				strings.Contains(in.body, "belongs_to") ||
				(strings.HasSuffix(in.p, "/gemfile") && strings.Contains(in.body, "gem 'rails'"))
		},
		packs: []FrameworkPack{FrameworkRails},
	},
	{
		id: "sinatra",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "sinatra::base") || strings.Contains(in.body, "require 'sinatra") ||
				strings.Contains(in.body, `require "sinatra`) || strings.Contains(in.p, "sinatra/")
		},
		packs: []FrameworkPack{FrameworkSinatra},
	},
	{
		id: "django",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "urlpatterns") || strings.Contains(in.body, "django.urls") ||
				strings.Contains(in.p, "urls.py")
		},
		packs: []FrameworkPack{FrameworkDjango},
	},
	{
		id: "djangorest",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "rest_framework") || strings.Contains(in.body, "apiview") ||
				strings.Contains(in.body, "viewsets.") || strings.Contains(in.body, "defaultrouter") ||
				strings.Contains(in.body, "@api_view") || strings.Contains(in.body, ".as_view(")
		},
		packs: []FrameworkPack{FrameworkDjangoREST, FrameworkDjango},
	},
	{
		id: "fastapi",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "from fastapi import") || strings.Contains(in.body, "fastapi(") ||
				strings.Contains(in.body, "apirouter(")
		},
		packs: []FrameworkPack{FrameworkFastAPI},
	},
	{
		id: "flask",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "from flask import") || strings.Contains(in.body, "flask(") ||
				strings.Contains(in.body, "@app.route") || strings.Contains(in.body, "@bp.route") ||
				strings.Contains(in.body, "@blueprint.route") || strings.Contains(in.body, "blueprint(")
		},
		packs: []FrameworkPack{FrameworkFlask},
	},
	{
		// NestJS: require @nestjs/* or Nest decorators. Do NOT tag bare
		// *.service.ts / *.module.ts paths (generic Node/Angular-adjacent names).
		id: "nestjs",
		match: func(in frameworkDetectInput) bool {
			if strings.Contains(in.p, "nest-cli.json") || strings.Contains(in.body, "@nestjs/") {
				return true
			}
			if strings.Contains(in.body, "@controller(") {
				return true
			}
			if strings.Contains(in.body, "@module(") && !strings.Contains(in.body, "@ngmodule(") {
				return true
			}
			if strings.Contains(in.body, "@injectable(") && !hasAngularMarkers(in) {
				return true
			}
			// Nest HTTP method decorators on controller files without an import line yet.
			if in.isJS && strings.Contains(in.p, ".controller.") && hasNestHTTPMethodDecorator(in) &&
				!hasAngularMarkers(in) {
				return true
			}
			return false
		},
		packs: []FrameworkPack{FrameworkNestJS},
	},
	{
		id: "express",
		match: func(in frameworkDetectInput) bool {
			if strings.Contains(in.body, "express()") || strings.Contains(in.body, "require('express") ||
				strings.Contains(in.body, `require("express`) || strings.Contains(in.body, "from 'express'") ||
				strings.Contains(in.body, `from "express"`) || strings.Contains(in.body, "express.router") ||
				strings.Contains(in.p, "lib/application.js") || strings.Contains(in.p, "lib/express.js") {
				return true
			}
			// Node/Express middleware convention: (req, res, next) under middleware/.
			if in.isJS && isExpressMiddlewarePath(in.p) &&
				(strings.Contains(in.body, "req, res, next") || strings.Contains(in.body, "req,res,next")) {
				return true
			}
			return false
		},
		packs: []FrameworkPack{FrameworkExpress},
	},
	{
		// Fastify: import/require fastify, Fastify()/fastify() — distinct from Express.
		id: "fastify",
		match: func(in frameworkDetectInput) bool {
			return in.isJS && (strings.Contains(in.body, "from 'fastify'") || strings.Contains(in.body, `from "fastify"`) ||
				strings.Contains(in.body, "require('fastify") || strings.Contains(in.body, `require("fastify`) ||
				strings.Contains(in.body, "fastify()") || strings.Contains(in.body, "@fastify/"))
		},
		packs: []FrameworkPack{FrameworkFastify},
	},
	{
		// Hono: import { Hono } / new Hono() — app.get API overlaps Express; pack is the distinguisher.
		id: "hono",
		match: func(in frameworkDetectInput) bool {
			return in.isJS && (strings.Contains(in.body, "from 'hono'") || strings.Contains(in.body, `from "hono"`) ||
				strings.Contains(in.body, "from 'hono/") || strings.Contains(in.body, `from "hono/`) ||
				strings.Contains(in.body, "require('hono") || strings.Contains(in.body, `require("hono`) ||
				strings.Contains(in.body, "new hono("))
		},
		packs: []FrameworkPack{FrameworkHono},
	},
	{
		id: "spring",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "org.springframework") || strings.Contains(in.body, "springframework") ||
				strings.Contains(in.p, "application.properties") || strings.Contains(in.p, "application.yml") ||
				strings.Contains(in.p, "application.yaml") ||
				((in.ext == ".java" || in.ext == ".kt") && (strings.Contains(in.body, "@restcontroller") ||
					strings.Contains(in.body, "@springbootapplication") || strings.Contains(in.body, "@autowired") ||
					strings.Contains(in.body, "@controller") || strings.Contains(in.body, "@service") ||
					strings.Contains(in.body, "@repository") || strings.Contains(in.body, "@component") ||
					strings.Contains(in.p, "/controller/") || strings.Contains(in.p, "/controllers/")))
		},
		packs: []FrameworkPack{FrameworkSpring},
	},
	{
		// JPA / Hibernate: jakarta/javax.persistence, @Entity/@Query, JpaRepository.
		id: "jpa",
		match: func(in frameworkDetectInput) bool {
			return (in.ext == ".java" || in.ext == ".kt") && (strings.Contains(in.body, "jakarta.persistence") ||
				strings.Contains(in.body, "javax.persistence") || strings.Contains(in.body, "@entity") ||
				strings.Contains(in.body, "@query") || strings.Contains(in.body, "jparepository") ||
				strings.Contains(in.body, "entitymanager") || strings.Contains(in.body, "org.hibernate") ||
				strings.Contains(in.p, "/entity/") || strings.Contains(in.p, "/entities/") ||
				strings.Contains(in.p, "/repository/") || strings.Contains(in.p, "/repositories/"))
		},
		packs: []FrameworkPack{FrameworkJPA},
		after: func(in frameworkDetectInput, out map[string]struct{}) {
			if strings.Contains(in.body, "org.hibernate") || strings.Contains(in.body, "hibernate") {
				addFrameworkPack(out, FrameworkHibernate)
			}
		},
	},
	{
		// Phoenix: Elixir controllers / LiveView / router (mix.exs dep or use Phoenix.*).
		id: "phoenix",
		match: func(in frameworkDetectInput) bool {
			if !(in.ext == ".ex" || in.ext == ".exs" || strings.HasSuffix(in.p, "mix.exs")) {
				return false
			}
			return strings.Contains(in.body, "phoenix.controller") || strings.Contains(in.body, "phoenix.liveview") ||
				strings.Contains(in.body, "phoenix.router") || strings.Contains(in.body, "use phoenix.") ||
				strings.Contains(in.body, ", :controller") || strings.Contains(in.body, ", :live_view") ||
				strings.Contains(in.body, ", :router") || strings.Contains(in.body, "pipe_through") ||
				strings.Contains(in.body, "{:phoenix,") || strings.HasSuffix(in.p, "_controller.ex") ||
				strings.HasSuffix(in.p, "_live.ex") || strings.HasSuffix(in.p, "/router.ex") ||
				strings.Contains(in.p, "/controllers/") || strings.Contains(in.p, "/live/")
		},
		packs: []FrameworkPack{FrameworkPhoenix},
	},
	{
		// ASP.NET Core: Controllers, Minimal APIs, DI markers (C#/.cs only — avoid PHP Controllers/).
		id: "aspnetcore",
		match: func(in frameworkDetectInput) bool {
			if !(in.ext == ".cs" || strings.Contains(in.p, ".csproj") || strings.Contains(in.body, "microsoft.aspnetcore")) {
				return false
			}
			return strings.Contains(in.body, "microsoft.aspnetcore") || strings.Contains(in.body, "webapplication") ||
				strings.Contains(in.body, "controllerbase") || strings.Contains(in.body, "[apicontroller]") ||
				strings.Contains(in.body, "mapget(") || strings.Contains(in.body, "mappost(") ||
				strings.Contains(in.body, "[fromservices]") || strings.Contains(in.body, "addcontrollers(") ||
				(in.ext == ".cs" && (strings.Contains(in.p, "/controllers/") || strings.HasSuffix(in.p, "controller.cs")))
		},
		packs: []FrameworkPack{FrameworkAspNetCore},
	},
	{
		// Deno: deno.json(c), Deno.* APIs, jsr: / npm: URL imports (Deno-native).
		id: "deno",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.p, "deno.json") || strings.HasSuffix(in.p, "deno.jsonc") ||
				strings.Contains(in.body, "deno.serve") || strings.Contains(in.body, "deno.env") ||
				strings.Contains(in.body, "from \"jsr:") || strings.Contains(in.body, "from 'jsr:") ||
				strings.Contains(in.body, "from \"npm:") || strings.Contains(in.body, "from 'npm:") ||
				strings.Contains(in.body, "// @deno") || strings.Contains(in.body, "deno.land/")
		},
		packs: []FrameworkPack{FrameworkDeno},
	},
	{
		// Bun: Bun.* APIs, bunfig, lock markers in path, bun: imports.
		id: "bun",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.p, "bun.lockb") || strings.Contains(in.p, "bun.lock") ||
				strings.Contains(in.p, "bunfig.toml") || strings.Contains(in.body, "bun.serve") ||
				strings.Contains(in.body, "bun.file") || strings.Contains(in.body, "bun.env") ||
				strings.Contains(in.body, "from \"bun:") || strings.Contains(in.body, "from 'bun:") ||
				strings.Contains(in.body, "bun.build")
		},
		packs: []FrameworkPack{FrameworkBun},
	},
	{
		// Cloudflare Workers: wrangler + ExportedHandler / default { fetch } patterns.
		id: "cloudflare_workers",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.p, "wrangler.toml") || strings.Contains(in.p, "wrangler.json") ||
				strings.Contains(in.body, "@cloudflare/workers") || strings.Contains(in.body, "cloudflare:workers") ||
				strings.Contains(in.body, "exportedhandler") || strings.Contains(in.body, "executioncontext") ||
				(in.isJS && strings.Contains(in.body, "async fetch(") &&
					(strings.Contains(in.body, "export default") || strings.Contains(in.body, "satisfies exportedhandler")))
		},
		packs: []FrameworkPack{FrameworkCloudflareWorkers, FrameworkEdge},
	},
	{
		// Generic edge runtime flags (Vercel/Netlify/Next edge config, EdgeRuntime).
		// Do not require Next — standalone edge handlers also use these markers.
		id: "edge",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "edgeruntime") ||
				strings.Contains(in.body, `runtime = "edge"`) || strings.Contains(in.body, "runtime = 'edge'") ||
				strings.Contains(in.body, `runtime: "edge"`) || strings.Contains(in.body, "runtime: 'edge'") ||
				strings.Contains(in.body, `runtime:"edge"`) || strings.Contains(in.body, "runtime:'edge'")
		},
		packs: []FrameworkPack{FrameworkEdge},
	},
	{
		// Flutter: package:flutter/*, widget bases, GoRouter, or pubspec flutter SDK.
		id: "flutter",
		match: func(in frameworkDetectInput) bool {
			if !(in.ext == ".dart" || strings.HasSuffix(in.p, "pubspec.yaml") || strings.HasSuffix(in.p, "pubspec.yml")) {
				return false
			}
			return strings.Contains(in.body, "package:flutter/") || strings.Contains(in.body, "package:flutter_riverpod") ||
				strings.Contains(in.body, "package:go_router") || strings.Contains(in.body, "statelesswidget") ||
				strings.Contains(in.body, "statefulwidget") || strings.Contains(in.body, "consumerwidget") ||
				strings.Contains(in.body, "gorouter(") || strings.Contains(in.body, "materialapp(") ||
				strings.Contains(in.body, "cupertinoapp(") ||
				((strings.HasSuffix(in.p, "pubspec.yaml") || strings.HasSuffix(in.p, "pubspec.yml")) &&
					(strings.Contains(in.body, "flutter:") || strings.Contains(in.body, "sdk: flutter")))
		},
		packs: []FrameworkPack{FrameworkFlutter},
	},
	{
		// React Native: react-native / @react-navigation, AppRegistry, screens/ conventions.
		id: "react_native",
		match: func(in frameworkDetectInput) bool {
			return in.isJS && (strings.Contains(in.body, "from 'react-native'") || strings.Contains(in.body, `from "react-native"`) ||
				strings.Contains(in.body, "from 'react-native/") || strings.Contains(in.body, `from "react-native/`) ||
				strings.Contains(in.body, "require('react-native") || strings.Contains(in.body, `require("react-native`) ||
				strings.Contains(in.body, "@react-navigation/") || strings.Contains(in.body, "react-native-screens") ||
				strings.Contains(in.body, "appregistry.registercomponent") ||
				strings.Contains(in.body, "createnativestacknavigator") || strings.Contains(in.body, "createbottomtabnavigator") ||
				strings.Contains(in.body, "navigationcontainer") ||
				((strings.Contains(in.p, "/screens/") || strings.HasPrefix(in.p, "screens/") ||
					strings.Contains(in.p, "/navigation/") || strings.HasPrefix(in.p, "navigation/")) &&
					(strings.Contains(in.body, "react-native") || strings.Contains(in.body, "@react-navigation") ||
						strings.Contains(in.body, "usesafeareainsets") ||
						strings.HasSuffix(filepath.Base(in.p), "screen.tsx") ||
						strings.HasSuffix(filepath.Base(in.p), "screen.ts") ||
						strings.HasSuffix(filepath.Base(in.p), "screen.jsx") ||
						strings.HasSuffix(filepath.Base(in.p), "screen.js"))))
		},
		packs: []FrameworkPack{FrameworkReactNative, FrameworkReact},
	},
	{
		id: "gin",
		match: func(in frameworkDetectInput) bool {
			return in.ext == ".go" && (strings.Contains(in.imps, "gin-gonic/gin") || strings.Contains(in.body, "gin-gonic/gin") ||
				strings.Contains(in.p, "/gin/") || strings.HasPrefix(in.p, "gin/") ||
				strings.Contains(in.body, "gin.context") || strings.HasPrefix(strings.TrimSpace(in.body), "package gin"))
		},
		packs: []FrameworkPack{FrameworkGin},
	},
	{
		id: "fiber",
		match: func(in frameworkDetectInput) bool {
			return in.ext == ".go" && (strings.Contains(in.imps, "gofiber/fiber") || strings.Contains(in.body, "gofiber/fiber") ||
				strings.Contains(in.p, "/fiber/") || strings.HasPrefix(in.p, "fiber/") ||
				strings.Contains(in.body, "fiber.ctx") || strings.HasPrefix(strings.TrimSpace(in.body), "package fiber"))
		},
		packs: []FrameworkPack{FrameworkFiber},
	},
	{
		id: "echo",
		match: func(in frameworkDetectInput) bool {
			return in.ext == ".go" && (strings.Contains(in.imps, "labstack/echo") || strings.Contains(in.body, "labstack/echo") ||
				strings.Contains(in.p, "/echo/") || strings.HasPrefix(in.p, "echo/") ||
				strings.Contains(in.body, "echo.context") || strings.HasPrefix(strings.TrimSpace(in.body), "package echo"))
		},
		packs: []FrameworkPack{FrameworkEcho},
	},
	{
		id: "chi",
		match: func(in frameworkDetectInput) bool {
			return in.ext == ".go" && (strings.Contains(in.imps, "go-chi/chi") || strings.Contains(in.body, "go-chi/chi") ||
				strings.Contains(in.p, "/chi/") || strings.HasPrefix(in.p, "chi/") ||
				strings.Contains(in.body, "chi.mux") || strings.Contains(in.body, "chi.newrouter") ||
				strings.HasPrefix(strings.TrimSpace(in.body), "package chi"))
		},
		packs: []FrameworkPack{FrameworkChi},
	},
	{
		id: "beego",
		match: func(in frameworkDetectInput) bool {
			return in.ext == ".go" && (strings.Contains(in.imps, "beego/beego") || strings.Contains(in.imps, "astaxie/beego") ||
				strings.Contains(in.body, "beego/beego") || strings.Contains(in.body, "astaxie/beego") ||
				strings.Contains(in.p, "/beego/") || strings.HasPrefix(in.p, "beego/") ||
				strings.Contains(in.body, "beego.router") || strings.Contains(in.body, "beego.controller") ||
				strings.HasPrefix(strings.TrimSpace(in.body), "package beego"))
		},
		packs: []FrameworkPack{FrameworkBeego},
	},
	{
		id: "prisma",
		match: func(in frameworkDetectInput) bool {
			return strings.HasSuffix(in.p, ".prisma") || strings.Contains(in.body, "@prisma/client") ||
				strings.Contains(in.body, "prismaclient") || strings.Contains(in.p, "schema.prisma")
		},
		packs: []FrameworkPack{FrameworkPrisma},
	},
	{
		id: "typeorm",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "typeorm") || strings.Contains(in.body, "@entity(") ||
				strings.Contains(in.body, "@manytoone") || strings.Contains(in.body, "getrepository(")
		},
		packs: []FrameworkPack{FrameworkTypeORM},
	},
	{
		id: "sequelize",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "sequelize") ||
				((strings.Contains(in.body, ".hasmany(") || strings.Contains(in.body, ".belongsto(")) &&
					(strings.Contains(in.body, "sequelize") || strings.Contains(in.p, "models/")))
		},
		packs: []FrameworkPack{FrameworkSequelize},
	},
	{
		// Drizzle ORM: drizzle-orm imports, *Table builders, db.query.*.findMany.
		id: "drizzle",
		match: func(in frameworkDetectInput) bool {
			return strings.Contains(in.body, "drizzle-orm") || strings.Contains(in.body, "drizzle-kit") ||
				strings.Contains(in.body, "pgtable(") || strings.Contains(in.body, "sqlitetable(") ||
				strings.Contains(in.body, "mysqltable(") || strings.Contains(in.body, "singlestoretable(") ||
				strings.Contains(in.body, "db.query.") ||
				(strings.Contains(in.body, "relations(") && (strings.Contains(in.body, "drizzle") ||
					strings.Contains(in.p, "/schema") || strings.Contains(in.p, "/db/")))
		},
		packs: []FrameworkPack{FrameworkDrizzle},
	},
	{
		// SwiftUI: import SwiftUI + View / NavigationStack markers (.swift only).
		id: "swiftui",
		match: func(in frameworkDetectInput) bool {
			return in.ext == ".swift" && (strings.Contains(in.body, "import swiftui") ||
				strings.Contains(in.body, ": view") || strings.Contains(in.body, "navigationstack") ||
				strings.Contains(in.body, "navigationlink") || strings.Contains(in.body, "navigationview"))
		},
		packs: []FrameworkPack{FrameworkSwiftUI},
	},
}

// --- Live-layout helpers for frameworkPackRules (keep DetectFrameworkPacks thin) ---

func hasNextJSMarkers(in frameworkDetectInput) bool {
	return strings.Contains(in.body, "from \"next/") || strings.Contains(in.body, "from 'next/") ||
		strings.Contains(in.body, "from \"next\"") || strings.Contains(in.body, "from 'next'") ||
		strings.Contains(in.body, "require(\"next/") || strings.Contains(in.body, "require('next/") ||
		strings.Contains(in.body, "require(\"next\")") || strings.Contains(in.body, "require('next')") ||
		strings.Contains(in.body, "nextresponse") || strings.Contains(in.body, "nextrequest") ||
		strings.Contains(in.body, "next/server") || strings.Contains(in.body, "next/navigation") ||
		strings.Contains(in.body, "next/image") || strings.Contains(in.body, "next/link") ||
		strings.Contains(in.body, "next/headers") || strings.Contains(in.body, "next/font") ||
		strings.Contains(in.body, "next/document") || strings.Contains(in.body, "next/router") ||
		strings.Contains(in.body, "nextdynamic")
}

func isNextPagesRouterRelPath(p string) bool {
	return strings.HasPrefix(p, "pages/") || strings.Contains(p, "/pages/")
}

func looksLikeNextPagesFile(in frameworkDetectInput) bool {
	// Ionic (and similar) also use src/pages/ — require Next signals or classic page names.
	if strings.Contains(in.body, "@ionic/") || strings.Contains(in.body, "ionpage") ||
		strings.Contains(in.body, "ionrouteroutlet") {
		return false
	}
	base := filepath.Base(in.p)
	if strings.HasPrefix(base, "_app.") || strings.HasPrefix(base, "_document.") ||
		strings.HasPrefix(base, "_error.") || strings.HasPrefix(base, "index.") ||
		strings.Contains(base, "[") {
		return true
	}
	if strings.Contains(in.body, "getserversideprops") || strings.Contains(in.body, "getstaticprops") ||
		strings.Contains(in.body, "getinitialprops") || strings.Contains(in.body, "getstaticpaths") {
		return true
	}
	return strings.Contains(in.body, "export default")
}

func hasNuxtBodyMarkers(in frameworkDetectInput) bool {
	if strings.Contains(in.body, "from '#app'") || strings.Contains(in.body, "from \"#app\"") ||
		strings.Contains(in.body, "from '#imports'") || strings.Contains(in.body, "from \"#imports\"") ||
		strings.Contains(in.body, "definenuxt") || strings.Contains(in.body, "definepagemeta(") ||
		strings.Contains(in.body, "usefetch(") || strings.Contains(in.body, "useasyncdata(") {
		return true
	}
	// useState is Nuxt-conventional in composables/ and page SFCs; not every JS hook.
	if strings.Contains(in.body, "usestate(") &&
		(isNuxtComposablesPath(in.p) || strings.HasSuffix(in.p, ".vue")) {
		return true
	}
	return false
}

func isNuxtPagesOrLayoutsPath(p string) bool {
	return strings.HasPrefix(p, "pages/") || strings.Contains(p, "/pages/") ||
		strings.HasPrefix(p, "layouts/") || strings.Contains(p, "/layouts/")
}

func isNuxtServerAPIPath(p string) bool {
	return strings.HasPrefix(p, "server/api/") || strings.Contains(p, "/server/api/")
}

func isNuxtComposablesPath(p string) bool {
	return strings.HasPrefix(p, "composables/") || strings.Contains(p, "/composables/")
}

func isNuxtPluginsPath(p string) bool {
	return strings.HasPrefix(p, "plugins/") || strings.Contains(p, "/plugins/")
}

func isNuxtMiddlewarePath(p string) bool {
	return strings.HasPrefix(p, "middleware/") || strings.Contains(p, "/middleware/")
}

func looksLikeNuxtComposable(in frameworkDetectInput) bool {
	base := filepath.Base(in.p)
	if !strings.HasPrefix(base, "use") {
		return false
	}
	return strings.Contains(in.body, "export function use") || strings.Contains(in.body, "export const use") ||
		strings.Contains(in.body, "usestate(") || strings.Contains(in.body, "usefetch(") ||
		strings.Contains(in.body, "useasyncdata(") || strings.Contains(in.body, "from '#") ||
		strings.Contains(in.body, "definenuxt")
}

func isLaravelAppTreePath(p string) bool {
	for _, seg := range []string{
		"app/http/", "app/models/", "app/providers/", "app/console/commands/",
		"app/jobs/", "app/events/", "app/listeners/", "app/policies/",
		"app/livewire/", "app/services/", "app/actions/", "app/enums/",
		"app/dtos/", "app/mail/", "app/notifications/", "app/rules/",
		"app/view/components/",
	} {
		if strings.HasPrefix(p, seg) || strings.Contains(p, "/"+seg) {
			return true
		}
	}
	return false
}

func hasWordPressBodyMarkers(body string) bool {
	for _, marker := range []string{
		"add_action(", "add_filter(", "do_action(", "apply_filters(",
		"register_rest_route(", "register_post_type(", "register_taxonomy(",
		"register_block_type(", "add_shortcode(", "wp_enqueue_script(",
		"wp_enqueue_style(", "plugin_dir_path(", "plugin_dir_url(",
		"get_template_part(", "wp_nonce_field(", "$wpdb", "wp_rest",
		"plugin name:", "theme name:", "abspath", "add_theme_support(",
		"get_header(", "get_footer(", "get_stylesheet_uri(",
	} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

func hasAngularMarkers(in frameworkDetectInput) bool {
	return strings.Contains(in.body, "@angular/") || strings.Contains(in.body, "@ngmodule(") ||
		strings.Contains(in.body, "@component(")
}

func hasNestHTTPMethodDecorator(in frameworkDetectInput) bool {
	for _, d := range []string{"@get(", "@post(", "@put(", "@patch(", "@delete(", "@options(", "@head(", "@all("} {
		if strings.Contains(in.body, d) {
			return true
		}
	}
	return false
}

func isExpressMiddlewarePath(p string) bool {
	return strings.HasPrefix(p, "middleware/") || strings.Contains(p, "/middleware/")
}
