package web

import (
	"fmt"
	"strings"
)

// Named browser interaction recipes. Agents pass recipe=… on the browser tool
// (or CLI) instead of hand-rolling fragile selector sequences. Credentials are
// supplied by the caller after resolving connections/secrets — never hardcoded.
const (
	RecipeWPLogin   = "wp_login"    // fill wp-login form → wait for #wpadminbar
	RecipeWPAdmin   = "wp_admin"    // same login steps (navigate to admin URL separately)
	RecipeWPPlugins = "wp_plugins"  // login → Plugins list (#the-list)
	RecipeWPPosts   = "wp_posts"    // login → Posts list (edit.php)
	RecipeWPNewPost = "wp_new_post" // login → Add New post (post-new.php)

	RecipeLaravelLogin = "laravel_login" // Breeze/Fortify-style email+password → authenticated shell
	RecipeDjangoAdmin  = "django_admin"  // /admin/login/ → #user-tools
	RecipeDrupalLogin  = "drupal_login"  // /user/login → toolbar
	RecipeMagentoLogin = "magento_login" // Magento 2 admin login → admin chrome
	RecipeSPAHydrate   = "spa_hydrate"   // wait_hydrate + root landmark smoke (no auth)
)

// KnownRecipes lists recipe names for help / errors.
func KnownRecipes() []string {
	return []string{
		RecipeWPLogin, RecipeWPAdmin, RecipeWPPlugins, RecipeWPPosts, RecipeWPNewPost,
		RecipeLaravelLogin, RecipeDjangoAdmin, RecipeDrupalLogin, RecipeMagentoLogin, RecipeSPAHydrate,
	}
}

// DefaultSiteKind maps a detected stack name to a connections website kind.
func DefaultSiteKind(stack string) string {
	switch strings.ToLower(strings.TrimSpace(stack)) {
	case "wordpress", "woocommerce", "wordpress_site", "wordpress_plugin", "wordpress_theme", "wordpress_child_theme":
		return "wordpress"
	case "laravel":
		return "laravel"
	case "django":
		return "django"
	case "drupal":
		return "drupal"
	case "magento":
		return "magento"
	case "next", "nuxt", "spa", "react", "vue", "svelte", "angular", "nodejs", "node":
		return "spa"
	default:
		return "generic"
	}
}

// DefaultRecipeForKind returns the default browser recipe for a website kind.
func DefaultRecipeForKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "wordpress", "wp":
		return RecipeWPLogin
	case "laravel":
		return RecipeLaravelLogin
	case "django":
		return RecipeDjangoAdmin
	case "drupal":
		return RecipeDrupalLogin
	case "magento":
		return RecipeMagentoLogin
	case "spa", "next", "generic", "http", "web":
		return RecipeSPAHydrate
	default:
		return RecipeSPAHydrate
	}
}

// RecipeNeedsAuth reports whether expanding the recipe requires user+password.
func RecipeNeedsAuth(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", RecipeSPAHydrate, "spa", "hydrate", "spa-hydrate":
		return false
	default:
		return true
	}
}

// ExpandRecipe returns the interaction steps for a named recipe.
// user/password may be empty for recipes that don't need them (spa_hydrate).
// Password fills are marked Sensitive so action logs never echo the secret.
func ExpandRecipe(name, user, password string) ([]Action, error) {
	return ExpandRecipeWithBase(name, user, password, "")
}

// ExpandRecipeWithBase is ExpandRecipe plus optional absolute path navigations
// after login (used when baseURL is known from a connections website profile).
// Relative admin paths work even when baseURL is empty (resolved against the
// current page after login).
func ExpandRecipeWithBase(name, user, password, baseURL string) ([]Action, error) {
	return ExpandRecipeOptions(name, user, password, baseURL, false)
}

