package verify

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// JoinArgv joins argv tokens into a single command line that splitArgv can
// reverse losslessly for common cases (no shell). Tokens with whitespace or
// quotes are double-quoted with \" and \\ escapes.
func JoinArgv(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, quoteArgvToken(a))
	}
	return strings.Join(parts, " ")
}

func quoteArgvToken(a string) string {
	if a == "" {
		return `""`
	}
	needs := false
	for _, r := range a {
		if unicode.IsSpace(r) || r == '"' || r == '\'' || r == '\\' || r == '$' || r == '`' {
			needs = true
			break
		}
	}
	if !needs {
		return a
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range a {
		switch r {
		case '\\', '"', '$', '`':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// NormalizeCommandLine accepts agent/MCP command inputs that may arrive as:
//   - a plain argv string: `go test ./...`
//   - a JSON array string: `["go","test","./..."]` (some clients stringify arrays)
//   - a Go fmt.Sprint mangled slice: `[go test ./...]` (Windows MCP footgun when
//     lint_cmd is a JSON array but the host/tool layer Sprint's it)
//
// recovered is true when a mangled/JSON-array form was converted into a normal
// argv string. Callers should surface a short correction note so agents learn
// to pass plain strings or real JSON arrays.
func NormalizeCommandLine(raw string) (cmdline string, recovered bool, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false, nil
	}
	// JSON array string (clients that stringify arrays).
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		var arr []string
		if jerr := json.Unmarshal([]byte(s), &arr); jerr == nil && len(arr) > 0 {
			return JoinArgv(arr), true, nil
		}
		// Go fmt.Sprint([]string{...}) → "[echo ok]" (no quotes around tokens).
		if mangled, ok := unmangleGoSliceSprint(s); ok {
			return JoinArgv(mangled), true, nil
		}
		return "", false, fmt.Errorf("%w: %q looks like a mangled argv array — pass a plain string (e.g. \"go test ./...\") or a real JSON array [\"go\",\"test\",\"./...\"]", ErrMangledArgv, truncateCmd(s, 80))
	}
	return s, false, nil
}

// ErrMangledArgv indicates a command line that looks like a Sprint'd JSON array
// and could not be safely recovered.
var ErrMangledArgv = errors.New("mangled argv array")

// unmangleGoSliceSprint recovers tokens from fmt.Sprint of a []string / []any
// of simple tokens: "[cmd /c echo ok]" → ["cmd","/c","echo","ok"].
// Refuses when tokens need quoting (embedded spaces inside a token cannot be
// recovered from Sprint form).
func unmangleGoSliceSprint(s string) ([]string, bool) {
	if len(s) < 3 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, false
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil, false
	}
	// JSON arrays with quoted strings are handled elsewhere; Sprint form has no quotes.
	if strings.ContainsAny(inner, `"'`) {
		return nil, false
	}
	parts := strings.Fields(inner)
	if len(parts) == 0 {
		return nil, false
	}
	// First token must look like an executable / path fragment, not `[echo`.
	first := parts[0]
	if first == "" || first[0] == '[' {
		return nil, false
	}
	return parts, true
}

func truncateCmd(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
