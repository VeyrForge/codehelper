package mcpsvc

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/verify"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- diagnostics -----------------------------------------------------------

type diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
}

type diagnosticsResponse struct {
	Toolchain    string       `json:"toolchain"`
	Commands     []string     `json:"commands"`
	OK           bool         `json:"ok"`
	Problems     []diagnostic `json:"problems"`
	ProblemCount int          `json:"problem_count"`
	Truncated    int          `json:"truncated,omitempty"`
	RawTail      string       `json:"raw_tail,omitempty"`
	Note         string       `json:"note"`
}

const maxDiagnostics = 100

// toolchainProbe maps a marker file in the repo root to the canonical static
// checks for that toolchain, plus the executable basenames the argv-mode runner
// is allowed to launch.
type toolchainProbe struct {
	name    string
	marker  string
	cmds    []string
	allowed []string
}

// orderedToolchains is checked in order; the first marker present wins, so a Go
// module is diagnosed with go vet/build before a stray package.json matters.
// Coverage spans the toolchains people actually run codehelper on; each check is
// a dependency-light STATIC check (compile/typecheck, not the full test suite).
var orderedToolchains = []toolchainProbe{
	{"go", "go.mod", []string{"go build ./...", "go vet ./..."}, []string{"go"}},
	{"rust", "Cargo.toml", []string{"cargo check --quiet"}, []string{"cargo"}},
	{"typescript", "tsconfig.json", []string{"npx --no-install tsc --noEmit"}, []string{"npx", "tsc"}},
	// PHP: phpstan/larastan is the de-facto static analyzer for Laravel & modern
	// PHP. Its neon config marks the project; --error-format=raw emits parseable
	// "path:line:message" and it honors any phpstan-baseline.neon automatically.
	{"php", "phpstan.neon", []string{"vendor/bin/phpstan analyse --no-progress --no-interaction --error-format=raw"}, []string{"phpstan"}},
	{"php", "phpstan.neon.dist", []string{"vendor/bin/phpstan analyse --no-progress --no-interaction --error-format=raw"}, []string{"phpstan"}},
	// Python: compileall is stdlib (always available) and catches syntax errors
	// without needing the project's deps installed. pyproject/setup/requirements
	// each mark a Python project.
	{"python", "pyproject.toml", []string{"python3 -m compileall -q ."}, []string{"python3", "python"}},
	{"python", "setup.py", []string{"python3 -m compileall -q ."}, []string{"python3", "python"}},
	{"python", "requirements.txt", []string{"python3 -m compileall -q ."}, []string{"python3", "python"}},
	// JVM: compile only (fast, no tests).
	{"java-maven", "pom.xml", []string{"mvn -q -e -DskipTests compile"}, []string{"mvn"}},
	{"java-gradle", "build.gradle", []string{"gradle -q compileJava"}, []string{"gradle"}},
}