// ExpandRecipeOptions is ExpandRecipeWithBase with skipLogin for warm session jars.
// When skipLogin is true, auth recipes omit the login form and only assert
// or navigate post-login screens (cookies must already authenticate the session).
func ExpandRecipeOptions(name, user, password, baseURL string, skipLogin bool) ([]Action, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return nil, nil
	case RecipeWPLogin, RecipeWPAdmin, "wordpress_login", "wp-login":
		if skipLogin {
			return WPSessionAssertActions(), nil
		}
		return WPLoginActions(user, password), nil
	case RecipeWPPlugins, "wordpress_plugins", "wp-plugins":
		if skipLogin {
			return WPPluginsActions(baseURL), nil
		}
		return append(WPLoginActions(user, password), WPPluginsActions(baseURL)...), nil
	case RecipeWPPosts, "wordpress_posts", "wp-posts":
		if skipLogin {
			return WPPostsActions(baseURL), nil
		}
		return append(WPLoginActions(user, password), WPPostsActions(baseURL)...), nil
	case RecipeWPNewPost, "wordpress_new_post", "wp-new-post":
		if skipLogin {
			return WPNewPostActions(baseURL), nil
		}
		return append(WPLoginActions(user, password), WPNewPostActions(baseURL)...), nil
	case RecipeLaravelLogin, "laravel", "laravel-login":
		if skipLogin {
			return LaravelSessionAssertActions(), nil
		}
		return LaravelLoginActions(user, password), nil
	case RecipeDjangoAdmin, "django", "django-admin", "django_login":
		if skipLogin {
			return DjangoSessionAssertActions(), nil
		}
		return DjangoAdminLoginActions(user, password), nil
	case RecipeDrupalLogin, "drupal", "drupal-login":
		if skipLogin {
			return DrupalSessionAssertActions(), nil
		}
		return DrupalLoginActions(user, password), nil
	case RecipeMagentoLogin, "magento", "magento-login", "magento_admin":
		if skipLogin {
			return MagentoSessionAssertActions(), nil
		}
		return MagentoLoginActions(user, password), nil
	case RecipeSPAHydrate, "spa", "hydrate", "spa-hydrate":
		return SPAHydrateActions(), nil
	default:
		return nil, fmt.Errorf("unknown browser recipe %q (want: %s)", name, strings.Join(KnownRecipes(), ", "))
	}
}

// WPSessionAssertActions verifies an already-authenticated admin session.
func WPSessionAssertActions() []Action {
	return []Action{
		{Do: "wait", Selector: "#wpadminbar", MS: 15000},
		{Do: "assert", Selector: "#wpadminbar"},
	}
}

// WPLoginActions drives the standard WordPress wp-login.php form and waits for
// the admin bar — the reliable signal that an admin session is established.
// Selectors match core WP markup (#user_login, #user_pass, #wp-submit, #wpadminbar).
func WPLoginActions(user, password string) []Action {
	return []Action{
		{Do: "wait", Selector: "#user_login", MS: 15000},
		{Do: "fill", Selector: "#user_login", Text: user},
		{Do: "fill", Selector: "#user_pass", Text: password, Sensitive: true},
		{Do: "click", Selector: "#wp-submit"},
		{Do: "wait", Selector: "#wpadminbar", MS: 20000},
		{Do: "assert", Selector: "#wpadminbar"},
	}
}

// WPPluginsActions opens the Plugins admin screen after an authenticated session.
func WPPluginsActions(baseURL string) []Action {
	return []Action{
		{Do: "navigate", Text: adminPath(baseURL, "/wp-admin/plugins.php")},
		{Do: "wait_nav", Text: "plugins.php", MS: 15000},
		{Do: "wait", Selector: "#the-list", MS: 15000},
		{Do: "assert_text", Selector: "h1", Text: "Plugins"},
	}
}

// WPPostsActions opens the Posts list after an authenticated session.
func WPPostsActions(baseURL string) []Action {
	return []Action{
		{Do: "navigate", Text: adminPath(baseURL, "/wp-admin/edit.php")},
		{Do: "wait_nav", Text: "edit.php", MS: 15000},
		{Do: "wait", Selector: "#the-list", MS: 15000},
		{Do: "assert_text", Selector: "h1", Text: "Posts"},
	}
}

