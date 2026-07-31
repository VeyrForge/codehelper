package docs

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/netguard"
)

// FetchResult is the outcome of fetching a single URL.
type FetchResult struct {
	URL        string
	StatusCode int
	Body       string
	Err        error
}

// Fetcher retrieves a URL's body. It is injectable so the engine and tests run
// fully offline; the production implementation is HTTPFetcher.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) FetchResult
}

// HTTPFetcher is the network-backed Fetcher. It enforces HTTPS, an allowlist of
// host suffixes, a body-size cap, and a timeout.
type HTTPFetcher struct {
	Client     *http.Client
	AllowHosts []string // host suffixes; empty means allow any HTTPS host
	MaxBytes   int64
	UserAgent  string
}

// NewHTTPFetcher builds an HTTPFetcher with sane defaults.
func NewHTTPFetcher(timeout time.Duration, allowHosts []string) *HTTPFetcher {
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	return &HTTPFetcher{
		// Documentation is always a public, external resource: deny loopback and
		// private ranges so a curated/redirected URL can never reach an internal
		// service or the cloud metadata endpoint.
		Client:     netguard.Client(timeout, netguard.Policy{}, false),
		AllowHosts: allowHosts,
		MaxBytes:   2 << 20, // 2 MiB (llms-full.txt can be large)
		UserAgent:  "codehelper-docs/1.0",
	}
}

// Fetch implements Fetcher.
func (f *HTTPFetcher) Fetch(ctx context.Context, rawURL string) FetchResult {
	res := FetchResult{URL: rawURL}
	u, err := url.Parse(rawURL)
	if err != nil {
		res.Err = err
		return res
	}
	if u.Scheme != "https" {
		res.Err = errHTTPSOnly
		return res
	}
	if !hostAllowed(u.Host, f.AllowHosts) {
		res.Err = errHostNotAllowed
		return res
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		res.Err = err
		return res
	}
	req.Header.Set("User-Agent", f.UserAgent)
	req.Header.Set("Accept", "text/markdown, text/plain, text/html;q=0.8")
	resp, err := f.Client.Do(req)
	if err != nil {
		res.Err = err
		return res
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode
	max := f.MaxBytes
	if max <= 0 {
		max = 2 << 20
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, max))
	res.Body = string(body)
	return res
}

// hostAllowed reports whether host matches one of the allowed suffixes. An
// empty allowlist permits any host (the fetcher still enforces HTTPS).
func hostAllowed(host string, allow []string) bool {
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if len(allow) == 0 {
		return host != ""
	}
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if host == a || strings.HasSuffix(host, "."+a) || strings.Contains(host, a) {
			return true
		}
	}
	return false
}

type fetchErr string

func (e fetchErr) Error() string { return string(e) }

const (
	errHTTPSOnly      = fetchErr("only https URLs are allowed")
	errHostNotAllowed = fetchErr("host is not in the docs allowlist")
)
