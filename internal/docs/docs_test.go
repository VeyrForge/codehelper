package docs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeFetcher serves canned bodies offline.
type fakeFetcher struct {
	bodies map[string]FetchResult
	calls  []string
}

func (f *fakeFetcher) Fetch(_ context.Context, u string) FetchResult {
	f.calls = append(f.calls, u)
	if r, ok := f.bodies[u]; ok {
		r.URL = u
		return r
	}
	return FetchResult{URL: u, StatusCode: 404}
}

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"^4.18.2":  "4.18.2",
		"~1.2.0":   "1.2.0",
		">=1.2,<2": "1.2",
		"v1.9.0":   "1.9.0",
		"*":        "",
		"latest":   "",
		"3.11":     "3.11",
		">= 2.0.0": "2.0.0",
	}
	for in, want := range cases {
		if got := normalizeVersion(in); got != want {
			t.Errorf("normalizeVersion(%q)=%q want %q", in, got, want)
		}
	}
}

func TestListDependenciesAndResolveVersion(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
	  "dependencies": {"next": "^15.0.0", "react": "18.2.0"},
	  "devDependencies": {"typescript": "~5.4.0"}
	}`)
	write(t, dir, "go.mod", "module x\n\ngo 1.22\n\nrequire (\n\tgithub.com/spf13/cobra v1.10.2\n\tgithub.com/x/y v0.1.0 // indirect\n)\n")
	write(t, dir, "requirements.txt", "django>=5.0\nrequests==2.31.0\n# comment\n")
	write(t, dir, "composer.json", `{"require": {"php": "^8.1", "laravel/framework": "^11.0"}}`)

	deps := ListDependencies(dir)
	if len(deps) < 7 {
		t.Fatalf("expected >=7 deps, got %d: %+v", len(deps), deps)
	}

	v, eco := ResolveVersion(dir, "next")
	if v != "15.0.0" || eco != "npm" {
		t.Errorf("next => %q,%q want 15.0.0,npm", v, eco)
	}
	if v, eco := ResolveVersion(dir, "cobra"); v != "1.10.2" || eco != "go" {
		t.Errorf("cobra (short name) => %q,%q want 1.10.2,go", v, eco)
	}
	if v, _ := ResolveVersion(dir, "laravel/framework"); v != "11.0" {
		t.Errorf("laravel => %q want 11.0", v)
	}
	if v, _ := ResolveVersion(dir, "django"); v != "5.0" {
		t.Errorf("django => %q want 5.0", v)
	}
	if v, _ := ResolveVersion(dir, "php"); v != "" {
		t.Errorf("php should be skipped from composer, got %q", v)
	}
	if v, _ := ResolveVersion(dir, "nonexistent"); v != "" {
		t.Errorf("unknown dep should resolve empty, got %q", v)
	}
}

func TestResolveCuratedAndDerived(t *testing.T) {
	r := Resolve("next", "15.0.0")
	if r.Origin != "curated" || r.DocBase == "" {
		t.Fatalf("next should be curated with docbase, got %+v", r)
	}
	var haveLLMS, haveHTML bool
	for _, s := range r.Sources {
		if s.Kind == "llms.txt" {
			haveLLMS = true
		}
		if s.Kind == "html" {
			haveHTML = true
		}
	}
	if !haveLLMS || !haveHTML {
		t.Errorf("expected both llms.txt and html sources, got %+v", r.Sources)
	}

	// Curated entry with explicit llms.txt (Anthropic).
	a := Resolve("anthropic", "")
	if a.Sources[0].URL != "https://docs.claude.com/llms.txt" {
		t.Errorf("anthropic first source = %q", a.Sources[0].URL)
	}

	d := Resolve("somerandomlib", "")
	if d.Origin != "derived" {
		t.Errorf("unknown lib should be derived, got %q", d.Origin)
	}
	// An npm-scoped package now derives its registry project page (HTML only).
	u := Resolve("@scope/private", "")
	if len(u.Sources) != 1 || u.Sources[0].Kind != "html" ||
		u.Sources[0].URL != "https://www.npmjs.com/package/@scope/private" {
		t.Errorf("scoped name should derive the npm package page, got %+v", u.Sources)
	}
}

func TestParseLLMSIndex(t *testing.T) {
	content := `# MyLib
