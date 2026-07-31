package docs

import (
	"regexp"
	"sort"
	"strings"
)

// Chunk is a documentation section ranked for relevance to a topic.
type Chunk struct {
	Heading string  `json:"heading,omitempty"`
	Text    string  `json:"text"`
	Score   float64 `json:"score,omitempty"`
	Tokens  int     `json:"tokens"`
}

// LLMSIndex is the parsed structure of an llms.txt index file.
type LLMSIndex struct {
	Title   string     `json:"title,omitempty"`
	Summary string     `json:"summary,omitempty"`
	Links   []LLMSLink `json:"links,omitempty"`
}

// LLMSLink is a single entry in an llms.txt section list.
type LLMSLink struct {
	Section string `json:"section,omitempty"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Desc    string `json:"desc,omitempty"`
}

var (
	mdLink   = regexp.MustCompile(`^\s*[-*]\s*\[([^\]]+)\]\(([^)]+)\)\s*:?\s*(.*)$`)
	htmlTag  = regexp.MustCompile(`(?s)<[^>]+>`)
	wsRun    = regexp.MustCompile(`[ \t]+`)
	blankRun = regexp.MustCompile(`\n{3,}`)
	// Go's RE2 has no backreferences, so strip each non-content element with a
	// dedicated pattern.
	htmlStripTags = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`),
		regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`),
		regexp.MustCompile(`(?is)<nav[^>]*>.*?</nav>`),
		regexp.MustCompile(`(?is)<footer[^>]*>.*?</footer>`),
		regexp.MustCompile(`(?is)<header[^>]*>.*?</header>`),
		regexp.MustCompile(`(?is)<svg[^>]*>.*?</svg>`),
	}
)

// ParseLLMSIndex parses an llms.txt index file (H1 title, optional blockquote
// summary, then "## Section" headers with markdown link lists). Pure.
func ParseLLMSIndex(content string) LLMSIndex {
	var idx LLMSIndex
	section := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "# ") && idx.Title == "":
			idx.Title = strings.TrimSpace(trimmed[2:])
		case strings.HasPrefix(trimmed, "## "):
			section = strings.TrimSpace(trimmed[3:])
		case strings.HasPrefix(trimmed, ">") && idx.Summary == "":
			idx.Summary = strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
		default:
			if m := mdLink.FindStringSubmatch(line); m != nil {
				idx.Links = append(idx.Links, LLMSLink{
					Section: section,
					Title:   strings.TrimSpace(m[1]),
					URL:     strings.TrimSpace(m[2]),
					Desc:    strings.TrimSpace(m[3]),
				})
			}
		}
	}
	return idx
}

// SelectLinks ranks llms.txt index links by relevance to topic and returns the
// top n. With an empty topic it returns the first n links in document order.
func SelectLinks(idx LLMSIndex, topic string, n int) []LLMSLink {
	if n <= 0 {
		n = 12
	}
	if strings.TrimSpace(topic) == "" {
		if len(idx.Links) > n {
			return idx.Links[:n]
		}
		return idx.Links
	}
	terms := tokenize(topic)
	type scored struct {
		link  LLMSLink
		score float64
		order int
	}
	var ranked []scored
	for i, l := range idx.Links {
		hay := strings.ToLower(l.Section + " " + l.Title + " " + l.Desc + " " + l.URL)
		ranked = append(ranked, scored{link: l, score: termScore(hay, terms), order: i})
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		if ranked[a].score != ranked[b].score {
			return ranked[a].score > ranked[b].score
		}
		return ranked[a].order < ranked[b].order
	})
	var out []LLMSLink
	for _, r := range ranked {
		if r.score == 0 && len(out) >= n {
			break
		}
		out = append(out, r.link)
		if len(out) >= n {
			break
		}
	}
	return out
}

// maxChunkTokens caps a section's size; larger sections are split on paragraph
// boundaries so one huge section can't dominate a token budget.
const maxChunkTokens = 700

// ChunkMarkdown splits markdown (e.g. llms-full.txt or a doc page) into
// heading-delimited sections, ignoring `#` lines inside fenced code blocks and
// splitting oversized sections. Pure.
func ChunkMarkdown(content string) []Chunk {
	lines := strings.Split(content, "\n")
	var chunks []Chunk
	var heading, body string
	inFence := false
	flush := func() {
		body = strings.TrimSpace(body)
		if body != "" || heading != "" {
			chunks = append(chunks, splitOversized(heading, body)...)
		}
		heading, body = "", ""
	}
	for _, line := range lines {
		if isFenceLine(line) {
			inFence = !inFence
			body += line + "\n"
			continue
		}
		if !inFence {
			if h := headingText(line); h != "" {
				flush()
				heading = h
				continue
			}
		}
		body += line + "\n"
	}
	flush()
	return chunks
}

