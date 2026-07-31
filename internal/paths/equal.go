package paths

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Canonical returns a stable form for comparing index roots across slash style
// and, on Windows, drive/path letter case. Relative segments are Clean'd but
// not Abs'd (callers that already hold absolute roots should pass them as-is).
// Backslashes are normalized on every OS so JSON/MCP Windows paths still match
// when compared on Linux CI or mixed-separator inputs.
func Canonical(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, `/`)
	p = filepath.ToSlash(filepath.Clean(p))
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// Equal reports whether a and b name the same path for index-root identity
// (slash-normalized; case-insensitive on Windows). Empty strings only match empty.
func Equal(a, b string) bool {
	if a == b {
		return true
	}
	ca := Canonical(a)
	cb := Canonical(b)
	if ca == "" || cb == "" {
		return ca == cb
	}
	return ca == cb
}

// EqualIndexRoot is like Equal after Abs when possible, so relative vs absolute
// forms of the same workspace compare equal.
func EqualIndexRoot(a, b string) bool {
	if Equal(a, b) {
		return true
	}
	aa, errA := filepath.Abs(filepath.Clean(strings.TrimSpace(a)))
	bb, errB := filepath.Abs(filepath.Clean(strings.TrimSpace(b)))
	if errA != nil || errB != nil {
		return false
	}
	return Equal(aa, bb)
}
