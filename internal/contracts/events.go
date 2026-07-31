package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// EventContract is a lightweight discovery record for event/async contracts.
// Sources: AsyncAPI channels, CloudEvents type fields, or simple event-name lists.
type EventContract struct {
	Path       string   `json:"path"`
	Format     string   `json:"format"` // json | yaml | text
	Source     string   `json:"source"` // asyncapi | cloudevents | event_pattern
	Title      string   `json:"title,omitempty"`
	Version    string   `json:"version,omitempty"`
	Channels   []string `json:"channels,omitempty"`
	EventNames []string `json:"event_names,omitempty"`
	RepoHint   string   `json:"repo_hint,omitempty"`
}

var (
	yamlChannelKeyRe = regexp.MustCompile(`(?m)^(\s*)([A-Za-z0-9_.\-/{}$]+):\s*(?:#.*)?$`)
	eventNameLineRe  = regexp.MustCompile(`(?m)^\s*[-*]?\s*["']?([A-Za-z][A-Za-z0-9_.\-]+(?:\.[A-Za-z0-9_.\-]+)+)["']?\s*$`)
	eventTypeKeyRe   = regexp.MustCompile(`(?m)^\s*(?:type|event[_-]?type|eventType|name|topic)\s*[:=]\s*["']?([A-Za-z][A-Za-z0-9_.\-:/]+)`)
	ceTypeJSONRe     = regexp.MustCompile(`"type"\s*:\s*"([^"]+)"`)
)