// splitOversized breaks a section into <=maxChunkTokens chunks on blank-line
// (paragraph) boundaries, keeping the heading on the first piece.
func splitOversized(heading, body string) []Chunk {
	total := estimateTokens(heading + " " + body)
	if total <= maxChunkTokens {
		return []Chunk{{Heading: heading, Text: body, Tokens: total}}
	}
	paras := strings.Split(body, "\n\n")
	var out []Chunk
	var buf strings.Builder
	emit := func(first bool) {
		text := strings.TrimSpace(buf.String())
		if text == "" {
			return
		}
		h := ""
		if first {
			h = heading
		} else if heading != "" {
			h = heading + " (cont.)"
		}
		out = append(out, Chunk{Heading: h, Text: text, Tokens: estimateTokens(h + " " + text)})
		buf.Reset()
	}
	for _, p := range paras {
		if buf.Len() > 0 && estimateTokens(buf.String()+p) > maxChunkTokens {
			emit(len(out) == 0)
		}
		buf.WriteString(p)
		buf.WriteString("\n\n")
	}
	emit(len(out) == 0)
	if len(out) == 0 {
		return []Chunk{{Heading: heading, Text: body, Tokens: total}}
	}
	return out
}

func isFenceLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// RankChunks scores chunks against topic and returns up to maxTokens worth of
// the best chunks (in relevance order). With an empty topic it returns the
// leading chunks up to the budget. Pure.
func RankChunks(chunks []Chunk, topic string, maxTokens int) []Chunk {
	if maxTokens <= 0 {
		maxTokens = 5000
	}
	terms := tokenize(topic)
	if len(terms) > 0 {
		for i := range chunks {
			hay := strings.ToLower(chunks[i].Heading + "\n" + chunks[i].Text)
			s := termScore(hay, terms)
			// Heading matches are strong relevance signals.
			s += 2 * termScore(strings.ToLower(chunks[i].Heading), terms)
			chunks[i].Score = s
		}
		sort.SliceStable(chunks, func(a, b int) bool {
			return chunks[a].Score > chunks[b].Score
		})
		// Drop zero-score chunks entirely when we have a topic and matches exist.
		if len(chunks) > 0 && chunks[0].Score > 0 {
			filtered := chunks[:0]
			for _, c := range chunks {
				if c.Score > 0 {
					filtered = append(filtered, c)
				}
			}
			chunks = filtered
		}
	}
	var out []Chunk
	used := 0
	for _, c := range chunks {
		if used+c.Tokens > maxTokens && len(out) > 0 {
			break
		}
		out = append(out, c)
		used += c.Tokens
		if used >= maxTokens {
			break
		}
	}
	return out
}

// HTMLToText strips scripts/styles/nav and tags from an HTML doc page into
// readable text. A pragmatic extractor (no DOM dependency). Pure.
func HTMLToText(html string) string {
	for _, re := range htmlStripTags {
		html = re.ReplaceAllString(html, " ")
	}
	html = strings.ReplaceAll(html, "</p>", "\n\n")
	html = strings.ReplaceAll(html, "</li>", "\n")
	html = regexp.MustCompile(`(?i)</h[1-6]>`).ReplaceAllString(html, "\n\n")
	html = htmlTag.ReplaceAllString(html, " ")
	html = unescapeEntities(html)
	html = wsRun.ReplaceAllString(html, " ")
	html = blankRun.ReplaceAllString(html, "\n\n")
	var lines []string
	for _, l := range strings.Split(html, "\n") {
		lines = append(lines, strings.TrimSpace(l))
	}
	return strings.TrimSpace(blankRun.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

func headingText(line string) string {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "#") {
		return strings.TrimSpace(strings.TrimLeft(t, "#"))
	}
	return ""
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	fields := regexp.MustCompile(`[^a-z0-9]+`).Split(s, -1)
	var out []string
	for _, f := range fields {
		if len(f) >= 2 {
			out = append(out, f)
		}
	}
	return out
}

// termScore counts distinct + total term hits in hay (already lowercased).
func termScore(hay string, terms []string) float64 {
	if len(terms) == 0 {
		return 0
	}
	var score float64
	for _, t := range terms {
		if c := strings.Count(hay, t); c > 0 {
			score += 1 + 0.1*float64(c-1) // distinct match dominates, frequency adds a little
		}
	}
	return score
}

// estimateTokens approximates token count (~4 chars/token), good enough for
// budgeting without a tokenizer dependency.
func estimateTokens(s string) int {
	n := len(s) / 4
	if n < 1 && len(s) > 0 {
		return 1
	}
	return n
}

func unescapeEntities(s string) string {
	r := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"",
		"&#39;", "'", "&apos;", "'", "&nbsp;", " ", "&mdash;", "—",
	)
	return r.Replace(s)
}
