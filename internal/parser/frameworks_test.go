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
		{"nuxt.config.ts", "export default defineNuxtConfig({})", "nuxt"},
		{"src/routes/+server.ts", "export const GET = async () => {}", "sveltekit"},
		{"src/main.ts", "import { registerPlugin } from \"@capacitor/core\"", "capacitor"},
		{"routes/web.php", "Route::get('/x', fn()=>1);", "laravel"},
		{"wp-content/plugins/x/plugin.php", "add_action('init', 'boot');", "wordpress"},
		{"api.py", "from fastapi import FastAPI\napp=FastAPI()", "fastapi"},
		{"urls.py", "urlpatterns = [path('x/', views.home)]", "django"},
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
