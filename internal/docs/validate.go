package docs

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/netguard"
)

// LinkStatus is the outcome of validating one URL.
type LinkStatus struct {
	URL   string `json:"url"`
	Code  int    `json:"code,omitempty"`
	OK    bool   `json:"ok"`
	Dead  bool   `json:"dead,omitempty"`  // resolved to a 4xx/5xx — almost certainly broken
	Stale bool   `json:"stale,omitempty"` // transient/unknown — kept but flagged
}

// LinkValidator checks whether a URL actually resolves. It is injectable so the
// engine and tests run fully offline.
type LinkValidator interface {
	Validate(ctx context.Context, url string) LinkStatus
}

// HTTPValidator validates links over the network with an SSRF-guarded client.
// It tries HEAD first (cheap) and falls back to GET when a server rejects HEAD
// (405/501), per RFC 9110 — HEAD support is only a SHOULD. Results are cached
// with a TTL: successes and hard 4xx are cached, transient 5xx are not (a flake
// must not become a permanent "broken" verdict).
type HTTPValidator struct {
	Client *http.Client
	TTL    time.Duration

	mu    sync.Mutex
	cache map[string]cachedStatus
}

type cachedStatus struct {
	st LinkStatus
	at time.Time
}

// NewHTTPValidator builds a validator with an external-only SSRF policy.
func NewHTTPValidator(timeout, ttl time.Duration) *HTTPValidator {
	if timeout <= 0 {
		timeout = 6 * time.Second
	}
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	return &HTTPValidator{
		Client: netguard.Client(timeout, netguard.Policy{}, false),
		TTL:    ttl,
		cache:  map[string]cachedStatus{},
	}
}

// now is overridable in tests; defaults to time.Now.
func (v *HTTPValidator) now() time.Time { return time.Now() }

func (v *HTTPValidator) Validate(ctx context.Context, url string) LinkStatus {
	if v.cache != nil {
		v.mu.Lock()
		if c, ok := v.cache[url]; ok && v.now().Sub(c.at) < v.TTL {
			v.mu.Unlock()
			return c.st
		}
		v.mu.Unlock()
	}
	st := v.probe(ctx, url)
	// Cache definitive verdicts only — never a transient 5xx/timeout.
	if v.cache != nil && (st.OK || st.Dead) {
		v.mu.Lock()
		v.cache[url] = cachedStatus{st: st, at: v.now()}
		v.mu.Unlock()
	}
	return st
}

func (v *HTTPValidator) probe(ctx context.Context, url string) LinkStatus {
	code, err := v.request(ctx, http.MethodHead, url)
	// Some servers reject HEAD; fall back to GET before trusting the verdict.
	if err != nil || code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented {
		if c2, err2 := v.request(ctx, http.MethodGet, url); err2 == nil {
			code, err = c2, nil
		} else if err != nil {
			return LinkStatus{URL: url, Stale: true} // network/transient — keep but flag
		}
	}
	switch {
	case code >= 200 && code < 400, code == http.StatusTooManyRequests:
		return LinkStatus{URL: url, Code: code, OK: true}
	case code >= 500:
		return LinkStatus{URL: url, Code: code, Stale: true} // server-side flake
	default:
		return LinkStatus{URL: url, Code: code, Dead: true} // 4xx — broken
	}
}

func (v *HTTPValidator) request(ctx context.Context, method, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "codehelper-docs/1.0")
	resp, err := v.Client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// validateLinks checks a set of links concurrently (bounded) and returns the
// surviving links plus the per-URL statuses. Dead (4xx) links are dropped;
// stale/unknown links are kept (a transient failure should not hide real docs).
func validateLinks(ctx context.Context, v LinkValidator, links []LLMSLink) ([]LLMSLink, []LinkStatus) {
	if v == nil || len(links) == 0 {
		return links, nil
	}
	const maxConc = 8
	sem := make(chan struct{}, maxConc)
	statuses := make([]LinkStatus, len(links))
	var wg sync.WaitGroup
	for i, l := range links {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, url string) {
			defer wg.Done()
			defer func() { <-sem }()
			statuses[i] = v.Validate(ctx, url)
		}(i, l.URL)
	}
	wg.Wait()

	kept := make([]LLMSLink, 0, len(links))
	out := make([]LinkStatus, 0, len(links))
	for i, l := range links {
		out = append(out, statuses[i])
		if statuses[i].Dead {
			continue // drop broken links so the LLM never sees a 404 URL
		}
		kept = append(kept, l)
	}
	return kept, out
}
