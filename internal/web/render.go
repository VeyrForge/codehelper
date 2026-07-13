package web

import (
	"context"
	"strings"
)

// Renderer renders a URL with a real (headless) browser and returns the
// post-JavaScript HTML. It is an optional escalation tier: the default web tool
// is HTTP-only (fast, no browser), and only pages that genuinely need client-side
// rendering should fall through to a Renderer.
//
// The production implementation is a pooled headless-Chrome driver (go-rod) kept
// in a separate, build-tagged file so the default binary stays dependency-free.
// Wire one in by setting DefaultRenderer at startup.
type Renderer interface {
	Render(ctx context.Context, url string) (html string, err error)
}

// DefaultRenderer, when non-nil, is used to re-fetch pages detected as
// JS-rendered. Nil (the default) means the tool reports needs_js_render instead
// of silently returning an empty SPA shell.
var DefaultRenderer Renderer

// looksJSRendered heuristically detects a client-rendered page: HTML that ships
// a script-driven app shell (an empty root container + bundled scripts) but
// almost no readable text. Returning the shell to an LLM is worse than useless —
// it looks like content but isn't — so the tool flags it explicitly.
func looksJSRendered(html, extractedText string) bool {
	if strings.TrimSpace(html) == "" {
		return false
	}
	lower := strings.ToLower(html)
	hasScript := strings.Count(lower, "<script") >= 1
	if !hasScript {
		return false
	}
	// Common SPA mount points.
	appShell := strings.Contains(lower, `id="root"`) ||
		strings.Contains(lower, `id="app"`) ||
		strings.Contains(lower, `id="__next"`) ||
		strings.Contains(lower, `data-reactroot`) ||
		strings.Contains(lower, `ng-app`)
	// Very little visible text relative to markup ⇒ the body is JS-populated.
	thinText := len(strings.Fields(extractedText)) < 40
	return appShell && thinText
}