// WPNewPostActions opens the Add New post editor after an authenticated session.
func WPNewPostActions(baseURL string) []Action {
	return []Action{
		{Do: "navigate", Text: adminPath(baseURL, "/wp-admin/post-new.php")},
		{Do: "wait_nav", Text: "post-new.php", MS: 15000},
		{Do: "wait", Selector: "#wpadminbar", MS: 15000},
		{Do: "assert_text", Selector: "body", Text: "Add"},
	}
}

// LaravelLoginActions fills a typical Laravel Breeze/Jetstream/Fortify login form
// (email + password + submit) and waits for a post-login landmark.
func LaravelLoginActions(user, password string) []Action {
	return []Action{
		{Do: "wait", Selector: "input[name=\"email\"]", MS: 15000},
		{Do: "fill", Selector: "input[name=\"email\"]", Text: user},
		{Do: "fill", Selector: "input[name=\"password\"]", Text: password, Sensitive: true},
		{Do: "click", Selector: "button[type=\"submit\"]"},
		{Do: "wait_idle", MS: 800},
		{Do: "wait", Selector: "body", MS: 20000},
		{Do: "assert", Selector: "body"},
	}
}

// LaravelSessionAssertActions verifies a warm Laravel session (page loaded).
func LaravelSessionAssertActions() []Action {
	return []Action{
		{Do: "wait", Selector: "body", MS: 15000},
		{Do: "assert", Selector: "body"},
	}
}

// DjangoAdminLoginActions drives Django's stock admin login form.
func DjangoAdminLoginActions(user, password string) []Action {
	return []Action{
		{Do: "wait", Selector: "#id_username", MS: 15000},
		{Do: "fill", Selector: "#id_username", Text: user},
		{Do: "fill", Selector: "#id_password", Text: password, Sensitive: true},
		{Do: "click", Selector: "input[type=\"submit\"]"},
		{Do: "wait", Selector: "#user-tools", MS: 20000},
		{Do: "assert", Selector: "#user-tools"},
	}
}

// DjangoSessionAssertActions verifies an already-authenticated Django admin session.
func DjangoSessionAssertActions() []Action {
	return []Action{
		{Do: "wait", Selector: "#user-tools", MS: 15000},
		{Do: "assert", Selector: "#user-tools"},
	}
}

// DrupalLoginActions drives core Drupal user login and waits for the admin toolbar.
func DrupalLoginActions(user, password string) []Action {
	return []Action{
		{Do: "wait", Selector: "#edit-name", MS: 15000},
		{Do: "fill", Selector: "#edit-name", Text: user},
		{Do: "fill", Selector: "#edit-pass", Text: password, Sensitive: true},
		{Do: "click", Selector: "#edit-submit"},
		{Do: "wait", Selector: "#toolbar-administration", MS: 20000},
		{Do: "assert", Selector: "#toolbar-administration"},
	}
}

// DrupalSessionAssertActions verifies a warm Drupal admin toolbar session.
func DrupalSessionAssertActions() []Action {
	return []Action{
		{Do: "wait", Selector: "#toolbar-administration", MS: 15000},
		{Do: "assert", Selector: "#toolbar-administration"},
	}
}

// MagentoLoginActions drives Magento 2 admin login (#username / #login).
func MagentoLoginActions(user, password string) []Action {
	return []Action{
		{Do: "wait", Selector: "#username", MS: 15000},
		{Do: "fill", Selector: "#username", Text: user},
		{Do: "fill", Selector: "#login", Text: password, Sensitive: true},
		{Do: "click", Selector: ".action-login"},
		{Do: "wait", Selector: ".page-header", MS: 25000},
		{Do: "assert", Selector: ".page-header"},
	}
}

// MagentoSessionAssertActions verifies Magento admin chrome after a warm jar.
func MagentoSessionAssertActions() []Action {
	return []Action{
		{Do: "wait", Selector: ".page-header", MS: 15000},
		{Do: "assert", Selector: ".page-header"},
	}
}

