package parser

import (
	"slices"
	"testing"
)

// Live-project layouts (minimal-testbeds / real app trees) + false-framework guards.
func TestDetectFrameworkPacks_LiveLayouts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		path    string
		body    string
		require []string
		forbid  []string
	}{
		// Laravel
		{
			name:    "laravel_eloquent_model",
			path:    "app/Models/User.php",
			body:    "<?php\nnamespace App\\Models;\nuse Illuminate\\Foundation\\Auth\\User as Authenticatable;\nclass User extends Authenticatable {}\n",
			require: []string{"laravel"},
			forbid:  []string{"nextjs", "wordpress", "symfony"},
		},
		{
			name:    "laravel_controller_tree",
			path:    "app/Http/Controllers/UserController.php",
			body:    "<?php\nnamespace App\\Http\\Controllers;\nclass UserController {}\n",
			require: []string{"laravel"},
			forbid:  []string{"nextjs", "aspnetcore"},
		},
		{
			name:    "laravel_routes",
			path:    "routes/web.php",
			body:    "<?php\nRoute::get('/users', [UserController::class, 'index']);\n",
			require: []string{"laravel"},
			forbid:  []string{"rails", "wordpress"},
		},
		{
			name:    "laravel_factory",
			path:    "database/factories/UserFactory.php",
			body:    "<?php\nclass UserFactory {}\n",
			require: []string{"laravel"},
			forbid:  []string{"wordpress"},
		},
		// WordPress
		{
			name:    "wp_plugin_under_wp_content",
			path:    "wp-content/plugins/probe-plugin/probe-plugin.php",
			body:    "<?php\nclass ProbePlugin {}\n",
			require: []string{"wordpress"},
			forbid:  []string{"laravel"},
		},
		{
			name:    "wp_theme_functions",
			path:    "wp-content/themes/probe-theme/functions.php",
			body:    "<?php\nadd_action('after_setup_theme', 'boot');\nwp_enqueue_style('probe', get_stylesheet_uri());\n",
			require: []string{"wordpress"},
			forbid:  []string{"laravel"},
		},
		{
			name:    "wp_rest_outside_tree",
			path:    "includes/rest.php",
			body:    "<?php\nregister_rest_route('probe/v1', '/items', []);\n",
			require: []string{"wordpress"},
			forbid:  []string{"laravel"},
		},
		// Next.js
		{
			name:    "next_app_router_page",
			path:    "app/page.tsx",
			body:    "import { greet } from \"../lib/greet\";\nexport default function Page(){ return <h1>{greet('next')}</h1> }\n",
			require: []string{"nextjs", "react"},
			forbid:  []string{"nuxt", "nestjs", "remix"},
		},
		{
			name:    "next_route_handler",
			path:    "app/api/probe/route.ts",
			body:    "import { NextResponse } from \"next/server\";\nexport async function GET(){ return NextResponse.json({}) }\n",
			require: []string{"nextjs"},
			forbid:  []string{"nuxt", "express", "nestjs"},
		},
		{
			name:    "next_middleware",
			path:    "middleware.ts",
			body:    "import { NextResponse } from \"next/server\";\nimport type { NextRequest } from \"next/server\";\nexport function middleware(req: NextRequest){ return NextResponse.next() }\n",
			require: []string{"nextjs"},
			forbid:  []string{"express", "nuxt", "electron"},
		},
		{
			name:    "next_pages_index",
			path:    "pages/index.tsx",
			body:    "export default function Home(){ return null }\n",
			require: []string{"nextjs", "react"},
			forbid:  []string{"nuxt"},
		},
		// Nuxt
		{
			name:    "nuxt_page_sfc",
			path:    "pages/index.vue",
			body:    "<script setup lang=\"ts\">\nconst { data } = await useFetch('/api/health')\n</script>\n<template><pre>{{ data }}</pre></template>\n",
			require: []string{"nuxt", "vue"},
			forbid:  []string{"nextjs", "nestjs"},
		},
		{
			name:    "nuxt_composable",
			path:    "composables/useCounter.ts",
			body:    "export function useCounter(){ return useState('n', () => 0) }\n",
			require: []string{"nuxt"},
			forbid:  []string{"nextjs", "nestjs"},
		},
		{
			name:    "nuxt_server_api",
			path:    "server/api/probe/index.get.ts",
			body:    "export default defineEventHandler(() => ({ ok: true }))\n",
			require: []string{"nuxt"},
			forbid:  []string{"nextjs", "express"},
		},
		{
			name:    "nuxt_route_middleware",
			path:    "middleware/auth.ts",
			body:    "export default defineNuxtRouteMiddleware((to) => { if (to.path.startsWith('/admin')) return navigateTo('/') })\n",
			require: []string{"nuxt"},
			forbid:  []string{"express", "nextjs"},
		},
		// NestJS
		{
			name:    "nest_module",
			path:    "src/cats/cats.module.ts",
			body:    "import { Module } from '@nestjs/common';\n@Module({providers:[]})\nexport class CatsModule {}\n",
			require: []string{"nestjs"},
			forbid:  []string{"angular", "nextjs"},
		},
		{
			name:    "nest_controller",
			path:    "src/cats/cats.controller.ts",
			body:    "import { Controller, Get } from '@nestjs/common';\n@Controller('cats')\nexport class CatsController { @Get() list(){ return [] } }\n",
			require: []string{"nestjs"},
			forbid:  []string{"angular", "spring", "express"},
		},
		{
			name:    "nest_service",
			path:    "src/cats/cats.service.ts",
			body:    "import { Injectable } from '@nestjs/common';\n@Injectable()\nexport class CatsService { findAll(){ return [] } }\n",
			require: []string{"nestjs"},
			forbid:  []string{"angular"},
		},
		// Express / Node
		{
			name:    "express_application_lib",
			path:    "lib/application.js",
			body:    "function createApplication(){ var app = function(){}; app.use = function(fn){ return app }; return app }\nmodule.exports = createApplication;\n",
			require: []string{"express"},
			forbid:  []string{"nestjs", "nextjs", "nuxt"},
		},
		{
			name:    "express_middleware_file",
			path:    "middleware/logger.js",
			body:    "module.exports = function logger(req, res, next){ next() }\n",
			require: []string{"express"},
			forbid:  []string{"nuxt", "nestjs"},
		},
		{
			name:    "express_require",
			path:    "src/server.js",
			body:    "const express = require('express');\nconst app = express();\napp.get('/', (req,res)=>res.end());\n",
			require: []string{"express"},
			forbid:  []string{"nestjs", "fastify"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DetectFrameworkPacks(tc.path, nil, tc.body)
			for _, want := range tc.require {
				if !slices.Contains(got, want) {
					t.Fatalf("expected pack %q in %v", want, got)
				}
			}
			for _, bad := range tc.forbid {
				if slices.Contains(got, bad) {
					t.Fatalf("forbidden pack %q in %v", bad, got)
				}
			}
		})
	}
}