// diagnosticsHandler gives the agent an LSP-free self-check loop: it detects the
// repo's toolchain and runs its canonical static checks (go vet + go build, cargo
// check, tsc --noEmit) through the sandboxed argv-mode verify runner, then parses
// the compiler/vet output into structured file:line problems. This is the one
// capability LSP-backed competitors have that a pure tree-sitter index lacks —
// without taking on an LSP dependency.
func diagnosticsHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		timeout := int(mcp.ParseInt64(req, "timeout_seconds", 0))
		if timeout <= 0 {
			timeout = 120
		}

		override := strings.TrimSpace(argString(args, "command"))
		var (
			toolchain string
			cmds      []string
			allowed   []string
			runRoot   = repo.RootPath
		)
		if override != "" {
			toolchain = "custom"
			cmds = []string{override}
			if fields := strings.Fields(override); len(fields) > 0 {
				allowed = []string{filepath.Base(fields[0])}
			}
		} else {
			ws := resolveVerifyWorkspace(repo.RootPath)
			if len(ws.Cmds) > 0 {
				toolchain, cmds, allowed = ws.Toolchain, ws.Cmds, ws.Allowed
				runRoot = ws.Cwd
			}
		}
		if len(cmds) == 0 {
			note := verifyWorkspaceNote(resolveVerifyWorkspace(repo.RootPath))
			return mustToolResultFormatted(diagnosticsResponse{
				OK:   true,
				Note: note,
			}, resolveFormat(args))
		}

		outcomes := verify.RunCommandLines(ctx, cmds, verify.RunCommandsOptions{
			RepoRoot:        runRoot,
			ExecMode:        verify.ExecArgv,
			AllowedCommands: connections.ResolveVerifyAllowlist(repo.RootPath, allowed),
			BlockPolicy:     connections.VerifyBlockPolicy(repo.RootPath),
			TimeoutSeconds:  timeout,
		})

		out := diagnosticsResponse{Toolchain: toolchain, Commands: cmds, OK: !verify.HasFailures(outcomes)}
		var combined strings.Builder
		for _, o := range outcomes {
			combined.WriteString(o.Output)
			combined.WriteString("\n")
		}
		problems := parseDiagnostics(combined.String())
		out.ProblemCount = len(problems)
		if len(problems) > maxDiagnostics {
			out.Truncated = len(problems) - maxDiagnostics
			problems = problems[:maxDiagnostics]
		}
		out.Problems = problems

		switch {
		case out.OK:
			out.Note = "clean — all checks passed. Safe to proceed."
		case len(problems) == 0:
			// Failed but nothing parsed (e.g. timeout, missing toolchain binary).
			out.RawTail = tailString(verify.FailuresText(outcomes), 2000)
			out.Note = "checks failed but no file:line problems were parsed — see raw_tail (the tool may be missing, or the failure isn't a compile error)."
		default:
			out.Note = "fix the problems above, then re-run diagnostics. Locations are file:line:col from the compiler/vet."
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func tailString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// goDiagRe matches Go compiler/vet lines: "path.go:12:5: message" (col optional).
var goDiagRe = regexp.MustCompile(`^(.+?\.go):(\d+):(?:(\d+):)?\s*(.*)$`)

// tscDiagRe matches tsc lines: "src/x.ts(12,5): error TS2304: message".
var tscDiagRe = regexp.MustCompile(`^(.+?\.tsx?)\((\d+),(\d+)\):\s*(error|warning)\s+\w+:\s*(.*)$`)

// pyLintRe matches ruff/flake8/mypy lines: "path.py:12:5: CODE message" (col optional).
var pyLintRe = regexp.MustCompile(`^(.+?\.py):(\d+):(?:(\d+):)?\s*(.*)$`)

// phpDiagRe matches phpstan --error-format=raw lines: "path.php:12:message".
var phpDiagRe = regexp.MustCompile(`^(.+?\.php):(\d+):\s*(.*)$`)

// pyFileRe matches the compileall / traceback location line: File "x.py", line 12.
var pyFileRe = regexp.MustCompile(`^\s*File "(.+?\.py)", line (\d+)`)

// pyErrRe matches the error class line that follows it: "SyntaxError: invalid syntax".
var pyErrRe = regexp.MustCompile(`^(\w*(?:Error|Warning)):\s*(.*)$`)

// parseDiagnostics extracts structured problems from compiler/vet stdout+stderr.
// Best-effort and format-specific (Go, TypeScript); unrecognized lines are
// ignored (the caller keeps a raw tail for context).
func parseDiagnostics(output string) []diagnostic {
	var out []diagnostic
	// pending holds a Python location line ("File \"x.py\", line N") awaiting the
	// error-class line that names the actual problem on a later line.
	var pending *diagnostic
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimRight(raw, "\r")
		line = strings.TrimPrefix(line, "./")
		if m := goDiagRe.FindStringSubmatch(line); m != nil {
			d := diagnostic{File: m[1], Severity: "error", Message: strings.TrimSpace(m[4])}
			d.Line, _ = strconv.Atoi(m[2])
			if m[3] != "" {
				d.Col, _ = strconv.Atoi(m[3])
			}
			if d.Message != "" {
				out = append(out, d)
			}
			continue
		}
		if m := tscDiagRe.FindStringSubmatch(line); m != nil {
			d := diagnostic{File: m[1], Severity: m[4], Message: strings.TrimSpace(m[5])}
			d.Line, _ = strconv.Atoi(m[2])
			d.Col, _ = strconv.Atoi(m[3])
			out = append(out, d)
			continue
		}
		// PHP (phpstan raw): path.php:line:message.
		if m := phpDiagRe.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[3]) != "" {
			d := diagnostic{File: m[1], Severity: "error", Message: strings.TrimSpace(m[3])}
			d.Line, _ = strconv.Atoi(m[2])
			out = append(out, d)
			continue
		}
		// Python lint/typecheck (ruff/flake8/mypy): path.py:line:col: message.
		if m := pyLintRe.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[4]) != "" {
			d := diagnostic{File: m[1], Severity: "error", Message: strings.TrimSpace(m[4])}
			d.Line, _ = strconv.Atoi(m[2])
			if m[3] != "" {
				d.Col, _ = strconv.Atoi(m[3])
			}
			out = append(out, d)
			continue
		}
		// Python compileall / traceback: a File "..." line, then an Error line.
		if m := pyFileRe.FindStringSubmatch(line); m != nil {
			d := diagnostic{File: strings.TrimPrefix(m[1], "./"), Severity: "error"}
			d.Line, _ = strconv.Atoi(m[2])
			pending = &d
			continue
		}
		if pending != nil {
			if m := pyErrRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				pending.Message = m[1] + ": " + strings.TrimSpace(m[2])
				out = append(out, *pending)
				pending = nil
			}
		}
	}
	return out
}
