//go:build rod

package web

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/VeyrForge/codehelper/internal/paths"
)

// In-process cookie jars keyed by Session name, backed by disk so CLI
// invocations and MCP restarts can reuse WordPress (etc.) logins.
var browserSessions sync.Map // string -> []*proto.NetworkCookie

type storedCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Secure   bool    `json:"secure"`
	HTTPOnly bool    `json:"httpOnly"`
	SameSite string  `json:"sameSite,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
}

// ClearBrowserSession drops a named cookie jar (or all jars when name is empty).
func ClearBrowserSession(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		browserSessions.Range(func(k, _ any) bool {
			browserSessions.Delete(k)
			_ = os.Remove(sessionFile(k.(string)))
			return true
		})
		return
	}
	browserSessions.Delete(name)
	_ = os.Remove(sessionFile(name))
}

// SessionHasCookies reports whether a named jar has at least one cookie (memory or disk).
func SessionHasCookies(name string) bool {
	return len(loadSessionCookies(name)) > 0
}

func sessionDir() string {
	base, err := paths.RegistryDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "browser", "sessions")
}

func sessionFile(name string) string {
	dir := sessionDir()
	if dir == "" || name == "" {
		return ""
	}
	// Sanitize name to a single path segment.
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
	return filepath.Join(dir, safe+".json")
}

func loadSessionCookies(session string) []*proto.NetworkCookie {
	session = strings.TrimSpace(session)
	if session == "" {
		return nil
	}
	if v, ok := browserSessions.Load(session); ok {
		if cookies, _ := v.([]*proto.NetworkCookie); len(cookies) > 0 {
			return cookies
		}
	}
	path := sessionFile(session)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	var stored []storedCookie
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil
	}
	out := make([]*proto.NetworkCookie, 0, len(stored))
	for _, s := range stored {
		c := &proto.NetworkCookie{
			Name:     s.Name,
			Value:    s.Value,
			Domain:   s.Domain,
			Path:     s.Path,
			Secure:   s.Secure,
			HTTPOnly: s.HTTPOnly,
			Expires:  proto.TimeSinceEpoch(s.Expires),
		}
		if s.SameSite != "" {
			c.SameSite = proto.NetworkCookieSameSite(s.SameSite)
		}
		out = append(out, c)
	}
	if len(out) > 0 {
		browserSessions.Store(session, out)
	}
	return out
}

func persistSessionCookies(session string, cookies []*proto.NetworkCookie) {
	session = strings.TrimSpace(session)
	if session == "" || len(cookies) == 0 {
		return
	}
	path := sessionFile(session)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	stored := make([]storedCookie, 0, len(cookies))
	for _, c := range cookies {
		if c == nil {
			continue
		}
		stored = append(stored, storedCookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: string(c.SameSite),
			Expires:  float64(c.Expires),
		})
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

func applySessionCookies(page *rod.Page, session string) error {
	cookies := loadSessionCookies(session)
	if len(cookies) == 0 {
		return nil
	}
	params := make([]*proto.NetworkCookieParam, 0, len(cookies))
	for _, c := range cookies {
		if c == nil {
			continue
		}
		params = append(params, &proto.NetworkCookieParam{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: c.SameSite,
			Expires:  c.Expires,
			URL:      cookieURL(c),
		})
	}
	return page.SetCookies(params)
}

func saveSessionCookies(page *rod.Page, session string) {
	session = strings.TrimSpace(session)
	if session == "" {
		return
	}
	cookies, err := page.Cookies([]string{})
	if err != nil || len(cookies) == 0 {
		return
	}
	cp := make([]*proto.NetworkCookie, len(cookies))
	copy(cp, cookies)
	browserSessions.Store(session, cp)
	persistSessionCookies(session, cp)
}

func cookieURL(c *proto.NetworkCookie) string {
	if c == nil {
		return ""
	}
	scheme := "http"
	if c.Secure {
		scheme = "https"
	}
	host := strings.TrimPrefix(c.Domain, ".")
	path := c.Path
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path
}

func resolveNavigateURL(page *rod.Page, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", navigateError("navigate requires text=url")
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return target, nil
	}
	info, err := page.Info()
	if err != nil {
		return "", err
	}
	base, err := url.Parse(info.URL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

type navigateError string

func (e navigateError) Error() string { return string(e) }
