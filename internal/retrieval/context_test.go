package retrieval

import "testing"

func TestClassifyContextKind(t *testing.T) {
	cases := map[string]string{
		"routes/api.php":               "entrypoint",
		"tests/feature/login_test.go":  "test",
		"config/app.go":                "config",
		"app/services/rate_limiter.go": "dependency",
		"app/api/users/route.ts":       "entrypoint",
		"src/routes/+server.ts":        "entrypoint",
		"backend/urls.py":              "entrypoint",
		"next.config.mjs":              "config",
		"capacitor.config.ts":          "config",
		"src/composables/use_auth.ts":  "dependency",
		"internal/auth/controller.go":  "implementation",
	}
	for path, want := range cases {
		if got := classifyContextKind(path); got != want {
			t.Fatalf("classifyContextKind(%q)=%q want %q", path, got, want)
		}
	}
}