// SPAHydrateActions is a framework-agnostic smoke: wait for client hydration /
// idle, then assert a common app root if present (falls back to body).
func SPAHydrateActions() []Action {
	return []Action{
		{Do: "wait_hydrate", MS: 15000},
		{Do: "wait_idle", MS: 500},
		{Do: "wait", Selector: "body", MS: 10000},
		{Do: "assert", Selector: "body"},
	}
}

func adminPath(baseURL, path string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return path
	}
	return baseURL + path
}

// IsWPRecipe reports whether name is a WordPress admin recipe.
func IsWPRecipe(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case RecipeWPLogin, RecipeWPAdmin, "wordpress_login", "wp-login",
		RecipeWPPlugins, "wordpress_plugins", "wp-plugins",
		RecipeWPPosts, "wordpress_posts", "wp-posts",
		RecipeWPNewPost, "wordpress_new_post", "wp-new-post":
		return true
	default:
		return false
	}
}

// IsAuthRecipe reports whether name is any recipe that performs a login form fill.
func IsAuthRecipe(name string) bool {
	return RecipeNeedsAuth(name) && strings.TrimSpace(name) != ""
}

// actionLabel is the human-readable step line for logs/previews. Sensitive
// fills redact Text so passwords from connections/secrets never appear in MCP
// responses or CLI stderr.
func actionLabel(a Action) string {
	switch strings.ToLower(a.Do) {
	case "type", "fill":
		text := a.Text
		if a.Sensitive {
			text = "***"
		}
		return fmt.Sprintf("%s %q=%q", a.Do, locatorLabel(a), text)
	case "press":
		return fmt.Sprintf("press %s", a.Key)
	case "scroll":
		if loc := locatorLabel(a); loc != "" {
			return "scroll to " + loc
		}
		return fmt.Sprintf("scroll y=%d", a.Y)
	case "wait":
		if loc := locatorLabel(a); loc != "" {
			return "wait for " + loc
		}
		return fmt.Sprintf("wait %dms", a.MS)
	case "navigate":
		return fmt.Sprintf("navigate %s", a.Text)
	case "wait_nav", "wait_navigation", "wait_url":
		if a.Text != "" {
			return fmt.Sprintf("wait_nav url contains %q", a.Text)
		}
		return "wait_nav"
	case "wait_idle", "wait_network", "network_idle":
		ms := a.MS
		if ms <= 0 {
			ms = 500
		}
		return fmt.Sprintf("wait_idle %dms", ms)
	case "wait_hydrate", "hydrate":
		if a.Selector != "" {
			return "wait_hydrate " + a.Selector
		}
		return "wait_hydrate"
	case "select", "select_option":
		return fmt.Sprintf("select %s=%q", locatorLabel(a), a.Text)
	case "snapshot", "aria_snapshot", "a11y_snapshot":
		return "snapshot"
	case "storage_set", "localstorage_set":
		key := a.Key
		if key == "" {
			key = a.Selector
		}
		return fmt.Sprintf("storage_set %s", key)
	case "storage_get", "localstorage_get":
		key := a.Key
		if key == "" {
			key = a.Selector
		}
		return fmt.Sprintf("storage_get %s", key)
	case "storage_clear", "localstorage_clear":
		return "storage_clear"
	case "clear_cookies", "cookie_clear":
		return "clear_cookies"
	case "assert", "assert_text":
		if a.Text != "" {
			sel := locatorLabel(a)
			if sel == "" {
				sel = "body"
			}
			return fmt.Sprintf("%s %q contains %q", a.Do, sel, a.Text)
		}
		return fmt.Sprintf("assert %q exists", locatorLabel(a))
	default:
		return fmt.Sprintf("%s %s", a.Do, locatorLabel(a))
	}
}

func locatorLabel(a Action) string {
	if a.TestID != "" {
		return "testid=" + a.TestID
	}
	if a.Role != "" {
		s := "role=" + a.Role
		if a.Name != "" {
			s += " name=" + a.Name
		}
		return s
	}
	if a.Name != "" {
		return "name=" + a.Name
	}
	return a.Selector
}
