//go:build rod

package web

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// findElement resolves Action locators (testid / role+name / text / CSS) with a
// bounded timeout, then self-heals via accessible-name search when practical.
// Returns the element and the locator string that worked (for logs/highlight).
func findElement(page *rod.Page, a Action) (*rod.Element, string, error) {
	d := actionElemTimeout
	if a.MS > 0 {
		d = time.Duration(a.MS) * time.Millisecond
	}
	loc := ResolveLocator(a)

	if loc.Empty() && strings.TrimSpace(a.Text) != "" {
		// click/assert by visible text when no selector was given
		loc.Text = strings.TrimSpace(a.Text)
	}
	if loc.Empty() {
		return nil, "", fmt.Errorf("no locator (set selector, role, name, testid, ref, or text:)")
	}

	primary := page.Timeout(d)
	el, used, err := locatePrimary(primary, loc)
	if err == nil && el != nil {
		return el, used, nil
	}
	primaryErr := err

	// Self-heal on a fresh timeout budget (primary may have exhausted its deadline).
	healBudget := d
	if healBudget > 3*time.Second {
		healBudget = 3 * time.Second
	}
	if healed, hsel, herr := locateByAccessible(page.Timeout(healBudget), loc, a); herr == nil && healed != nil {
		return healed, hsel + " (healed)", nil
	}
	if primaryErr != nil {
		return nil, loc.Describe(), primaryErr
	}
	return nil, loc.Describe(), fmt.Errorf("element not found: %s", loc.Describe())
}

func locatePrimary(page *rod.Page, loc Locator) (*rod.Element, string, error) {
	if loc.Ref != "" {
		return locateByOutlineRef(page, loc.Ref)
	}
	if loc.TestID != "" {
		css := loc.TestIDCSS()
		el, err := page.Element(css)
		if err == nil {
			return el, "testid=" + loc.TestID, nil
		}
		return nil, css, err
	}
	if loc.Role != "" {
		el, used, err := locateByAccessible(page, loc, Action{})
		if err == nil {
			return el, used, nil
		}
		return nil, loc.Describe(), err
	}
	if loc.Text != "" && loc.CSS == "" {
		el, err := page.ElementR("a, button, label, [role=button], [role=link], summary, option, td, th, li, span, p, h1, h2, h3, h4, h5, h6",
			escapeJSRegex(loc.Text))
		if err == nil {
			return el, "text=" + loc.Text, nil
		}
		return nil, "text=" + loc.Text, err
	}
	if loc.CSS != "" {
		el, err := page.Element(loc.CSS)
		if err == nil {
			return el, loc.CSS, nil
		}
		return nil, loc.CSS, err
	}
	if loc.Name != "" {
		el, used, err := locateByAccessible(page, loc, Action{})
		if err == nil {
			return el, used, nil
		}
		return nil, loc.Describe(), err
	}
	if loc.Text != "" {
		el, err := page.ElementR("a, button, label, [role=button], [role=link], summary, option, td, th, li, span, p, h1, h2, h3, h4, h5, h6",
			escapeJSRegex(loc.Text))
		if err == nil {
			return el, "text=" + loc.Text, nil
		}
		return nil, "text=" + loc.Text, err
	}
	return nil, loc.Describe(), fmt.Errorf("empty locator")
}

// locateByAccessible finds a visible element by ARIA/implicit role and/or
// accessible name (aria-label, labelled-by, label, text, placeholder, title).
func locateByAccessible(page *rod.Page, loc Locator, a Action) (*rod.Element, string, error) {
	role := strings.TrimSpace(loc.Role)
	name := strings.TrimSpace(loc.Name)
	if name == "" {
		name = strings.TrimSpace(a.Name)
	}
	if name == "" && loc.Text != "" && role != "" {
		name = loc.Text
	}
	if role == "" && name == "" {
		return nil, "", fmt.Errorf("no role/name for heal")
	}
	el, err := page.ElementByJS(rod.Eval(accessibleFindJS, role, name))
	if err != nil {
		return nil, "", err
	}
	used := "role=" + role
	if name != "" {
		used += " name=" + name
	}
	return el, used, nil
}

// locateByOutlineRef re-collects the interactive outline and resolves ref=eN to
// that element's CSS selector (same enumeration order as outline=true).
func locateByOutlineRef(page *rod.Page, ref string) (*rod.Element, string, error) {
	ref = NormalizeOutlineRef(ref)
	if ref == "" {
		return nil, "", fmt.Errorf("empty outline ref")
	}
	els := collectOutline(page)
	for _, e := range els {
		if NormalizeOutlineRef(e.Ref) == ref {
			if strings.TrimSpace(e.Selector) == "" {
				return nil, "ref=" + ref, fmt.Errorf("outline %s has empty selector", ref)
			}
			el, err := page.Element(e.Selector)
			if err != nil {
				return nil, "ref=" + ref + " → " + e.Selector, err
			}
			return el, "ref=" + ref + " → " + e.Selector, nil
		}
	}
	return nil, "ref=" + ref, fmt.Errorf("unknown outline ref %q (call outline=true on this page state, then use ref:eN)", ref)
}

