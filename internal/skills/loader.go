package skills

import (
	"encoding/json"
	"io/fs"
	"strings"
)

// Match describes a skill relevant to a task.
type Match struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Checklist []string `json:"checklist,omitempty"`
	WhenToUse string   `json:"when_to_use,omitempty"`
}

type skillMeta struct {
	ID        string
	Title     string
	WhenToUse string
	Checklist []string
}

// MatchTask returns embedded skills whose keywords appear in the request or project type.
func MatchTask(request, projectType string) []Match {
	lq := strings.ToLower(request + " " + projectType)
	var out []Match
	_ = fs.WalkDir(data, "data", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "SKILL.md") {
			return err
		}
		b, err := data.ReadFile(path)
		if err != nil {
			return nil
		}
		meta := parseSkillMeta(string(b), path)
		if meta.ID == "" {
			return nil
		}
		key := strings.ToLower(meta.ID + " " + meta.Title + " " + meta.WhenToUse)
		if !stringsContainsAny(lq, key) && !topicMatch(lq, meta.ID) {
			return nil
		}
		out = append(out, Match{
			ID: meta.ID, Title: meta.Title, Checklist: meta.Checklist, WhenToUse: meta.WhenToUse,
		})
		return nil
	})
	return out
}

// PromptAddendum returns skill checklist text for agent system prompts.
func PromptAddendum(request, projectType string) string {
	matches := MatchTask(request, projectType)
	if len(matches) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n<skill_checklists>\n")
	for _, m := range matches {
		b.WriteString("- ")
		b.WriteString(m.Title)
		if len(m.Checklist) > 0 {
			b.WriteString(": ")
			b.WriteString(strings.Join(m.Checklist, "; "))
		}
		b.WriteByte('\n')
	}
	b.WriteString("</skill_checklists>\n")
	return b.String()
}

func parseSkillMeta(body, path string) skillMeta {
	rel, _ := strings.CutPrefix(path, "data/")
	id := strings.TrimSuffix(strings.TrimSuffix(rel, "/SKILL.md"), "/SKILL.md")
	id = strings.ReplaceAll(id, "/", "-")
	title := id
	var checklist []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") && title == id {
			title = strings.TrimPrefix(line, "# ")
		}
		if strings.HasPrefix(line, "- ") {
			checklist = append(checklist, strings.TrimPrefix(line, "- "))
		}
	}
	when := ""
	if idx := strings.Index(strings.ToLower(body), "when to use"); idx >= 0 {
		chunk := body[idx:]
		if end := strings.Index(chunk, "\n\n"); end > 0 {
			when = strings.TrimSpace(chunk[:end])
		}
	}
	return skillMeta{ID: id, Title: title, WhenToUse: when, Checklist: checklist}
}

func stringsContainsAny(haystack, needle string) bool {
	for _, tok := range strings.Fields(needle) {
		if len(tok) < 4 {
			continue
		}
		if strings.Contains(haystack, tok) {
			return true
		}
	}
	return false
}

func topicMatch(lq, id string) bool {
	switch {
	case strings.Contains(id, "debug") && strings.Contains(lq, "debug"):
		return true
	case strings.Contains(id, "impact") && strings.Contains(lq, "impact"):
		return true
	case strings.Contains(id, "refactor") && strings.Contains(lq, "refactor"):
		return true
	case strings.Contains(id, "explor") && (strings.Contains(lq, "explore") || strings.Contains(lq, "architecture")):
		return true
	}
	return false
}

// ManifestVersion returns embedded skills bundle version.
func ManifestVersion() string {
	mf, err := loadManifest()
	if err != nil {
		return ""
	}
	b, _ := json.Marshal(mf.Version)
	return strings.Trim(string(b), `"`)
}