// DiscoverEvents finds AsyncAPI / CloudEvents / simple event-name contract files under root.
func DiscoverEvents(root string) []EventContract {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	candidates := []string{
		"asyncapi.json", "asyncapi.yaml", "asyncapi.yml",
		"api/asyncapi.json", "api/asyncapi.yaml", "api/asyncapi.yml",
		"docs/asyncapi.yaml", "docs/asyncapi.yml", "docs/asyncapi.json",
		"events/asyncapi.yaml", "events/asyncapi.yml", "events/asyncapi.json",
		"events.yaml", "events.yml", "events.json",
		"event-catalog.yaml", "event-catalog.yml", "event-catalog.json",
		"events/events.yaml", "events/events.yml", "events/events.json",
		"cloudevents.json", "events/cloudevents.json",
	}
	seen := map[string]struct{}{}
	var out []EventContract
	add := func(abs string) {
		c, ok := parseEventFile(abs)
		if !ok {
			return
		}
		key := filepath.ToSlash(c.Path)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	for _, rel := range candidates {
		add(filepath.Join(root, filepath.FromSlash(rel)))
	}
	for _, dir := range []string{"events", "asyncapi", "schemas/events"} {
		base := filepath.Join(root, filepath.FromSlash(dir))
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(e.Name())
			switch {
			case strings.HasSuffix(name, ".json"),
				strings.HasSuffix(name, ".yaml"),
				strings.HasSuffix(name, ".yml"),
				strings.HasSuffix(name, ".txt"):
				add(filepath.Join(base, e.Name()))
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func parseEventFile(abs string) (EventContract, bool) {
	b, err := os.ReadFile(abs)
	if err != nil || len(b) == 0 {
		return EventContract{}, false
	}
	ext := strings.ToLower(filepath.Ext(abs))
	switch ext {
	case ".json":
		if c, ok := parseAsyncAPIJSON(abs, b); ok {
			return c, true
		}
		if c, ok := parseCloudEventsJSON(abs, b); ok {
			return c, true
		}
		if c, ok := parseEventPatternJSON(abs, b); ok {
			return c, true
		}
	case ".yaml", ".yml":
		if c, ok := parseAsyncAPIYAMLLite(abs, b); ok {
			return c, true
		}
		if c, ok := parseEventPatternYAML(abs, b); ok {
			return c, true
		}
	case ".txt":
		if c, ok := parseEventPatternText(abs, b); ok {
			return c, true
		}
	}
	return EventContract{}, false
}

func parseAsyncAPIJSON(abs string, b []byte) (EventContract, bool) {
	var doc struct {
		AsyncAPI string `json:"asyncapi"`
		Info     struct {
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
		Channels map[string]json.RawMessage `json:"channels"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return EventContract{}, false
	}
	if doc.AsyncAPI == "" && len(doc.Channels) == 0 {
		return EventContract{}, false
	}
	channels := make([]string, 0, len(doc.Channels))
	for ch := range doc.Channels {
		channels = append(channels, ch)
	}
	sort.Strings(channels)
	return EventContract{
		Path:     abs,
		Format:   "json",
		Source:   "asyncapi",
		Title:    strings.TrimSpace(doc.Info.Title),
		Version:  strings.TrimSpace(doc.Info.Version),
		Channels: channels,
	}, true
}

func parseAsyncAPIYAMLLite(abs string, b []byte) (EventContract, bool) {
	text := string(b)
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "asyncapi:") && !strings.Contains(lower, "\nchannels:") {
		return EventContract{}, false
	}
	if !strings.Contains(lower, "asyncapi:") {
		return EventContract{}, false
	}
	c := EventContract{Path: abs, Format: "yaml", Source: "asyncapi"}
	if m := yamlTitleRe.FindStringSubmatch(text); len(m) >= 2 {
		c.Title = strings.TrimSpace(m[1])
	}
	if m := yamlVersionRe.FindStringSubmatch(text); len(m) >= 2 {
		c.Version = strings.TrimSpace(m[1])
	}
	inChannels := false
	channelsIndent := -1
	var channels []string
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		indent := countLeadingSpaces(line)
		if strings.HasPrefix(trim, "channels:") {
			inChannels = true
			channelsIndent = indent
			continue
		}
		if !inChannels {
			continue
		}
		if indent <= channelsIndent && !strings.HasPrefix(trim, "-") {
			inChannels = false
			continue
		}
		if indent == channelsIndent+2 || (channelsIndent >= 0 && indent > channelsIndent) {
			if m := yamlChannelKeyRe.FindStringSubmatch(line); len(m) >= 3 {
				key := strings.TrimSpace(m[2])
				if key != "" && !strings.HasPrefix(key, "parameters") && !strings.HasPrefix(key, "publish") &&
					!strings.HasPrefix(key, "subscribe") && !strings.HasPrefix(key, "description") &&
					!strings.HasPrefix(key, "servers") && !strings.HasPrefix(key, "bindings") {
					// Only accept top-level channel keys (one indent step under channels).
					if indent == channelsIndent+2 || indent == channelsIndent+1 {
						channels = append(channels, key)
					}
				}
			}
		}
	}
	c.Channels = uniqueStrings(channels)
	sort.Strings(c.Channels)
	return c, true
}

func parseCloudEventsJSON(abs string, b []byte) (EventContract, bool) {
	lower := strings.ToLower(string(b))
	if !strings.Contains(lower, "specversion") && !strings.Contains(lower, "cloudevents") {
		return EventContract{}, false
	}
	var names []string
	// Single event object.
	var one struct {
		SpecVersion string `json:"specversion"`
		Type        string `json:"type"`
	}
	if err := json.Unmarshal(b, &one); err == nil && (one.SpecVersion != "" || one.Type != "") {
		if t := strings.TrimSpace(one.Type); t != "" {
			names = append(names, t)
		}
	}
	// Array of events or envelope with events[].
	var arr []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &arr); err == nil {
		for _, e := range arr {
			if t := strings.TrimSpace(e.Type); t != "" {
				names = append(names, t)
			}
		}
	}
	var envelope struct {
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
		Types []string `json:"types"`
	}
	if err := json.Unmarshal(b, &envelope); err == nil {
		for _, e := range envelope.Events {
			if t := strings.TrimSpace(e.Type); t != "" {
				names = append(names, t)
			}
		}
		names = append(names, envelope.Types...)
	}
	if len(names) == 0 {
		for _, m := range ceTypeJSONRe.FindAllStringSubmatch(string(b), -1) {
			if len(m) >= 2 {
				names = append(names, m[1])
			}
		}
	}
	names = uniqueStrings(names)
	if len(names) == 0 {
		return EventContract{}, false
	}
	sort.Strings(names)
	return EventContract{
		Path:       abs,
		Format:     "json",
		Source:     "cloudevents",
		EventNames: names,
	}, true
}

func parseEventPatternJSON(abs string, b []byte) (EventContract, bool) {
	var doc struct {
		Events []string `json:"events"`
		Names  []string `json:"names"`
		Topics []string `json:"topics"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return EventContract{}, false
	}
	names := append([]string{}, doc.Events...)
	names = append(names, doc.Names...)
	names = append(names, doc.Topics...)
	names = uniqueStrings(names)
	if len(names) == 0 {
		return EventContract{}, false
	}
	base := strings.ToLower(filepath.Base(abs))
	if !strings.Contains(base, "event") && !strings.Contains(base, "topic") {
		return EventContract{}, false
	}
	sort.Strings(names)
	return EventContract{
		Path:       abs,
		Format:     "json",
		Source:     "event_pattern",
		EventNames: names,
	}, true
}

func parseEventPatternYAML(abs string, b []byte) (EventContract, bool) {
	text := string(b)
	lower := strings.ToLower(text)
	base := strings.ToLower(filepath.Base(abs))
	looksEvent := strings.Contains(base, "event") || strings.Contains(base, "topic") ||
		strings.Contains(lower, "\nevents:") || strings.Contains(lower, "\ntopics:")
	if !looksEvent || strings.Contains(lower, "asyncapi:") {
		return EventContract{}, false
	}
	var names []string
	for _, m := range eventTypeKeyRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			names = append(names, strings.TrimSpace(m[1]))
		}
	}
	for _, m := range eventNameLineRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			names = append(names, strings.TrimSpace(m[1]))
		}
	}
	names = uniqueStrings(names)
	if len(names) == 0 {
		return EventContract{}, false
	}
	sort.Strings(names)
	return EventContract{
		Path:       abs,
		Format:     "yaml",
		Source:     "event_pattern",
		EventNames: names,
	}, true
}

func parseEventPatternText(abs string, b []byte) (EventContract, bool) {
	base := strings.ToLower(filepath.Base(abs))
	if !strings.Contains(base, "event") && !strings.Contains(base, "topic") {
		return EventContract{}, false
	}
	var names []string
	for _, line := range strings.Split(string(b), "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if m := eventNameLineRe.FindStringSubmatch(trim); len(m) >= 2 {
			names = append(names, m[1])
			continue
		}
		// Plain dotted event names on their own line.
		if strings.Count(trim, ".") >= 1 && !strings.ContainsAny(trim, " \t") {
			names = append(names, trim)
		}
	}
	names = uniqueStrings(names)
	if len(names) == 0 {
		return EventContract{}, false
	}
	sort.Strings(names)
	return EventContract{
		Path:       abs,
		Format:     "text",
		Source:     "event_pattern",
		EventNames: names,
	}, true
}
