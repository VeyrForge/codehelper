package verify

import (
	"errors"
	"strings"
	"unicode"
)

// splitArgv tokenizes a POSIX-ish command line into argv without invoking a
// shell. Supports single quotes (raw), double quotes with backslash escape
// for `"`, `\` and `$`, and backslash-escaped characters in the unquoted
// state. It does NOT perform variable expansion, command substitution,
// glob expansion or pipes — that is exactly the point: argv mode keeps
// command and data separate (OWASP OS Command Injection Defense).
//
// Returns ErrUnclosedQuote if the input ends inside a quoted segment.
func splitArgv(s string) ([]string, error) {
	var (
		out    []string
		token  strings.Builder
		state  = stateUnquoted
		hasTok bool
	)
	flush := func() {
		if hasTok {
			out = append(out, token.String())
			token.Reset()
			hasTok = false
		}
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch state {
		case stateUnquoted:
			switch {
			case unicode.IsSpace(r):
				flush()
			case r == '\'':
				state = stateSingle
				hasTok = true
			case r == '"':
				state = stateDouble
				hasTok = true
			case r == '\\':
				if i+1 < len(runes) {
					i++
					token.WriteRune(runes[i])
					hasTok = true
				}
			default:
				token.WriteRune(r)
				hasTok = true
			}
		case stateSingle:
			if r == '\'' {
				state = stateUnquoted
				continue
			}
			token.WriteRune(r)
		case stateDouble:
			switch {
			case r == '"':
				state = stateUnquoted
			case r == '\\' && i+1 < len(runes):
				next := runes[i+1]
				if next == '"' || next == '\\' || next == '$' || next == '`' || next == '\n' {
					token.WriteRune(next)
					i++
				} else {
					token.WriteRune(r)
				}
			default:
				token.WriteRune(r)
			}
		}
	}
	if state != stateUnquoted {
		return nil, ErrUnclosedQuote
	}
	flush()
	return out, nil
}

const (
	stateUnquoted = iota
	stateSingle
	stateDouble
)

// ErrUnclosedQuote indicates the command line ended mid-quote.
var ErrUnclosedQuote = errors.New("unclosed quote in command")
