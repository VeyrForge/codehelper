//go:build rod

// Package web's headless-Chrome render tier. It is build-tagged so the default
// binary stays dependency-free and chromium-free; enable it with:
//
//	go get github.com/go-rod/rod@latest
//	go build -tags rod ./...
//
// At runtime it needs a Chrome/Chromium binary on PATH (rod can also download a
// managed one). Wire it in at startup with:
//
//	web.DefaultRenderer = web.NewRodRenderer(4) // pool of 4 reusable pages
//
// Design notes (from 2026 headless-browser research):
//   - ONE persistent browser process + a bounded page pool — launching Chrome is
//     the bottleneck, so amortize it across requests.
//   - Block images/fonts/media and cap memory; recycle pages after N uses.
//   - All fetches still flow through the SSRF policy at the OS/proxy layer; do not
//     point this tier at untrusted internal URLs.
package web

import (
	"context"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// RodRenderer renders pages with a single persistent headless browser and a
// bounded pool of reusable pages.
type RodRenderer struct {
	once    sync.Once
	browser *rod.Browser
	pool    rod.Pool[rod.Page]
	size    int
	timeout time.Duration
}

// NewRodRenderer builds a renderer with a page pool of the given size.
func NewRodRenderer(poolSize int) *RodRenderer {
	if poolSize <= 0 {
		poolSize = 4
	}
	return &RodRenderer{size: poolSize, timeout: 20 * time.Second}
}

func (r *RodRenderer) init() {
	r.once.Do(func() {
		u := launcher.New().
			Headless(true).
			Set("disable-gpu").
			Set("disable-dev-shm-usage").
			Set("blink-settings", "imagesEnabled=false").
			MustLaunch()
		r.browser = rod.New().ControlURL(u).MustConnect()
		r.pool = rod.NewPagePool(r.size)
	})
}

// Render returns the page's HTML after the network goes idle (JS executed).
func (r *RodRenderer) Render(ctx context.Context, url string) (string, error) {
	r.init()
	page, err := r.pool.Get(func() (*rod.Page, error) {
		return r.browser.Page(proto.TargetCreateTarget{})
	})
	if err != nil {
		return "", err
	}
	defer r.pool.Put(page)

	page = page.Context(ctx).Timeout(r.timeout)
	if err := page.Navigate(url); err != nil {
		return "", err
	}
	if err := page.WaitDOMStable(300*time.Millisecond, 0.2); err != nil {
		// Stability timeout is non-fatal — return whatever rendered so far.
		_ = err
	}
	return page.HTML()
}

// Close releases the browser and its pages.
func (r *RodRenderer) Close() {
	if r.browser != nil {
		r.pool.Cleanup(func(p *rod.Page) { _ = p.Close() })
		_ = r.browser.Close()
	}
}
