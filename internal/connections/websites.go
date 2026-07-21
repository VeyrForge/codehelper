package connections

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// WebSite is a named HTTP site profile (e.g. a local WordPress admin) used by
// browser recipes. Passwords are NEVER stored here — only env:/secret refs,
// matching DBConn. The agent sees name/kind/base_url/user/has_secret only.
type WebSite struct {
	Name string `json:"name"`
	// Kind selects default login/admin paths and recipes:
	// wordpress | laravel | django | drupal | magento | spa | generic.
	Kind string `json:"kind,omitempty"`
	// BaseURL is the site root, e.g. http://wp-test.local or http://127.0.0.1:8088.
	BaseURL string `json:"base_url"`
	// LoginPath overrides the login page path (defaults depend on Kind).
	LoginPath string `json:"login_path,omitempty"`
	// AdminPath overrides the admin entry path (defaults depend on Kind).
	AdminPath string `json:"admin_path,omitempty"`
	User      string `json:"user,omitempty"`
	// PasswordRef is env:VAR or "secret" — never an inline password.
	PasswordRef string `json:"password_ref,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
}

// UsesSecretStore reports whether the site password comes from the encrypted store.
func (s WebSite) UsesSecretStore() bool {
	return strings.EqualFold(strings.TrimSpace(s.PasswordRef), SecretRef)
}

// Enabled reports whether the site profile is active.
func (s WebSite) Enabled() bool { return !s.Disabled }

// CanonicalKind folds aliases into a stable kind string.
func (s WebSite) CanonicalKind() string {
	k := strings.ToLower(strings.TrimSpace(s.Kind))
	switch k {
	case "", "wp", "wordpress":
		return "wordpress"
	case "laravel":
		return "laravel"
	case "django":
		return "django"
	case "drupal":
		return "drupal"
	case "magento", "magento2":
		return "magento"
	case "spa", "next", "nuxt":
		return "spa"
	case "generic", "http", "web":
		return "generic"
	default:
		return k
	}
}

// DefaultRecipe returns the browser recipe name for this site's kind.
// Kept in sync with web.DefaultRecipeForKind (connections must not import web).
func (s WebSite) DefaultRecipe() string {
	switch s.CanonicalKind() {
	case "wordpress":
		return "wp_login"
	case "laravel":
		return "laravel_login"
	case "django":
		return "django_admin"
	case "drupal":
		return "drupal_login"
	case "magento":
		return "magento_login"
	default:
		return "spa_hydrate"
	}
}

// defaultLoginPath returns the stock login path for a kind.
func defaultLoginPath(kind string) string {
	switch kind {
	case "wordpress":
		return "/wp-login.php"
	case "laravel":
		return "/login"
	case "django":
		return "/admin/login/"
	case "drupal":
		return "/user/login"
	case "magento":
		return "/admin"
	default:
		return "/"
	}
}

// defaultAdminPath returns the stock post-login / admin path for a kind.
func defaultAdminPath(kind string) string {
	switch kind {
	case "wordpress":
		return "/wp-admin/"
	case "laravel":
		return "/dashboard"
	case "django":
		return "/admin/"
	case "drupal":
		return "/admin/content"
	case "magento":
		return "/admin"
	default:
		return "/"
	}
}

// LoginURL returns the absolute login page URL for this site.
func (s WebSite) LoginURL() (string, error) {
	base := strings.TrimSpace(s.BaseURL)
	if base == "" {
		return "", fmt.Errorf("site %q has empty base_url", s.Name)
	}
	path := strings.TrimSpace(s.LoginPath)
	if path == "" {
		path = defaultLoginPath(s.CanonicalKind())
	}
	return joinURL(base, path)
}

// AdminURL returns the absolute admin (or configured admin) URL.
func (s WebSite) AdminURL() (string, error) {
	base := strings.TrimSpace(s.BaseURL)
	if base == "" {
		return "", fmt.Errorf("site %q has empty base_url", s.Name)
	}
	path := strings.TrimSpace(s.AdminPath)
	if path == "" {
		path = defaultAdminPath(s.CanonicalKind())
	}
	return joinURL(base, path)
}

// PathURL joins an absolute path (e.g. /wp-admin/plugins.php) onto BaseURL.
func (s WebSite) PathURL(path string) (string, error) {
	base := strings.TrimSpace(s.BaseURL)
	if base == "" {
		return "", fmt.Errorf("site %q has empty base_url", s.Name)
	}
	return joinURL(base, path)
}

func joinURL(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid base_url %q", base)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	return u.ResolveReference(ref).String(), nil
}

// FindWebSite returns the site profile by name (case-insensitive), or nil.
func (c *Config) FindWebSite(name string) *WebSite {
	name = strings.TrimSpace(name)
	for i := range c.WebSites {
		if strings.EqualFold(c.WebSites[i].Name, name) {
			return &c.WebSites[i]
		}
	}
	return nil
}

// SupportedSiteKinds lists allowed website kind values for CLI help / validation.
func SupportedSiteKinds() []string {
	return []string{"wordpress", "laravel", "django", "drupal", "magento", "spa", "generic"}
}

// AddWebSite validates and upserts a site profile by name (case-insensitive).
func (c *Config) AddWebSite(s WebSite) error {
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" {
		return fmt.Errorf("site name is required")
	}
	s.BaseURL = strings.TrimSpace(s.BaseURL)
	if s.BaseURL == "" {
		return fmt.Errorf("site %q needs a base_url", s.Name)
	}
	if _, err := url.ParseRequestURI(s.BaseURL); err != nil {
		return fmt.Errorf("site %q base_url is invalid: %w", s.Name, err)
	}
	u, err := url.Parse(s.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("site %q base_url must include scheme and host (e.g. http://127.0.0.1:8088)", s.Name)
	}
	kind := s.CanonicalKind()
	allowed := false
	for _, k := range SupportedSiteKinds() {
		if k == kind {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("site kind must be one of %s (got %q)", strings.Join(SupportedSiteKinds(), "|"), s.Kind)
	}
	s.Kind = kind
	if err := validatePasswordRef(s.PasswordRef); err != nil {
		return err
	}
	out := c.WebSites[:0]
	for _, x := range c.WebSites {
		if !strings.EqualFold(x.Name, s.Name) {
			out = append(out, x)
		}
	}
	c.WebSites = append(out, s)
	sortWebSites(c)
	return nil
}

func sortWebSites(c *Config) {
	sort.Slice(c.WebSites, func(i, j int) bool {
		return c.WebSites[i].Name < c.WebSites[j].Name
	})
}
