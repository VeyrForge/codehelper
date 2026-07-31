package indexer

import "testing"

func TestLanguageFromExt(t *testing.T) {
	cases := map[string]string{
		"a.go":                "go",
		"x.php":               "php",
		"z.kt":                "kotlin",
		"p.sql":               "sql",
		"foo.component.ts":    "typescript",
		"Dockerfile":          "dockerfile",
		"Dockerfile.dev":      "dockerfile",
		"Makefile":            "makefile",
		"docker-compose.yml":  "compose",
		"compose.yaml":        "compose",
		"k8s/deployment.yaml": "kubernetes",
		"playbooks/site.yml":  "ansible",
		"deploy.ps1":          "powershell",
		"src/greeter.zig":     "zig",
		"contracts/G.sol":     "solidity",
		"src/demo/g.clj":      "clojure",
		"src/greeter.erl":     "erlang",
		"Greeter.fs":          "fsharp",
		"greeter.R":           "r",
		"Greeter.pm":          "perl",
		"greeter.ml":          "ocaml",
		"Greeter.hs":          "haskell",
	}
	for path, want := range cases {
		if got := languageFromExt(path); got != want {
			t.Fatalf("%s: got %q want %q", path, got, want)
		}
	}
}

func TestSourceExtensionsCoversLiteBeds(t *testing.T) {
	for _, ext := range []string{
		".zig", ".sol", ".clj", ".cljs", ".cljc", ".erl", ".hrl", ".fs", ".fsi", ".fsx",
		".r", ".pl", ".pm", ".t", ".ml", ".mli", ".hs", ".lhs",
		".ps1", ".psm1",
	} {
		if _, ok := SourceExtensions[ext]; !ok {
			t.Fatalf("SourceExtensions missing %s (paired soft-skip beds will index 0 files)", ext)
		}
	}
}
