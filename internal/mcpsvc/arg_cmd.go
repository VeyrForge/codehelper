package mcpsvc

import (
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/verify"
)

// argCommandLine reads lint_cmd / build_cmd / test_cmd style MCP args.
// Accepts:
//   - plain string: "go test ./..."
//   - JSON array ([]string / []any): ["go","test","./..."] — preferred when agents
//     pass argv as an array (must NOT be Sprint'd to "[go test ./...]")
//   - JSON-array string or Go fmt.Sprint mangled slice (recovered with a note)
//
// recoveredNote is non-empty when a mangled/JSON-array form was normalized so
// agents learn the preferred plain-string shape.
func argCommandLine(args map[string]any, keys ...string) (cmdline, recoveredNote string, err error) {
	if args == nil {
		return "", "", nil
	}
	for _, k := range keys {
		v, ok := args[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			s := strings.TrimSpace(t)
			if s == "" {
				continue
			}
			norm, recovered, nerr := verify.NormalizeCommandLine(s)
			if nerr != nil {
				return "", "", fmt.Errorf("%s: %w", k, nerr)
			}
			if recovered {
				return norm, fmt.Sprintf(
					"Accepted mangled/JSON-array %s — prefer a plain argv string (e.g. \"go test ./...\") or a real JSON array [\"go\",\"test\",\"./...\"] so Windows MCP hosts do not Sprint the array.",
					k,
				), nil
			}
			return norm, "", nil
		case []string:
			if len(t) == 0 {
				continue
			}
			return verify.JoinArgv(t), "", nil
		case []any:
			parts := make([]string, 0, len(t))
			for _, e := range t {
				switch x := e.(type) {
				case string:
					parts = append(parts, x)
				case fmt.Stringer:
					parts = append(parts, x.String())
				default:
					parts = append(parts, fmt.Sprint(x))
				}
			}
			if len(parts) == 0 {
				continue
			}
			return verify.JoinArgv(parts), "", nil
		default:
			// Last resort: Sprint then normalize (covers odd MCP host coercions).
			norm, recovered, nerr := verify.NormalizeCommandLine(fmt.Sprint(t))
			if nerr != nil {
				return "", "", fmt.Errorf("%s: %w", k, nerr)
			}
			if strings.TrimSpace(norm) == "" {
				continue
			}
			note := ""
			if recovered {
				note = fmt.Sprintf(
					"Accepted non-string %s via Sprint+normalize — prefer plain string or JSON array of argv tokens.",
					k,
				)
			}
			return norm, note, nil
		}
	}
	return "", "", nil
}

// argAliasCorrection returns a teach-fix note when an alias key was used
// without the canonical key (e.g. symbol= without name= for context).
func argAliasCorrection(args map[string]any, canonical string, aliases ...string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[canonical]; ok && v != nil {
		if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && s != "<nil>" {
			return ""
		}
	}
	for _, a := range aliases {
		if v, ok := args[a]; ok && v != nil {
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && s != "<nil>" {
				return fmt.Sprintf("Accepted alias: you passed %s=…; canonical key is %s=. Prefer %s= on the next call.", a, canonical, canonical)
			}
		}
	}
	return ""
}
