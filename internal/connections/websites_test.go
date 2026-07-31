package connections

import "testing"

func TestAddWebSite_WordPressRoundTrip(t *testing.T) {
	root := t.TempDir()
	var c Config
	if err := c.AddWebSite(WebSite{
		Name: "local-wp", Kind: "wp", BaseURL: "http://wp-test.local",
		User: "admin", PasswordRef: "env:WP_PASS",
	}); err != nil {
		t.Fatalf("AddWebSite: %v", err)
	}
	if c.WebSites[0].Kind != "wordpress" {
		t.Fatalf("kind not canonicalized: %q", c.WebSites[0].Kind)
	}
	login, err := c.WebSites[0].LoginURL()
	if err != nil || login != "http://wp-test.local/wp-login.php" {
		t.Fatalf("LoginURL=%q err=%v", login, err)
	}
	admin, err := c.WebSites[0].AdminURL()
	if err != nil || admin != "http://wp-test.local/wp-admin/" {
		t.Fatalf("AdminURL=%q err=%v", admin, err)
	}
	if err := Save(root, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WebSites) != 1 || got.WebSites[0].User != "admin" {
		t.Fatalf("round trip: %+v", got.WebSites)
	}
	if got.Empty() {
		t.Fatal("config with website should not be Empty")
	}
	if !got.Remove("local-wp") || !got.Empty() {
		t.Fatal("Remove website failed")
	}
}

func TestAddWebSite_RejectsInlineSecretAndBadURL(t *testing.T) {
	var c Config
	if err := c.AddWebSite(WebSite{Name: "x", BaseURL: "http://example.com", PasswordRef: "hunter2"}); err == nil {
		t.Fatal("expected inline secret rejected")
	}
	if err := c.AddWebSite(WebSite{Name: "x", BaseURL: "not-a-url"}); err == nil {
		t.Fatal("expected bad url rejected")
	}
}

func TestAddWebSite_LaravelDjangoPaths(t *testing.T) {
	var c Config
	if err := c.AddWebSite(WebSite{
		Name: "app", Kind: "laravel", BaseURL: "http://127.0.0.1:8000",
		User: "a@b.c", PasswordRef: "env:PASS",
	}); err != nil {
		t.Fatal(err)
	}
	s := c.WebSites[0]
	login, err := s.LoginURL()
	if err != nil || login != "http://127.0.0.1:8000/login" {
		t.Fatalf("laravel login=%q err=%v", login, err)
	}
	if s.DefaultRecipe() != "laravel_login" {
		t.Fatalf("recipe=%q", s.DefaultRecipe())
	}
	if err := c.AddWebSite(WebSite{
		Name: "dj", Kind: "django", BaseURL: "http://127.0.0.1:8000",
		PasswordRef: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	dj := c.FindWebSite("dj")
	admin, err := dj.AdminURL()
	if err != nil || admin != "http://127.0.0.1:8000/admin/" {
		t.Fatalf("django admin=%q err=%v", admin, err)
	}
	if err := c.AddWebSite(WebSite{Name: "bad", Kind: "rails", BaseURL: "http://127.0.0.1:3000"}); err == nil {
		t.Fatal("expected unknown kind rejected")
	}
}
