package verify

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrShellLineUnsupported is returned when a shell line uses constructs we refuse
// to allowlist-check safely (redirects, subshells, empty compound segments).
var ErrShellLineUnsupported = errors.New("unsupported shell construct")

// shellSegmentExecutables returns the executable token for every command segment
// in a shell line. Segments are split on ; && || | |& and bare & outside quotes.
// Redirections (>/<) and newlines are rejected — verify captures stdout/stderr
// itself, and redirect targets are not executables we can allowlist.
//
// Approach: compound/pipeline lines are allowed only when EVERY segment's
// executable is on the caller's allowlist (checked by the caller). This closes
// the first-token-only bypass (e.g. `go test; curl|sh` with go allowlisted).
func shellSegmentExecutables(cmdline string) ([]string, error) {
	segments, err := splitShellSegments(cmdline)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(segments))
	for _, seg := range segments {
		exe, err := segmentExecutable(seg)
		if err != nil {
			return nil, err
		}
		out = append(out, exe)
	}
	return out, nil
}

func splitShellSegments(cmdline string) ([]string, error) {
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" {
		return nil, ErrEmptyCommand
	}
	var (
		segments []string
		token    strings.Builder
		state    = stateUnquoted
		runes    = []rune(cmdline)
	)
	flush := func() error {
		seg := strings.TrimSpace(token.String())
		token.Reset()
		if seg == "" {
			return fmt.Errorf("%w: empty command segment in compound/pipeline", ErrShellLineUnsupported)
		}
		segments = append(segments, seg)
		return nil
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch state {
		case stateUnquoted:
			switch {
			case r == '\'':
				state = stateSingle
				token.WriteRune(r)
			case r == '"':
				state = stateDouble
				token.WriteRune(r)
			case r == '\\':
				token.WriteRune(r)
				if i+1 < len(runes) {
					i++
					token.WriteRune(runes[i])
				}
			case r == '\n' || r == '\r':
				return nil, fmt.Errorf("%w: newlines are not allowed in shell mode", ErrShellLineUnsupported)
			case r == '>' || r == '<':
				return nil, fmt.Errorf("%w: redirection is not allowed in shell mode (omit > < << >>; verify already captures stdout/stderr)", ErrShellLineUnsupported)
			case r == ';' || r == '|' || r == '&':
				// Multi-char operators: && || |&
				if r == '&' && i+1 < len(runes) && runes[i+1] == '&' {
					i++
				} else if r == '|' && i+1 < len(runes) && (runes[i+1] == '|' || runes[i+1] == '&') {
					i++
				}
				if err := flush(); err != nil {
					return nil, err
				}
			default:
				token.WriteRune(r)
			}
		case stateSingle:
			token.WriteRune(r)
			if r == '\'' {
				state = stateUnquoted
			}
		case stateDouble:
			token.WriteRune(r)
			if r == '\\' && i+1 < len(runes) {
				i++
				token.WriteRune(runes[i])
				continue
			}
			if r == '"' {
				state = stateUnquoted
			}
		}
	}
	if state != stateUnquoted {
		return nil, ErrUnclosedQuote
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return segments, nil
}

func segmentExecutable(seg string) (string, error) {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return "", fmt.Errorf("%w: empty command segment", ErrShellLineUnsupported)
	}
	if seg[0] == '(' || seg[0] == '{' {
		return "", fmt.Errorf("%w: subshell/group is not allowed in shell mode", ErrShellLineUnsupported)
	}
	argv, err := splitArgv(seg)
	if err != nil {
		return "", err
	}
	if len(argv) == 0 {
		return "", ErrEmptyCommand
	}
	i := 0
	for i < len(argv) && looksLikeEnvAssignment(argv[i]) {
		i++
	}
	if i >= len(argv) {
		return "", fmt.Errorf("%w: shell segment has no executable (only env assignments)", ErrShellLineUnsupported)
	}
	return argv[i], nil
}

func looksLikeEnvAssignment(tok string) bool {
	if tok == "" || tok[0] == '=' {
		return false
	}
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	name := tok[:eq]
	for i, r := range name {
		if i == 0 {
			if !unicode.IsLetter(r) && r != '_' {
				return false
			}
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}