// escapeJSRegex escapes a literal string for use as a JS RegExp source in ElementR.
func escapeJSRegex(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// accessibleFindJS returns the first visible element matching role + name.
const accessibleFindJS = `(role, name) => {
  const wantRole = (role||'').toLowerCase().trim();
  const wantName = (name||'').toLowerCase().trim();
  const esc = (v) => (window.CSS && CSS.escape) ? CSS.escape(v) : String(v).replace(/["\\]/g,'\\$&');
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' && cs.opacity !== '0';
  };
  const roleOf = (el) => {
    const explicit = (el.getAttribute('role')||'').toLowerCase();
    if (explicit) return explicit;
    const t = el.tagName.toLowerCase();
    if (t === 'a') return 'link';
    if (t === 'button') return 'button';
    if (t === 'select') return 'combobox';
    if (t === 'textarea') return 'textbox';
    if (t === 'input') {
      const it = (el.getAttribute('type')||'text').toLowerCase();
      if (['button','submit','reset','image'].indexOf(it) >= 0) return 'button';
      if (it === 'checkbox') return 'checkbox';
      if (it === 'radio') return 'radio';
      if (it === 'search') return 'searchbox';
      return 'textbox';
    }
    if (t === 'h1' || t === 'h2' || t === 'h3' || t === 'h4' || t === 'h5' || t === 'h6') return 'heading';
    if (t === 'img') return 'img';
    if (el.isContentEditable) return 'textbox';
    return '';
  };
  const nameOf = (el) => {
    const aria = el.getAttribute('aria-label'); if (aria) return aria.trim();
    const labelled = el.getAttribute('aria-labelledby');
    if (labelled) {
      const parts = labelled.split(/\s+/).map(id => {
        const n = document.getElementById(id); return n ? (n.textContent||'').trim() : '';
      }).filter(Boolean);
      if (parts.length) return parts.join(' ');
    }
    if (el.id) {
      const l = document.querySelector('label[for="'+esc(el.id)+'"]');
      if (l && l.textContent.trim()) return l.textContent.trim();
    }
    const lab = el.closest && el.closest('label');
    if (lab && lab !== el && lab.textContent.trim()) return lab.textContent.trim();
    const txt = (el.innerText || el.textContent || '').trim();
    if (txt) return txt;
    const ph = el.getAttribute('placeholder'); if (ph) return ph.trim();
    const title = el.getAttribute('title'); if (title) return title.trim();
    const alt = el.getAttribute('alt'); if (alt) return alt.trim();
    return (el.value || '').trim();
  };
  const sel = 'a[href], button, input:not([type=hidden]), select, textarea, summary, ' +
    '[role], [contenteditable=""], [contenteditable=true], h1, h2, h3, h4, h5, h6, img[alt]';
  const els = document.querySelectorAll(sel);
  for (let i = 0; i < els.length; i++) {
    const el = els[i];
    if (!visible(el)) continue;
    const r = roleOf(el);
    if (wantRole && r !== wantRole && !(wantRole === 'select' && r === 'combobox')) continue;
    if (wantName) {
      const n = nameOf(el).toLowerCase().replace(/\s+/g,' ');
      if (!n.includes(wantName)) continue;
    }
    return el;
  }
  return null;
}`

// snapshotJS builds a bounded, Playwright-MCP-style accessibility snapshot.
const snapshotJS = `() => {
  const max = 80;
  const esc = (v) => (window.CSS && CSS.escape) ? CSS.escape(v) : String(v).replace(/["\\]/g,'\\$&');
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none';
  };
  const roleOf = (el) => {
    const explicit = el.getAttribute('role'); if (explicit) return explicit;
    const t = el.tagName.toLowerCase();
    if (t === 'a') return 'link';
    if (t === 'button') return 'button';
    if (t === 'nav') return 'navigation';
    if (t === 'main') return 'main';
    if (t === 'header') return 'banner';
    if (t === 'footer') return 'contentinfo';
    if (t === 'form') return 'form';
    if (t === 'select') return 'combobox';
    if (t === 'textarea') return 'textbox';
    if (t === 'h1' || t === 'h2' || t === 'h3' || t === 'h4' || t === 'h5' || t === 'h6') return 'heading';
    if (t === 'input') {
      const it = (el.getAttribute('type')||'text').toLowerCase();
      if (['button','submit','reset'].indexOf(it) >= 0) return 'button';
      if (it === 'checkbox') return 'checkbox';
      if (it === 'radio') return 'radio';
      return 'textbox';
    }
    if (el.isContentEditable) return 'textbox';
    return '';
  };
  const nameOf = (el) => {
    const aria = el.getAttribute('aria-label'); if (aria) return aria.trim();
    if (el.id) {
      const l = document.querySelector('label[for="'+esc(el.id)+'"]');
      if (l && l.textContent.trim()) return l.textContent.trim();
    }
    const lab = el.closest && el.closest('label');
    if (lab && lab !== el && lab.textContent.trim()) return lab.textContent.trim();
    const txt = (el.innerText || el.textContent || '').trim();
    if (txt) return txt.slice(0, 80);
    const ph = el.getAttribute('placeholder'); if (ph) return ph.trim();
    return (el.getAttribute('title')||el.getAttribute('alt')||'').trim();
  };
  const testidOf = (el) => el.getAttribute('data-testid') || el.getAttribute('data-test') || '';
  const sel = 'a[href], button, input:not([type=hidden]), select, textarea, ' +
    'nav, main, header, footer, form, h1, h2, h3, h4, h5, h6, ' +
    '[role=button], [role=link], [role=textbox], [role=checkbox], [role=radio], ' +
    '[role=tab], [role=menuitem], [role=navigation], [role=main], [data-testid], [data-test]';
  const lines = [];
  const seen = {};
  const els = document.querySelectorAll(sel);
  for (let i = 0; i < els.length && lines.length < max; i++) {
    const el = els[i];
    if (!visible(el)) continue;
    const role = roleOf(el);
    if (!role) continue;
    const name = nameOf(el).replace(/\s+/g,' ').slice(0, 60);
    const tid = testidOf(el);
    let line = role;
    if (name) line += ' "' + name.replace(/"/g, '\\"') + '"';
    if (tid) line += ' [testid=' + tid + ']';
    if (el.tagName.toLowerCase().match(/^h[1-6]$/)) line += ' [level=' + el.tagName[1] + ']';
    if (seen[line]) continue;
    seen[line] = 1;
    lines.push(line);
  }
  return lines.join('\n');
}`

func collectSnapshot(page *rod.Page) string {
	obj, err := page.Eval(snapshotJS)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(obj.Value.Str())
}

func waitNetworkIdle(page *rod.Page, idleMS, timeoutMS int) error {
	if idleMS <= 0 {
		idleMS = 500
	}
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	p := page.Timeout(time.Duration(timeoutMS) * time.Millisecond)
	wait := p.WaitRequestIdle(time.Duration(idleMS)*time.Millisecond, nil, nil, nil)
	wait()
	return p.GetContext().Err()
}

func waitHydrate(page *rod.Page, selector string, ms int) error {
	timeout := 15000
	if ms > 0 {
		timeout = ms
	}
	p := page.Timeout(time.Duration(timeout) * time.Millisecond)
	_ = p.WaitIdle(time.Duration(timeout) * time.Millisecond)
	_ = p.WaitDOMStable(300*time.Millisecond, 0.2)
	_ = waitNetworkIdle(p, 400, timeout)
	if selector != "" {
		if _, err := p.Element(selector); err != nil {
			return fmt.Errorf("wait_hydrate selector %q: %w", selector, err)
		}
	}
	// Framework/CMS-agnostic ready signals (SPA roots, WP admin, Axum/static
	// landmarks). Non-fatal if absent — network idle + DOM stable already ran.
	_, _ = page.Eval(`() => {
    const sel = [
      '#root', '#__next', '#app', '#svelte', '[data-reactroot]',
      '.wp-admin', '#wpadminbar', '[data-turbo-ready]', 'body[data-ready]',
      'main', '[role=main]', '#content', '.container', 'form'
    ].join(',');
    const root = document.querySelector(sel);
    if (root && (root.children.length > 0 || root.id === 'wpadminbar' || root.tagName === 'FORM' || root.tagName === 'MAIN')) {
      return true;
    }
    return document.readyState === 'complete' && !!(document.body && document.body.children.length > 0);
  }`)
	return nil
}

func clearPageCookies(page *rod.Page) error {
	info, err := page.Info()
	if err != nil {
		return err
	}
	cookies, err := page.Cookies([]string{info.URL})
	if err != nil {
		return err
	}
	for _, c := range cookies {
		if c == nil {
			continue
		}
		_ = proto.NetworkDeleteCookies{
			Name:   c.Name,
			Domain: c.Domain,
			Path:   c.Path,
			URL:    info.URL,
		}.Call(page)
	}
	return nil
}