> Fast widgets for everyone.

## Guides
- [Getting Started](https://x.dev/start.md): how to begin
- [Auth](https://x.dev/auth.md): authentication and sessions

## API
- [Client](https://x.dev/api/client.md)
`
	idx := ParseLLMSIndex(content)
	if idx.Title != "MyLib" {
		t.Errorf("title=%q", idx.Title)
	}
	if !strings.Contains(idx.Summary, "Fast widgets") {
		t.Errorf("summary=%q", idx.Summary)
	}
	if len(idx.Links) != 3 {
		t.Fatalf("links=%d want 3: %+v", len(idx.Links), idx.Links)
	}
	if idx.Links[1].Section != "Guides" || idx.Links[1].Title != "Auth" || idx.Links[1].Desc != "authentication and sessions" {
		t.Errorf("link[1]=%+v", idx.Links[1])
	}

	sel := SelectLinks(idx, "authentication session", 2)
	if len(sel) == 0 || sel[0].Title != "Auth" {
		t.Errorf("topic select should rank Auth first, got %+v", sel)
	}
}

func TestChunkAndRankMarkdown(t *testing.T) {
	md := `# Title
intro text

## Installation
run the installer

## Authentication
use tokens for auth, sessions and login flows

## Misc
unrelated content`
	chunks := ChunkMarkdown(md)
	if len(chunks) < 4 {
		t.Fatalf("expected >=4 chunks, got %d", len(chunks))
	}
	ranked := RankChunks(chunks, "authentication tokens", 10000)
	if len(ranked) == 0 || !strings.Contains(strings.ToLower(ranked[0].Heading), "authentication") {
		t.Errorf("auth chunk should rank first, got %+v", ranked)
	}
	// Zero-match chunks should be filtered out when a topic matches.
	for _, c := range ranked {
		if strings.Contains(c.Text, "unrelated content") {
			t.Errorf("unrelated chunk should be filtered: %+v", c)
		}
	}
}

func TestChunkMarkdownIgnoresCodeFenceHeadings(t *testing.T) {
	md := "# Real Heading\nintro\n\n```python\n# this is a comment not a heading\nprint(1)\n## also not a heading\n```\n\n## Second Real\nbody"
	chunks := ChunkMarkdown(md)
	headings := []string{}
	for _, c := range chunks {
		if c.Heading != "" {
			headings = append(headings, c.Heading)
		}
	}
	if len(headings) != 2 {
		t.Fatalf("expected 2 real headings, got %d: %v", len(headings), headings)
	}
	if headings[0] != "Real Heading" || headings[1] != "Second Real" {
		t.Errorf("wrong headings: %v", headings)
	}
}

func TestChunkMarkdownSplitsOversized(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Big\n")
	for i := 0; i < 60; i++ {
		b.WriteString(strings.Repeat("word ", 40) + "\n\n")
	}
	chunks := ChunkMarkdown(b.String())
	if len(chunks) < 2 {
		t.Fatalf("oversized section should split, got %d chunks", len(chunks))
	}
	for _, c := range chunks {
		if c.Tokens > maxChunkTokens+200 {
			t.Errorf("chunk too large: %d tokens", c.Tokens)
		}
	}
}

func TestRankChunksBudget(t *testing.T) {
	var chunks []Chunk
	for i := 0; i < 20; i++ {
		chunks = append(chunks, Chunk{Heading: "H", Text: strings.Repeat("word ", 100), Tokens: 125})
	}
	out := RankChunks(chunks, "", 300)
	total := 0
	for _, c := range out {
		total += c.Tokens
	}
	if total > 400 { // 300 budget + one overflow chunk allowed
		t.Errorf("budget exceeded: %d tokens across %d chunks", total, len(out))
	}
}

func TestHTMLToText(t *testing.T) {
	html := `<html><head><style>.x{}</style><script>bad()</script></head>
<body><nav>menu</nav><h1>Hello</h1><p>World &amp; more</p><footer>foot</footer></body></html>`
	txt := HTMLToText(html)
	if strings.Contains(txt, "bad()") || strings.Contains(txt, "menu") || strings.Contains(txt, "foot") {
		t.Errorf("script/nav/footer not stripped: %q", txt)
	}
	if !strings.Contains(txt, "Hello") || !strings.Contains(txt, "World & more") {
		t.Errorf("content/entities wrong: %q", txt)
	}
}

func TestEngineLookupLLMSFull(t *testing.T) {
	ff := &fakeFetcher{bodies: map[string]FetchResult{
		"https://docs.claude.com/llms-full.txt": {StatusCode: 200, Body: "# Claude\n\n## Messages API\nsend messages with tools\n\n## Embeddings\nvectors here\n"},
	}}
	eng := &Engine{Fetcher: ff}
	res, err := eng.Lookup(context.Background(), LookupOptions{
		Library: "anthropic", Topic: "messages tools", Network: true, MaxTokens: 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SourceKind != "llms-full.txt" {
		t.Fatalf("expected llms-full source, got %q (attempts %+v)", res.SourceKind, res.Attempts)
	}
	if len(res.Chunks) == 0 || !strings.Contains(strings.ToLower(res.Chunks[0].Heading), "messages") {
		t.Errorf("messages chunk should rank first: %+v", res.Chunks)
	}
}

func TestEngineFollowsLLMSIndexSameHostOnly(t *testing.T) {
	ff := &fakeFetcher{bodies: map[string]FetchResult{
		"https://nextjs.org/llms.txt":   {StatusCode: 200, Body: "# Next\n## Docs\n- [Routing](https://nextjs.org/routing.md): app router\n- [Evil](https://evil.com/x.md): bad\n"},
		"https://nextjs.org/routing.md": {StatusCode: 200, Body: "# Routing\nuse the app router for routing\n"},
	}}
	// llms-full 404s so it falls through to llms.txt.
	eng := &Engine{Fetcher: ff}
	res, err := eng.Lookup(context.Background(), LookupOptions{
		Library: "next", Topic: "routing", Network: true, FollowLinks: true, MaxTokens: 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.ToLower(res.SourceKind + " " + chunkText(res.Chunks))
	if !strings.Contains(joined, "routing") {
		t.Errorf("expected routing content, got kind=%q chunks=%+v", res.SourceKind, res.Chunks)
	}
	for _, c := range ff.calls {
		if strings.Contains(c, "evil.com") {
			t.Errorf("SSRF guard failed: fetched cross-host link %q", c)
		}
	}
}

func TestEngineOfflineReturnsSources(t *testing.T) {
	eng := &Engine{Fetcher: &fakeFetcher{}}
	res, err := eng.Lookup(context.Background(), LookupOptions{Library: "react", Network: false})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Offline || len(res.Resolved.Sources) == 0 {
		t.Errorf("offline lookup should return resolved sources, got %+v", res)
	}
	if len(res.Chunks) != 0 {
		t.Errorf("offline lookup should not have chunks")
	}
}

func TestEngineCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cache := NewCache(filepath.Join(dir, "docs-cache"), time.Hour)
	ff := &fakeFetcher{bodies: map[string]FetchResult{
		"https://react.dev/llms-full.txt": {StatusCode: 200, Body: "# React\n## Hooks\nuseState and useEffect\n"},
	}}
	eng := &Engine{Fetcher: ff, Cache: cache, Now: func() time.Time { return time.Unix(1000, 0) }}
	opts := LookupOptions{Library: "react", Topic: "hooks", Network: true}
	if _, err := eng.Lookup(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := len(ff.calls)
	res2, err := eng.Lookup(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.FromCache {
		t.Errorf("second lookup should be from cache")
	}
	if len(ff.calls) != callsAfterFirst {
		t.Errorf("cache hit should not refetch: before=%d after=%d", callsAfterFirst, len(ff.calls))
	}
}

func TestHostAllowed(t *testing.T) {
	allow := []string{"nextjs.org", "docs.claude.com"}
	if !hostAllowed("nextjs.org", allow) || !hostAllowed("www.nextjs.org", allow) {
		t.Error("should allow nextjs hosts")
	}
	if hostAllowed("evil.com", allow) {
		t.Error("should reject evil.com")
	}
	if !hostAllowed("anything.com", nil) {
		t.Error("empty allowlist should allow any host")
	}
}

func chunkText(cs []Chunk) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.Heading + " " + c.Text + " ")
	}
	return b.String()
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
