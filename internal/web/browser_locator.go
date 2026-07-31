package web

import (
	"fmt"
	"strings"
)

// Locator is a resolved element target. Prefer role/name/testid/ref over brittle
// CSS (Playwright-MCP / Stagehand pattern). CSS remains the escape hatch.
type Locator struct {
	CSS    string // plain CSS selector when set
	Role   string // ARIA / implicit role
	Name   string // accessible-name substring (case-insensitive)
	TestID string // data-testid or data-test
	Text   string // visible-text match (ElementR / text: prefix)
	Ref    string // outline ref (e1, e2, …) from a prior outline on this page state
}

// ResolveLocator builds a Locator from Action fields and optional selector
// prefixes: testid:…, role:button, role:button:Submit, text:…, name:…, ref:e3,
// css:…. Action.Ref also sets Ref.
func ResolveLocator(a Action) Locator {
	loc := Locator{
		Role:   strings.TrimSpace(a.Role),
		Name:   strings.TrimSpace(a.Name),
		TestID: strings.TrimSpace(a.TestID),
		Ref:    NormalizeOutlineRef(a.Ref),
		Text:   "",
		CSS:    "",
	}
	sel := strings.TrimSpace(a.Selector)
	if sel == "" {
		return loc
	}
	lower := strings.ToLower(sel)
	switch {
	case strings.HasPrefix(lower, "testid:"):
		loc.TestID = strings.TrimSpace(sel[len("testid:"):])
	case strings.HasPrefix(lower, "data-testid:"):
		loc.TestID = strings.TrimSpace(sel[len("data-testid:"):])
	case strings.HasPrefix(lower, "role:"):
		rest := strings.TrimSpace(sel[len("role:"):])
		if i := strings.IndexByte(rest, ':'); i > 0 {
			loc.Role = strings.TrimSpace(rest[:i])
			loc.Name = strings.TrimSpace(rest[i+1:])
		} else {
			loc.Role = rest
		}
	case strings.HasPrefix(lower, "text:"):
		loc.Text = strings.TrimSpace(sel[len("text:"):])
	case strings.HasPrefix(lower, "name:"):
		loc.Name = strings.TrimSpace(sel[len("name:"):])
	case strings.HasPrefix(lower, "ref:"):
		loc.Ref = NormalizeOutlineRef(sel[len("ref:"):])
	case strings.HasPrefix(lower, "ref="):
		loc.Ref = NormalizeOutlineRef(sel[len("ref="):])
	case strings.HasPrefix(lower, "css:"):
		loc.CSS = strings.TrimSpace(sel[len("css:"):])
	default:
		loc.CSS = sel
	}
	return loc
}

// NormalizeOutlineRef coerces "e3", "E3", or "3" to the canonical "e3" form.
func NormalizeOutlineRef(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "ref:")
	s = strings.TrimPrefix(s, "ref=")
	if s == "" {
		return ""
	}
	if s[0] != 'e' {
		return "e" + s
	}
	return s
}

// Describe returns a short human label for logs.
func (l Locator) Describe() string {
	var parts []string
	if l.Ref != "" {
		parts = append(parts, "ref="+l.Ref)
	}
	if l.TestID != "" {
		parts = append(parts, "testid="+l.TestID)
	}
	if l.Role != "" {
		parts = append(parts, "role="+l.Role)
	}
	if l.Name != "" {
		parts = append(parts, "name="+l.Name)
	}
	if l.Text != "" {
		parts = append(parts, "text="+l.Text)
	}
	if l.CSS != "" {
		parts = append(parts, l.CSS)
	}
	if len(parts) == 0 {
		return "(no locator)"
	}
	return strings.Join(parts, " ")
}

// Empty reports whether no locator strategy was set.
func (l Locator) Empty() bool {
	return l.CSS == "" && l.Role == "" && l.Name == "" && l.TestID == "" && l.Text == "" && l.Ref == ""
}

// TestIDCSS returns a CSS selector for data-testid / data-test when TestID is set.
func (l Locator) TestIDCSS() string {
	if l.TestID == "" {
		return ""
	}
	esc := cssAttrEscape(l.TestID)
	return fmt.Sprintf(`[data-testid="%s"], [data-test="%s"]`, esc, esc)
}

func cssAttrEscape(v string) string {
	return strings.ReplaceAll(v, `"`, `\\"`)
}
