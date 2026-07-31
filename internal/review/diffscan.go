package review

import (
	"strings"
)

// DiffFile holds the added (+) lines for a single file in a unified diff.
type DiffFile struct {
	Path  string
	Added []DiffLine
}

// DiffLine is one added line and its target-file line number.
type DiffLine struct {
	LineNo  int
	Content string
}

// ParseUnifiedDiff extracts added lines per file from a unified-diff string.
// Renamed/binary/deleted-only files are skipped. The result preserves order.
func ParseUnifiedDiff(diff string) []DiffFile {
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	var (
		files     []DiffFile
		current   *DiffFile
		lineNo    int
		inHunk    bool
		pathHints = []string{"+++ ", "--- "}
	)
	_ = pathHints
	for _, raw := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(raw, "diff --git "):
			if current != nil {
				files = append(files, *current)
			}
			current = &DiffFile{}
			inHunk = false
		case strings.HasPrefix(raw, "+++ "):
			if current == nil {
				current = &DiffFile{}
			}
			p := strings.TrimPrefix(raw, "+++ ")
			p = strings.TrimSpace(p)
			if p == "/dev/null" {
				current.Path = ""
				continue
			}
			// strip a/ b/ prefixes used by git
			if strings.HasPrefix(p, "b/") {
				p = strings.TrimPrefix(p, "b/")
			}
			current.Path = p
		case strings.HasPrefix(raw, "@@"):
			inHunk = true
			lineNo = parseHunkNewStart(raw)
		case inHunk && current != nil && current.Path != "":
			if len(raw) == 0 {
				lineNo++
				continue
			}
			switch raw[0] {
			case '+':
				if strings.HasPrefix(raw, "+++") {
					continue
				}
				current.Added = append(current.Added, DiffLine{LineNo: lineNo, Content: raw[1:]})
				lineNo++
			case '-':
				if strings.HasPrefix(raw, "---") {
					continue
				}
			case '\\':
			default:
				lineNo++
			}
		}
	}
	if current != nil {
		files = append(files, *current)
	}
	out := make([]DiffFile, 0, len(files))
	for _, f := range files {
		if f.Path == "" {
			continue
		}
		out = append(out, f)
	}
	return out
}

// parseHunkNewStart pulls the new-file starting line from a hunk header
// such as "@@ -10,4 +12,7 @@".
func parseHunkNewStart(hdr string) int {
	plus := strings.Index(hdr, "+")
	if plus < 0 {
		return 1
	}
	rest := hdr[plus+1:]
	end := strings.IndexAny(rest, " ,")
	if end < 0 {
		end = len(rest)
	}
	n := 0
	for i := 0; i < end; i++ {
		c := rest[i]
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return 1
	}
	return n
}

// Trimmed returns lowercased, trimmed content for cheap pattern checks.
func (d DiffLine) Trimmed() string {
	return strings.ToLower(strings.TrimSpace(d.Content))
}
