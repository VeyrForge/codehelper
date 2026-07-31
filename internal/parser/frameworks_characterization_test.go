package parser

import (
	"slices"
	"testing"
)

// Characterization lock for DetectFrameworkPacks: exact sorted pack sets.
// Covers major packs + bleed guards. Do not loosen to "contains" assertions.
func TestDetectFrameworkPacks_Characterization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		path    string
		imports []string
		body    string
		want    []string
	}{
		{
			name: "react_jsx_path",
			path: "components/Button.jsx",
			body: "export function Button(){ return null }",
			want: []string{"react"},
		},
		{
			name: "next_app_router_page",
			path: "app/page.tsx",
			body: "import React from \"react\";\nexport default function Page(){}",
			want: []string{"nextjs", "react"},
		},
		{
			name: "next_app_router_route_handler",
			path: "app/api/health/route.ts",
			body: "import { NextResponse } from \"next/server\";\nexport async function GET(){ return NextResponse.json({}) }",
			want: []string{"nextjs"},
		},
		{
			name: "next_pages_router",
			path: "pages/index.tsx",
			body: "export default function Home(){ return null }",
			want: []string{"nextjs", "react"},
		},
		{
			name: "nuxt_config",
			path: "nuxt.config.ts",
			body: "export default defineNuxtConfig({})",
			want: []string{"nuxt"},
		},
		{
			name: "nuxt_composable_dir",
			path: "composables/useCounter.ts",
			body: "export function useCounter(){ return useState('n', () => 0) }",
			want: []string{"nuxt"},
		},
		{
			name: "angular_component",
			path: "src/app/hero.component.ts",
			body: "import { Component } from '@angular/core';\n@Component({selector:'app-hero',template:''})\nexport class HeroComponent {}",
			want: []string{"angular"},
		},
		{
			name: "svelte_extension",
			path: "src/lib/Widget.svelte",
			body: "<script>export let name</script>",
			want: []string{"svelte"},
		},
		{
			name: "sveltekit_server",
			path: "src/routes/+server.ts",
			body: "export const GET = async () => {}",
			want: []string{"sveltekit"},
		},
		{
			name: "sveltekit_page_svelte",
			path: "src/routes/+page.svelte",
			body: "<script>import { page } from '$app/stores'</script>",
			want: []string{"svelte", "sveltekit"},
		},
		{
			name: "remix_route_loader",
			path: "app/routes/_index.tsx",
			body: "import { json } from '@remix-run/node';\nexport async function loader(){ return json({}) }",
			want: []string{"react", "remix"},
		},
		{
			name: "electron_main_ipc",
			path: "src/main/main.js",
			body: "const { ipcMain } = require('electron');\nipcMain.handle('x', fn);",
			want: []string{"electron"},
		},
		{
			name: "vue_script_setup",
			path: "components/Greeter.vue",
			body: "<script setup>\ndefineProps({})\n</script>",
			want: []string{"vue"},
		},
		{
			name: "astro_page",
			path: "src/pages/index.astro",
			body: "---\nconst x = 1\n---\n",
			want: []string{"astro"},
		},
		{
			name: "capacitor_register",
			path: "src/main.ts",
			body: "import { registerPlugin } from \"@capacitor/core\"",
			want: []string{"capacitor"},
		},
		{
			name: "ionic_page",
			// Ionic React often lives under src/pages/ — must not bleed into Next Pages Router.
			path: "src/pages/HomePage.tsx",
			body: "import { IonPage } from '@ionic/react';\nexport function HomePage(){ return <IonPage/> }",
			want: []string{"ionic", "react"},
		},
		{
			name: "ionic_with_capacitor",
			path: "src/pages/HomePage.tsx",
			body: "import { IonPage } from '@ionic/react';\nimport { Plugins } from '@capacitor/core';\nexport function HomePage(){ return <IonPage/> }",
			want: []string{"capacitor", "ionic", "react"},
		},
		{
			name: "ionic_no_next_bleed",
			path: "src/app/home/HomePage.tsx",
			body: "import { IonPage } from '@ionic/react';\nexport function HomePage(){ return <IonPage/> }",
			want: []string{"ionic", "react"},
		},
		{
			name: "laravel_routes",
			path: "routes/web.php",
			body: "Route::get('/x', fn()=>1);",
			want: []string{"laravel"},
		},
		{
			name: "symfony_attribute_route",
			path: "src/Controller/X.php",
			body: "<?php\nuse Symfony\\Component\\Routing\\Attribute\\Route;\n#[Route('/x')]\nclass X {}",
			want: []string{"symfony"},
		},
		{
			name: "wordpress_plugin",
			path: "wp-content/plugins/x/plugin.php",
			body: "add_action('init', 'boot');",
			want: []string{"wordpress"},
		},
		{
			name: "rails_routes",
			path: "config/routes.rb",
			body: "Rails.application.routes.draw do\n  get '/x', to: 'users#show'\nend\n",
			want: []string{"rails"},
		},
		{
			name: "sinatra_base",
			path: "lib/sinatra/base.rb",
			body: "module Sinatra\n  class Base\n  end\nend\n",
			want: []string{"sinatra"},
		},
		{
			name: "django_urls",
			path: "urls.py",
			body: "urlpatterns = [path('x/', views.home)]",
			want: []string{"django"},
		},
		{
			name: "django_rest_apiview",
			path: "api/views.py",
			body: "from rest_framework.views import APIView\nclass UserView(APIView):\n    pass",
			want: []string{"django", "djangorest"},
		},
		{
			name: "fastapi_app",
			path: "api.py",
			body: "from fastapi import FastAPI\napp=FastAPI()",
			want: []string{"fastapi"},
		},
		{
			name: "flask_app",
			path: "app.py",
			body: "from flask import Flask\napp=Flask(__name__)\n@app.route('/')\ndef index():\n    return 'ok'",
			want: []string{"flask"},
		},
		{
			name: "nestjs_module",
			path: "src/cats/cats.module.ts",
			body: "import { Module } from '@nestjs/common';\n@Module({providers:[]})\nexport class CatsModule {}",
			want: []string{"nestjs"},
		},
		{
			name: "express_require",
			path: "src/server.js",
			body: "const express = require('express');\nconst app = express();\napp.get('/', (req,res)=>res.end());",
			want: []string{"express"},
		},
		{
			name: "fastify_import",
			path: "src/app.ts",
			body: "import Fastify from \"fastify\";\nconst app = Fastify();\napp.get('/', async () => ({}));\n",
			want: []string{"fastify"},
		},
		{
			name: "hono_import",
			path: "src/index.ts",
			body: "import { Hono } from \"hono\";\nconst app = new Hono();\napp.get('/', (c) => c.text('ok'));\n",
			want: []string{"hono"},
		},
		{
			name: "spring_restcontroller",
			path: "src/OwnerController.java",
			body: "package demo;\nimport org.springframework.web.bind.annotation.RestController;\n@RestController\nclass OwnerController {}",
			want: []string{"spring"},
		},
		{
			name: "jpa_entity",
			path: "src/main/java/demo/Owner.java",
			body: "import jakarta.persistence.Entity;\n@Entity\nclass Owner {}",
			want: []string{"jpa"},
		},
		{
			name: "jpa_hibernate",
			path: "src/main/java/demo/Owner.java",
			body: "import jakarta.persistence.Entity;\nimport org.hibernate.annotations.BatchSize;\n@Entity\nclass Owner {}",
			want: []string{"hibernate", "jpa"},
		},
		{
			name: "phoenix_router",
			path: "lib/demo_web/router.ex",
			body: "defmodule DemoWeb.Router do\n  use DemoWeb, :router\nend",
			want: []string{"phoenix"},
		},
		{
			name: "aspnetcore_controller",
			path: "Controllers/UsersController.cs",
			body: "using Microsoft.AspNetCore.Mvc;\n[ApiController]\nclass UsersController : ControllerBase {}",
			want: []string{"aspnetcore"},
		},
		{
			name: "deno_serve",
			path: "main.ts",
			body: "Deno.serve(handler);\nfunction handler(req: Request){ return new Response('ok') }",
			want: []string{"deno"},
		},
		{
			name: "bun_serve",
			path: "index.ts",
			body: "Bun.serve({ fetch: handler });\nfunction handler(req: Request){ return new Response('ok') }",
			want: []string{"bun"},
		},
		{
			name: "cloudflare_worker_default_fetch",
			path: "src/index.ts",
			body: "export default {\n  async fetch(req: Request){ return new Response('ok') }\n}",
			want: []string{"cloudflare_workers", "edge"},
		},
		{
			name: "edge_runtime_export",
			path: "api/edge.ts",
			body: "export const runtime = 'edge';\nexport default function handler(){ return new Response('ok') }",
			want: []string{"edge"},
		},
		{
			name: "flutter_stateless",
			path: "lib/main.dart",
			body: "import 'package:flutter/material.dart';\nclass App extends StatelessWidget { Widget build(c)=>MaterialApp(); }",
			want: []string{"flutter"},
		},
		{
			name: "react_native_screen",
			path: "screens/HomeScreen.tsx",
			body: "import { View, Text } from 'react-native';\nexport function HomeScreen(){ return <View><Text>hi</Text></View> }",
			want: []string{"react", "react_native"},
		},
		{
			name:    "gin_import",
			path:    "cmd/api/main.go",
			imports: []string{"github.com/gin-gonic/gin"},
			body:    "package main\nfunc main(){ r := gin.Default() }",
			want:    []string{"gin"},
		},
		{
			name:    "fiber_import",
			path:    "cmd/api/main.go",
			imports: []string{"github.com/gofiber/fiber/v2"},
			body:    "package main\nfunc main(){ app := fiber.New() }",
			want:    []string{"fiber"},
		},
		{
			name:    "echo_import",
			path:    "cmd/api/main.go",
			imports: []string{"github.com/labstack/echo/v4"},
			body:    "package main\nfunc main(){ e := echo.New() }",
			want:    []string{"echo"},
		},
		{
			name:    "chi_import",
			path:    "cmd/api/main.go",
			imports: []string{"github.com/go-chi/chi/v5"},
			body:    "package main\nfunc main(){ r := chi.NewRouter() }",
			want:    []string{"chi"},
		},
		{
			name:    "beego_import",
			path:    "cmd/api/main.go",
			imports: []string{"github.com/beego/beego/v2/server/web"},
			body:    "package main\nfunc main(){ beego.Run() }",
			want:    []string{"beego"},
		},
		{
			name: "prisma_schema",
			path: "prisma/schema.prisma",
			body: "model User { id Int @id }",
			want: []string{"prisma"},
		},
		{
			name: "typeorm_entity",
			path: "src/entities/User.ts",
			body: "import { Entity, ManyToOne } from 'typeorm';\n@Entity()\nexport class User { @ManyToOne(() => Org) org; }",
			want: []string{"typeorm"},
		},
		{
			name: "sequelize_models",
			path: "models/user.js",
			body: "User.hasMany(Post);\nPost.belongsTo(User);\nmodule.exports = { sequelize };",
			want: []string{"sequelize"},
		},
		{
			name: "drizzle_pgtable",
			path: "src/db/schema.ts",
			body: "import { pgTable } from 'drizzle-orm/pg-core';\nexport const users = pgTable('users', {});",
			want: []string{"drizzle"},
		},
		{
			name: "swiftui_view",
			path: "Views/HomeView.swift",
			body: "import SwiftUI\nstruct HomeView: View { var body: some View { Text(\"x\") } }",
			want: []string{"swiftui"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DetectFrameworkPacks(tc.path, tc.imports, tc.body)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("DetectFrameworkPacks(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestDetectFrameworkPacks_BleedGuards(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		path    string
		body    string
		forbid  []string
		require []string
	}{
		{
			name:   "laravel_model_not_nextjs",
			path:   "app/Models/User.php",
			body:   "<?php\nnamespace App\\Models;\nclass User {}\n",
			forbid: []string{"nextjs"},
		},
		{
			name:    "laravel_model_is_laravel",
			path:    "app/Models/User.php",
			body:    "<?php\nnamespace App\\Models;\nclass User extends Model {}\n",
			require: []string{"laravel"},
			forbid:  []string{"nextjs", "wordpress"},
		},
		{
			name:   "laravel_controller_not_nextjs",
			path:   "app/Http/Controllers/Controller.php",
			body:   "<?php\nnamespace App\\Http\\Controllers;\nclass Controller {}\n",
			forbid: []string{"nextjs"},
		},
		{
			name:   "bare_functions_php_not_wordpress",
			path:   "lib/helpers/functions.php",
			body:   "<?php\nfunction add(int $a, int $b): int { return $a + $b; }\n",
			forbid: []string{"wordpress", "laravel"},
		},
		{
			name:   "plain_service_ts_not_nestjs",
			path:   "src/users/users.service.ts",
			body:   "export class UsersService { findAll(){ return [] } }\n",
			forbid: []string{"nestjs", "angular", "nextjs"},
		},
		{
			name:   "ionic_pages_not_nextjs",
			path:   "src/pages/HomePage.tsx",
			body:   "import { IonPage } from '@ionic/react';\nexport function HomePage(){ return <IonPage/> }",
			forbid: []string{"nextjs"},
		},
		{
			name:   "react_hook_not_nuxt",
			path:   "src/hooks/useCounter.ts",
			body:   "export function useCounter(){ return { n: 0 } }\n",
			forbid: []string{"nuxt"},
		},
		{
			name:    "angular_not_nestjs",
			path:    "src/app/hero.component.ts",
			body:    "import { Component } from '@angular/core';\n@Component({selector:'app-hero',template:''})\nexport class HeroComponent {}",
			require: []string{"angular"},
			forbid:  []string{"nestjs"},
		},
		{
			name:    "nestjs_not_angular",
			path:    "src/cats/cats.module.ts",
			body:    "import { Module } from '@nestjs/common';\n@Module({providers:[]})\nexport class CatsModule {}",
			require: []string{"nestjs"},
			forbid:  []string{"angular"},
		},
		{
			name:   "nestjs_controller_not_spring",
			path:   "cats.controller.ts",
			body:   "import { Controller } from \"@nestjs/common\";\n@Controller(\"cats\")\nexport class CatsController {}",
			forbid: []string{"spring"},
		},
		{
			name:   "php_controllers_not_aspnetcore",
			path:   "app/Http/Controllers/UserController.php",
			body:   "<?php\nnamespace App\\Http\\Controllers;\nclass UserController {}",
			forbid: []string{"aspnetcore"},
		},
		{
			name:   "deno_main_not_electron",
			path:   "main.ts",
			body:   "Deno.serve(() => new Response('ok'));",
			forbid: []string{"electron"},
		},
		{
			name:   "plain_go_not_gin",
			path:   "cmd/api/main.go",
			body:   "package main\nfunc main(){}",
			forbid: []string{"gin", "fiber", "echo", "chi", "beego"},
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