func TestDetectFrameworkPacks_FalseFrameworkGuards(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		path   string
		body   string
		forbid []string
	}{
		{
			name:   "bare_php_functions_not_wp",
			path:   "src/functions.php",
			body:   "<?php\nfunction helper(){ return 1; }\n",
			forbid: []string{"wordpress", "laravel"},
		},
		{
			name:   "extends_model_outside_laravel_tree",
			path:   "src/Domain/User.php",
			body:   "<?php\nclass User extends Model {}\n",
			forbid: []string{"laravel"},
		},
		{
			name:   "generic_service_ts_not_nest",
			path:   "src/billing/billing.service.ts",
			body:   "export class BillingService { charge(){} }\n",
			forbid: []string{"nestjs"},
		},
		{
			name:   "generic_module_ts_not_nest",
			path:   "src/billing/billing.module.ts",
			body:   "export class BillingModule {}\n",
			forbid: []string{"nestjs", "angular"},
		},
		{
			name:   "ionic_page_not_next",
			path:   "src/pages/SettingsPage.tsx",
			body:   "import { IonPage } from '@ionic/react';\nexport function SettingsPage(){ return <IonPage/> }\n",
			forbid: []string{"nextjs"},
		},
		{
			name:   "react_hooks_dir_not_nuxt",
			path:   "src/hooks/useToggle.ts",
			body:   "export function useToggle(){ return false }\n",
			forbid: []string{"nuxt"},
		},
		{
			name:   "vite_views_vue_not_nuxt",
			path:   "src/views/Home.vue",
			body:   "<script setup>\nconst n = 1\n</script>\n",
			forbid: []string{"nuxt"},
		},
		{
			name:   "laravel_php_not_next_or_nest",
			path:   "app/Services/Billing.php",
			body:   "<?php\nnamespace App\\Services;\nuse Illuminate\\Support\\Facades\\Log;\nclass Billing {}\n",
			forbid: []string{"nextjs", "nestjs", "wordpress"},
		},
		{
			name:   "mention_next_slash_in_comment_not_next",
			path:   "src/utils/format.ts",
			body:   "// see docs at example.com/next/guide\nexport function format(n: number){ return String(n) }\n",
			forbid: []string{"nextjs"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DetectFrameworkPacks(tc.path, nil, tc.body)
			for _, bad := range tc.forbid {
				if slices.Contains(got, bad) {
					t.Fatalf("forbidden pack %q in %v (path=%s)", bad, got, tc.path)
				}
			}
		})
	}
}
