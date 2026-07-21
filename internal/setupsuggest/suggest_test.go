package setupsuggest

import (
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/projcfg"
)

func TestBuild_WordPressRequiresSite(t *testing.T) {
	rep := Build(Input{
		ProjectType: "wordpress_site",
		Framework:   "wordpress",
		IncludeMCP:  true,
		BinaryHint:  "/usr/bin/codehelper",
	})
	if rep.Stack != "wordpress" || rep.DefaultRecipe != "wp_login" {
		t.Fatalf("stack/recipe: %+v", rep)
	}
	if !strings.Contains(rep.LocalURLHint, "127.0.0.1") {
		t.Fatalf("local url hint: %q", rep.LocalURLHint)
	}
	var sawRequired bool
	for _, s := range rep.Suggestions {
		if s.ID == "add_site_profile" && s.Priority == PriorityRequired && !s.Done {
			sawRequired = true
		}
	}
	if !sawRequired {
		t.Fatalf("expected required add_site_profile: %+v", rep.Suggestions)
	}
	if !strings.Contains(rep.MCPSnippet, "mcpServers") || !strings.Contains(rep.MCPSnippet, "/usr/bin/codehelper") {
		t.Fatalf("mcp snippet: %q", rep.MCPSnippet)
	}
	text := FormatText(rep)
	if !strings.Contains(text, "setup suggestions") || !strings.Contains(text, "add-site") {
		t.Fatalf("FormatText: %s", text)
	}
}

func TestBuild_WithConfiguredSiteMarksDone(t *testing.T) {
	var c connections.Config
	if err := c.AddWebSite(connections.WebSite{
		Name: "local-laravel", Kind: "laravel", BaseURL: "http://127.0.0.1:8000",
		User: "a@b.c", PasswordRef: "env:PASS",
	}); err != nil {
		t.Fatal(err)
	}
	headed := true
	rep := Build(Input{
		Framework:   "laravel",
		Connections: c,
		Projcfg: projcfg.Config{
			BrowserSite:   "local-laravel",
			BrowserRecipe: "laravel_login",
			BrowserHeaded: &headed,
		},
	})
	if rep.DefaultRecipe != "laravel_login" {
		t.Fatalf("recipe=%q", rep.DefaultRecipe)
	}
	for _, s := range rep.Suggestions {
		if s.ID == "add_site_profile" && !s.Done {
			t.Fatalf("site should be done: %+v", s)
		}
	}
}

func TestBuild_RemotePatternsPresent(t *testing.T) {
	rep := Build(Input{Framework: "next"})
	if rep.SiteKind != "spa" || rep.DefaultRecipe != "spa_hydrate" {
		t.Fatalf("%+v", rep)
	}
	if len(rep.RemotePatterns) == 0 || !strings.Contains(rep.RemotePatterns[0], "ssh") {
		t.Fatalf("remote patterns: %+v", rep.RemotePatterns)
	}
}
