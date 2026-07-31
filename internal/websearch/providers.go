package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ---- Tavily (https://tavily.com) — clean LLM-oriented JSON, free 1000/mo ----

func searchTavily(ctx context.Context, key, query string, count int) (*Response, error) {
	body, _ := json.Marshal(map[string]any{
		"api_key":        key,
		"query":          query,
		"max_results":    count,
		"search_depth":   "basic",
		"include_answer": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	data, err := doRequest(req, Tavily)
	if err != nil {
		return nil, err
	}
	return parseTavily(data, query)
}

func parseTavily(data []byte, query string) (*Response, error) {
	var raw struct {
		Answer  string `json:"answer"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("tavily: bad response: %w", err)
	}
	resp := &Response{Provider: Tavily, Query: query, Answer: strings.TrimSpace(raw.Answer)}
	for _, r := range raw.Results {
		resp.Results = append(resp.Results, Result{Title: r.Title, URL: r.URL, Snippet: capSnippet(r.Content)})
	}
	return resp, nil
}

// ---- Brave (https://brave.com/search/api) — independent index, free 2000/mo ----

func searchBrave(ctx context.Context, key, query string, count int) (*Response, error) {
	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d", url.QueryEscape(query), count)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)
	data, err := doRequest(req, Brave)
	if err != nil {
		return nil, err
	}
	return parseBrave(data, query)
}

func parseBrave(data []byte, query string) (*Response, error) {
	var raw struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("brave: bad response: %w", err)
	}
	resp := &Response{Provider: Brave, Query: query}
	for _, r := range raw.Web.Results {
		resp.Results = append(resp.Results, Result{Title: stripTags(r.Title), URL: r.URL, Snippet: capSnippet(stripTags(r.Description))})
	}
	return resp, nil
}

// ---- DuckDuckGo — keyless, best-effort HTML scrape (the zero-config fallback) ----

func searchDuckDuckGo(ctx context.Context, query string, count int) (*Response, error) {
	u := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// A UA avoids the bot-block page; this endpoint has no API and may change,
	// which is why it's only the fallback.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) codehelper-search/1.0")
	data, err := doRequest(req, DuckDuckGo)
	if err != nil {
		return nil, err
	}
	resp := &Response{Provider: DuckDuckGo, Query: query, Results: parseDuckDuckGo(data, count)}
	if len(resp.Results) == 0 {
		return resp, fmt.Errorf("duckduckgo returned no parseable results (it may be rate-limiting); add a free Tavily/Brave key with `ch config search set`")
	}
	return resp, nil
}

var ddgResultRe = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>.*?<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)

func parseDuckDuckGo(html []byte, count int) []Result {
	var out []Result
	for _, m := range ddgResultRe.FindAllSubmatch(html, -1) {
		out = append(out, Result{
			Title:   stripTags(string(m[2])),
			URL:     decodeDDGURL(string(m[1])),
			Snippet: capSnippet(stripTags(string(m[3]))),
		})
		if len(out) >= count {
			break
		}
	}
	return out
}

// decodeDDGURL unwraps DuckDuckGo's /l/?uddg=<encoded> redirect to the real URL.
func decodeDDGURL(raw string) string {
	raw = htmlUnescape(raw)
	if i := strings.Index(raw, "uddg="); i >= 0 {
		enc := raw[i+len("uddg="):]
		if amp := strings.IndexByte(enc, '&'); amp >= 0 {
			enc = enc[:amp]
		}
		if dec, err := url.QueryUnescape(enc); err == nil {
			return dec
		}
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return raw
}

// ---- shared helpers ----

func doRequest(req *http.Request, provider string) ([]byte, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", provider, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%s: %d — check the API key (`ch config search show`)", provider, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s: http %d", provider, resp.StatusCode)
	}
	return data, nil
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string {
	return htmlUnescape(strings.TrimSpace(tagRe.ReplaceAllString(s, "")))
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#x27;", "'", "&#39;", "'")
	return r.Replace(s)
}
