//go:build !rod

package web

import "context"

// EnsureBrowser is a no-op stub when the headless-browser tier is not compiled
// in; it reports the rebuild instruction via ErrBrowserUnavailable.
func EnsureBrowser(_ context.Context) (string, error) {
	return "", ErrBrowserUnavailable
}

// CaptureBrowser is the unavailable-tier stub.
func CaptureBrowser(_ context.Context, _ BrowserOptions) (*BrowserResult, error) {
	return nil, ErrBrowserUnavailable
}

// BrowserAvailable reports whether this build includes the browser tier.
func BrowserAvailable() bool { return false }
