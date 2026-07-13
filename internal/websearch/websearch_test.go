package websearch

import (
	"strings"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	t.Setenv("CODEHELPER_INDEX_HOME", "") // ensure RegistryDir uses HOME
	t.Setenv("HOME", t.TempDir())
	// Clear any inherited env overrides so Load/Save see only the file.
	for _, k := range []string{"CODEHELPER_SEARCH_PROVIDER", "TAVILY_API_KEY", "CODEHELPER_TAVILY_KEY", "BRAVE_API_KEY", "CODEHELPER_BRAVE_KEY", "BRAVE_SEARCH_API_KEY"} {
		t.Setenv(k, "")
	}
	want := Config{Provider: Brave, BraveKey: "BSA-secret"}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("round trip: got %+v want %+v", got, want)
	}
}

func TestChooseProvider(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		override string
		want     string
	}{
		{"override wins", Config{Provider: Tavily, TavilyKey: "k"}, "brave", Brave},
		{"configured", Config{Provider: Brave}, "", Brave},
		{"tavily key auto", Config{TavilyKey: "k"}, "", Tavily},
		{"brave key auto", Config{BraveKey: "k"}, "", Brave},
		{"empty falls to ddg", Config{}, "", DuckDuckGo},
		{"bad override ignored, key auto", Config{TavilyKey: "k"}, "nonsense", Tavily},
	}
	for _, c := range cases {
		if got := ChooseProvider(c.cfg, c.override); got != c.want {
			t.Errorf("%s: ChooseProvider=%q want %q", c.name, got, c.want)
		}
	}
}

func TestEnvOverridesFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Save(Config{Provider: DuckDuckGo}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEHELPER_SEARCH_PROVIDER", "tavily")
	t.Setenv("TAVILY_API_KEY", "tvly-env")
	c := Effective()
	if c.Provider != Tavily || c.TavilyKey != "tvly-env" {
		t.Fatalf("env should override file: %+v", c)
	}
}

func TestParseTavily(t *testing.T) {
	data := []byte(`{"answer":"Go 1.25 adds X.","results":[
		{"title":"Go 1.25 release notes","url":"https://go.dev/doc/go1.25","content":"  details   here  "},
		{"title":"Blog","url":"https://go.dev/blog","content":"more"}]}`)
	resp, err := parseTavily(data, "go 1.25")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != Tavily || resp.Answer != "Go 1.25 adds X." || len(resp.Results) != 2 {
		t.Fatalf("unexpected: %+v", resp)
	}
	if resp.Results[0].URL != "https://go.dev/doc/go1.25" || resp.Results[0].Snippet != "details here" {
		t.Fatalf("result0 wrong: %+v", resp.Results[0])
	}
}

func TestParseBrave(t *testing.T) {
	data := []byte(`{"web":{"results":[
		{"title":"<b>Rust</b> docs","url":"https://doc.rust-lang.org","description":"the <em>book</em>"}]}}`)
	resp, err := parseBrave(data, "rust")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "Rust docs" || resp.Results[0].Snippet != "the book" {
		t.Fatalf("brave parse/strip wrong: %+v", resp.Results)
	}
}

func TestParseDuckDuckGoAndURLDecode(t *testing.T) {
	html := []byte(`<div><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa&rut=x">Example <b>Title</b></a>` +
		`<a class="result__snippet" href="x">a useful snippet</a></div>`)
	res := parseDuckDuckGo(html, 5)
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if res[0].URL != "https://example.com/a" {
		t.Errorf("url decode wrong: %q", res[0].URL)
	}
	if res[0].Title != "Example Title" || !strings.Contains(res[0].Snippet, "useful snippet") {
		t.Errorf("title/snippet wrong: %+v", res[0])
	}
}

func TestCapSnippet(t *testing.T) {
	long := strings.Repeat("x", 400)
	if got := capSnippet(long); len([]rune(got)) != 301 { // 300 + ellipsis
		t.Fatalf("capSnippet len = %d", len([]rune(got)))
	}
	if capSnippet("  a\n b ") != "a b" {
		t.Fatal("capSnippet should collapse whitespace")
	}
}
