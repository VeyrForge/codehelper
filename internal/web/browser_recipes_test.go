package web

import (
	"strings"
	"testing"
)

func TestExpandRecipeWPLogin(t *testing.T) {
	acts, err := ExpandRecipe(RecipeWPLogin, "admin", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) < 4 {
		t.Fatalf("want login steps, got %d", len(acts))
	}
	var sawPass, sawBar bool
	for _, a := range acts {
		if a.Selector == "#user_pass" {
			if a.Text != "s3cret" || !a.Sensitive {
				t.Fatalf("password fill must be Sensitive with plaintext for runtime only: %+v", a)
			}
			sawPass = true
		}
		if a.Selector == "#wpadminbar" && (a.Do == "wait" || a.Do == "assert") {
			sawBar = true
		}
	}
	if !sawPass || !sawBar {
		t.Fatalf("missing password fill or wpadminbar wait/assert: %+v", acts)
	}
	for _, a := range acts {
		label := actionLabel(a)
		if a.Sensitive && strings.Contains(label, "s3cret") {
			t.Fatalf("action label leaked secret: %q", label)
		}
	}
}

func TestExpandRecipeSkipLogin(t *testing.T) {
	acts, err := ExpandRecipeOptions(RecipeWPPlugins, "admin", "s3cret", "http://wp-test.local", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Do == "fill" || a.Selector == "#user_login" {
			t.Fatalf("skipLogin should omit login fills, got %#v", acts)
		}
	}
	if len(acts) == 0 || acts[0].Do != "navigate" {
		t.Fatalf("want navigate-first plugins actions, got %#v", acts)
	}
	warm, err := ExpandRecipeOptions(RecipeWPLogin, "admin", "s3cret", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(warm) != 2 || warm[0].Selector != "#wpadminbar" {
		t.Fatalf("warm login assert: %#v", warm)
	}
}

func TestExpandRecipeWPPluginsAndPosts(t *testing.T) {
	for _, name := range []string{RecipeWPPlugins, RecipeWPPosts, RecipeWPNewPost} {
		acts, err := ExpandRecipeWithBase(name, "admin", "s3cret", "http://wp-test.local")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var sawNav, sawAssertText bool
		for _, a := range acts {
			if a.Do == "navigate" {
				sawNav = true
				if !strings.Contains(a.Text, "wp-admin") {
					t.Errorf("%s navigate target missing wp-admin: %q", name, a.Text)
				}
			}
			if a.Do == "assert_text" || a.Do == "wait_nav" {
				sawAssertText = true
			}
			if a.Sensitive && strings.Contains(actionLabel(a), "s3cret") {
				t.Fatalf("%s leaked secret in label", name)
			}
		}
		if !sawNav || !sawAssertText {
			t.Fatalf("%s missing navigate/assert: %+v", name, acts)
		}
	}
}

func TestExpandRecipeUnknown(t *testing.T) {
	_, err := ExpandRecipe("nope", "", "")
	if err == nil {
		t.Fatal("expected error for unknown recipe")
	}
}

func TestExpandRecipeMultiCMS(t *testing.T) {
	cases := []struct {
		name     string
		needAuth bool
		wantSel  string
	}{
		{RecipeLaravelLogin, true, "input[name=\"email\"]"},
		{RecipeDjangoAdmin, true, "#id_username"},
		{RecipeDrupalLogin, true, "#edit-name"},
		{RecipeMagentoLogin, true, "#username"},
		{RecipeSPAHydrate, false, "body"},
	}
	for _, tc := range cases {
		acts, err := ExpandRecipe(tc.name, "u", "p")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(acts) == 0 {
			t.Fatalf("%s: empty actions", tc.name)
		}
		if RecipeNeedsAuth(tc.name) != tc.needAuth {
			t.Fatalf("%s RecipeNeedsAuth=%v want %v", tc.name, RecipeNeedsAuth(tc.name), tc.needAuth)
		}
		var saw bool
		for _, a := range acts {
			if a.Selector == tc.wantSel || (tc.name == RecipeSPAHydrate && a.Do == "wait_hydrate") {
				saw = true
			}
			if a.Sensitive && strings.Contains(actionLabel(a), "p") && a.Text == "p" {
				if strings.Contains(actionLabel(a), "\"p\"") {
					t.Fatalf("%s leaked password in label: %q", tc.name, actionLabel(a))
				}
			}
		}
		if !saw {
			t.Fatalf("%s missing expected selector/step %q in %#v", tc.name, tc.wantSel, acts)
		}
	}
}

func TestDefaultRecipeForKind(t *testing.T) {
	if DefaultRecipeForKind("laravel") != RecipeLaravelLogin {
		t.Fatal(DefaultRecipeForKind("laravel"))
	}
	if DefaultSiteKind("wordpress_plugin") != "wordpress" {
		t.Fatal(DefaultSiteKind("wordpress_plugin"))
	}
}

func TestActionLabelNavigateWaitNavAssertText(t *testing.T) {
	if got := actionLabel(Action{Do: "navigate", Text: "/wp-admin/plugins.php"}); !strings.Contains(got, "plugins.php") {
		t.Fatalf("navigate label: %q", got)
	}
	if got := actionLabel(Action{Do: "wait_nav", Text: "edit.php"}); !strings.Contains(got, "edit.php") {
		t.Fatalf("wait_nav label: %q", got)
	}
	if got := actionLabel(Action{Do: "assert_text", Selector: "h1", Text: "Plugins"}); !strings.Contains(got, "Plugins") {
		t.Fatalf("assert_text label: %q", got)
	}
}
