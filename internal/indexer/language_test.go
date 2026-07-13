package indexer

import "testing"

func TestLanguageFromExt(t *testing.T) {
	cases := map[string]string{
		"a.go":             "go",
		"x.php":            "php",
		"z.kt":             "kotlin",
		"p.sql":            "sql",
		"foo.component.ts": "typescript",
	}
	for path, want := range cases {
		if got := languageFromExt(path); got != want {
			t.Fatalf("%s: got %q want %q", path, got, want)
		}
	}
}
