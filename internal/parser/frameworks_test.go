package parser

import "testing"

func TestDetectFrameworkPacks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		body string
		want string
	}{
		{"app/page.tsx", "import React from \"react\";\nexport default function Page(){}", "nextjs"},
		{"app/api/health/route.ts", "import { NextResponse } from \"next/server\";\nexport async function GET(){ return NextResponse.json({}) }", "nextjs"},
		{"nuxt.config.ts", "export default defineNuxtConfig({})", "nuxt"},
		{"composables/useCounter.ts", "export function useCounter(){ return useState('n', () => 0) }", "nuxt"},
		{"src/app/hero.component.ts", "import { Component } from '@angular/core';\n@Component({selector:'app-hero',template:''})\nexport class HeroComponent {}", "angular"},
		{"src/routes/+server.ts", "export const GET = async () => {}", "sveltekit"},
		{"app/routes/_index.tsx", "import { json } from '@remix-run/node';\nexport async function loader(){ return json({}) }", "remix"},
		{"src/main/main.js", "const { ipcMain } = require('electron');\nipcMain.handle('x', fn);", "electron"},
		{"components/Greeter.vue", "<script setup>\ndefineProps({})\n</script>", "vue"},
		{"src/pages/index.astro", "---\nconst x = 1\n---\n", "astro"},
		{"src/main.ts", "import { registerPlugin } from \"@capacitor/core\"", "capacitor"},
		{"src/pages/HomePage.tsx", "import { IonPage } from '@ionic/react';\nexport function HomePage(){ return <IonPage/> }", "ionic"},
		{"src/db/schema.ts", "import { pgTable } from 'drizzle-orm/pg-core';\nexport const users = pgTable('users', {});", "drizzle"},
		{"Views/HomeView.swift", "import SwiftUI\nstruct HomeView: View { var body: some View { Text(\"x\") } }", "swiftui"},
		{"routes/web.php", "Route::get('/x', fn()=>1);", "laravel"},
		{"src/Controller/X.php", "<?php\nuse Symfony\\Component\\Routing\\Attribute\\Route;\n#[Route('/x')]\nclass X {}", "symfony"},
		{"src/app.ts", "import Fastify from \"fastify\";\nconst app = Fastify();\napp.get('/', async () => ({}));\n", "fastify"},
		{"src/index.ts", "import { Hono } from \"hono\";\nconst app = new Hono();\napp.get('/', (c) => c.text('ok'));\n", "hono"},
		{"wp-content/plugins/x/plugin.php", "add_action('init', 'boot');", "wordpress"},
		{"config/routes.rb", "Rails.application.routes.draw do\n  get '/x', to: 'users#show'\nend\n", "rails"},
		{"lib/sinatra/base.rb", "module Sinatra\n  class Base\n  end\nend\n", "sinatra"},
		{"api.py", "from fastapi import FastAPI\napp=FastAPI()", "fastapi"},
		{"urls.py", "urlpatterns = [path('x/', views.home)]", "django"},
		{"app.py", "from flask import Flask\napp=Flask(__name__)\n@app.route('/')\ndef index():\n    return 'ok'", "flask"},
		{"api/views.py", "from rest_framework.views import APIView\nclass UserView(APIView):\n    pass", "djangorest"},
		{"Controllers/UsersController.cs", "using Microsoft.AspNetCore.Mvc;\n[ApiController]\nclass UsersController : ControllerBase {}", "aspnetcore"},
		{"lib/demo_web/router.ex", "defmodule DemoWeb.Router do\n  use DemoWeb, :router\nend", "phoenix"},
		{"src/main/java/demo/Owner.java", "import jakarta.persistence.Entity;\n@Entity\nclass Owner {}", "jpa"},
		{"main.ts", "Deno.serve(handler);\nfunction handler(req: Request){ return new Response('ok') }", "deno"},
		{"index.ts", "Bun.serve({ fetch: handler });\nfunction handler(req: Request){ return new Response('ok') }", "bun"},
		{"src/index.ts", "export default {\n  async fetch(req: Request){ return new Response('ok') }\n}", "cloudflare_workers"},
		{"api/edge.ts", "export const runtime = 'edge';\nexport default function handler(){ return new Response('ok') }", "edge"},
	}
	for _, tc := range cases {
		got := DetectFrameworkPacks(tc.path, nil, tc.body)
		found := false
		for _, g := range got {
			if g == tc.want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("path %q expected framework %q, got %v", tc.path, tc.want, got)
		}
	}
}

func TestDetectFrameworkPacks_LaravelAppNotNextJS(t *testing.T) {
	t.Parallel()
	got := DetectFrameworkPacks("app/Models/User.php", nil, "<?php\nnamespace App\\Models;\nclass User {}\n")
	for _, g := range got {
		if g == "nextjs" {
			t.Fatalf("Laravel PHP under app/ must not be tagged nextjs, got %v", got)
		}
	}
	got = DetectFrameworkPacks("app/Http/Controllers/Controller.php", nil, "<?php\nnamespace App\\Http\\Controllers;\nclass Controller {}\n")
	for _, g := range got {
		if g == "nextjs" {
			t.Fatalf("Laravel controller must not be tagged nextjs, got %v", got)
		}
	}
}
