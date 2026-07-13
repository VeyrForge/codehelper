package review

import "testing"

func TestIsCodeSourceFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Sources we DO want regression tests for.
		{"internal/mcpsvc/register.go", true},
		{"internal/agent/loop.go", true},
		{"vscode-extension/src/coreClient.ts", true},
		{"scripts/build-go.mjs", true},
		{"pkg/types/types.go", true},

		// Not source.
		{".gitignore", false},
		{".vscodeignore", false},
		{".env.local", false},
		{"package.json", false},
		{"package-lock.json", false},
		{"vscode-extension/package-lock.json", false},
		{"tsconfig.json", false},
		{"go.sum", false},
		{"README.md", false},
		{"docs/intro.md", false},
		{"vscode-extension/media/panel.css", false},
		{"assets/logo.svg", false},

		// Generated / vendored / compiled.
		{"vscode-extension/out/extension.js", false},
		{"vscode-extension/out/coreClient.js", false},
		{"dist/index.js", false},
		{"build/main.js", false},
		{"node_modules/lodash/index.js", false},
		{"vendor/example/example.go", false},
		{"target/debug/foo.rs", false},
		{"coverage/lcov-report/index.html", false},
	}
	for _, tc := range tests {
		got := IsCodeSourceFile(tc.path)
		if got != tc.want {
			t.Errorf("IsCodeSourceFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsTestPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"internal/mcpsvc/workspace_tools_test.go", true},
		{"internal/review/heuristics_test.go", true},
		{"src/__tests__/foo.test.ts", true},
		{"src/foo.spec.ts", true},
		{"src/foo.test.tsx", true},
		{"app/spec/users_spec.rb", true},
		{"tests/integration/foo.py", true},
		{"test/foo.py", true},
		{"src/test_helpers.py", true},

		{"internal/mcpsvc/workspace_tools.go", false},
		{"src/coreClient.ts", false},
		{"vscode-extension/test-runner.json", false},
	}
	for _, tc := range tests {
		got := IsTestPath(tc.path)
		if got != tc.want {
			t.Errorf("IsTestPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsBehavioralSymbolKind(t *testing.T) {
	for _, k := range []string{"function", "method", "FUNCTION", " Method "} {
		if !IsBehavioralSymbolKind(k) {
			t.Errorf("IsBehavioralSymbolKind(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"class", "interface", "type_alias", "enum", "namespace", "variable", "unknown", ""} {
		if IsBehavioralSymbolKind(k) {
			t.Errorf("IsBehavioralSymbolKind(%q) = true, want false", k)
		}
	}
}
