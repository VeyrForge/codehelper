package mcpsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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
	Toolchain            string       `json:"toolchain"`
	Commands             []string     `json:"commands"`
	OK                   bool         `json:"ok"`
	Problems             []diagnostic `json:"problems"`
	ProblemCount         int          `json:"problem_count"`
	ActionableCount      int          `json:"actionable_count,omitempty"`
	GeneratedCount       int          `json:"generated_count,omitempty"`
	Truncated            int          `json:"truncated,omitempty"`
	RawTail              string       `json:"raw_tail,omitempty"`
	Note                 string       `json:"note"`
	WhatNext             string       `json:"what_next,omitempty"`
	RecommendedNextTools []string     `json:"recommended_next_tools,omitempty"`
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
// Python/PHP commands are refined further in toolchainAt (pyright/mypy, phpstan
// without neon).
var orderedToolchains = []toolchainProbe{
	{"go", "go.mod", []string{"go build ./...", "go vet ./..."}, []string{"go"}},
	{"rust", "Cargo.toml", []string{"cargo check --quiet"}, []string{"cargo"}},
	{"typescript", "tsconfig.json", []string{"npx --no-install tsc --noEmit"}, []string{"npx", "tsc"}},
	// PHP: prefer neon when present; composer.json alone is handled in toolchainAt
	// so projects without phpstan.neon still get phpstan (vendor) or php -l.
	{"php", "phpstan.neon", []string{"vendor/bin/phpstan analyse --no-progress --no-interaction --error-format=raw"}, []string{"phpstan", "php"}},
	{"php", "phpstan.neon.dist", []string{"vendor/bin/phpstan analyse --no-progress --no-interaction --error-format=raw"}, []string{"phpstan", "php"}},
	{"php", "composer.json", nil, []string{"phpstan", "php"}}, // cmds filled by phpDiagCmds
	// Python: compileall is the always-available fallback; toolchainAt upgrades to
	// pyright/mypy/ruff when those binaries (or local installs) are present.
	{"python", "pyproject.toml", []string{"python3 -m compileall -q ."}, []string{"python3", "python", "pyright", "mypy", "ruff"}},
	{"python", "setup.py", []string{"python3 -m compileall -q ."}, []string{"python3", "python", "pyright", "mypy", "ruff"}},
	{"python", "requirements.txt", []string{"python3 -m compileall -q ."}, []string{"python3", "python", "pyright", "mypy", "ruff"}},
	{"python", "Pipfile", []string{"python3 -m compileall -q ."}, []string{"python3", "python", "pyright", "mypy", "ruff"}},
	// JVM: compile only (fast, no tests).
	{"java-maven", "pom.xml", []string{"mvn -q -e -DskipTests compile"}, []string{"mvn"}},
	{"java-gradle", "build.gradle", []string{"gradle -q compileJava"}, []string{"gradle"}},
	// Node without tsconfig — LAST: package.json is often incidental (Django assets,
	// Laravel Mix). Stronger markers above must win.
	{"javascript", "package.json", nil, []string{"node", "npx", "eslint"}},
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
				OK:                   true,
				Note:                 note,
				WhatNext:             "Pass command= for an explicit check, or proceed to review_diff → verify with explicit lint/build/test cmds",
				RecommendedNextTools: []string{"review_diff", "verify", "project_context"},
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
		actionable, generated := bucketDiagnostics(problems)
		out.ProblemCount = len(problems)
		out.ActionableCount = len(actionable)
		out.GeneratedCount = len(generated)
		// Surface actionable (app/source) errors first; generated/.next noise last.
		ordered := append(append([]diagnostic{}, actionable...), generated...)
		if len(ordered) > maxDiagnostics {
			out.Truncated = len(ordered) - maxDiagnostics
			ordered = ordered[:maxDiagnostics]
		}
		out.Problems = ordered

		switch {
		case out.OK:
			out.Note = "clean — all checks passed. Safe to proceed."
			out.WhatNext = "Run review_diff → verify (argv) → finish_check verify_ran=true before claiming done"
			out.RecommendedNextTools = []string{"review_diff", "verify", "finish_check"}
		case len(problems) == 0:
			// Failed but nothing parsed (e.g. timeout, missing toolchain binary).
			out.RawTail = tailString(verify.FailuresText(outcomes), 2000)
			out.Note = "checks failed but no file:line problems were parsed — see raw_tail (the tool may be missing, or the failure isn't a compile error)."
			out.WhatNext = "Inspect raw_tail, fix the toolchain/command, then re-run diagnostics"
			out.RecommendedNextTools = []string{"diagnostics", "verify", "read_workspace_file"}
		default:
			out.Note = "fix the problems above, then re-run diagnostics. Locations are file:line:col from the compiler/vet."
			if out.GeneratedCount > 0 {
				out.Note += fmt.Sprintf(
					" %d problem(s) are in generated/build paths (.next, dist, …) and were sorted after %d actionable source problem(s).",
					out.GeneratedCount, out.ActionableCount,
				)
			}
			out.WhatNext = "Fix actionable file:line problems, re-run diagnostics, then review_diff → verify"
			out.RecommendedNextTools = []string{"diagnostics", "apply_patch_workspace_file", "review_diff"}
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
// Best-effort and format-specific (Go, TypeScript, Python, PHP, pyright JSON);
// unrecognized lines are ignored (the caller keeps a raw tail for context).
func parseDiagnostics(output string) []diagnostic {
	if digs := parsePyrightJSON(output); len(digs) > 0 {
		return digs
	}
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

// parsePyrightJSON extracts diagnostics from pyright --outputjson payloads.
func parsePyrightJSON(output string) []diagnostic {
	output = strings.TrimSpace(output)
	if output == "" || !strings.Contains(output, "generalDiagnostics") {
		return nil
	}
	// pyright may print non-JSON noise before/after; find the outermost object.
	start := strings.Index(output, "{")
	end := strings.LastIndex(output, "}")
	if start < 0 || end <= start {
		return nil
	}
	var payload struct {
		GeneralDiagnostics []struct {
			File     string `json:"file"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
			Range    *struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
			} `json:"range"`
		} `json:"generalDiagnostics"`
	}
	if json.Unmarshal([]byte(output[start:end+1]), &payload) != nil {
		return nil
	}
	var out []diagnostic
	for _, g := range payload.GeneralDiagnostics {
		if strings.TrimSpace(g.Message) == "" {
			continue
		}
		d := diagnostic{
			File:     g.File,
			Severity: strings.ToLower(g.Severity),
			Message:  g.Message,
		}
		if d.Severity == "" {
			d.Severity = "error"
		}
		if g.Range != nil {
			d.Line = g.Range.Start.Line + 1 // pyright is 0-based
			d.Col = g.Range.Start.Character + 1
		}
		out = append(out, d)
	}
	return out
}

// pythonDiagCmds picks the strongest available Python static check.
// Preference: pyright → mypy → ruff → compileall (stdlib fallback).
func pythonDiagCmds(dir string) (cmds, allowed []string) {
	allowed = []string{"python3", "python", "pyright", "mypy", "ruff", "npx"}
	if diagBinAvailable(dir, "pyright") || fileExists(filepath.Join(dir, "node_modules", ".bin", "pyright")) ||
		fileExists(filepath.Join(dir, "node_modules", ".bin", "pyright.cmd")) {
		return []string{"pyright --outputjson"}, allowed
	}
	if diagBinAvailable(dir, "mypy") || fileExists(filepath.Join(dir, ".venv", "bin", "mypy")) ||
		fileExists(filepath.Join(dir, ".venv", "Scripts", "mypy.exe")) {
		return []string{"mypy . --hide-error-context --no-error-summary --show-column-numbers"}, allowed
	}
	if diagBinAvailable(dir, "ruff") || fileExists(filepath.Join(dir, ".venv", "bin", "ruff")) ||
		fileExists(filepath.Join(dir, ".venv", "Scripts", "ruff.exe")) {
		return []string{"ruff check ."}, allowed
	}
	return []string{"python3 -m compileall -q ."}, allowed
}

// phpDiagCmds returns phpstan (with or without neon) or a bounded php -l sweep.
func phpDiagCmds(dir string) (cmds, allowed []string, ok bool) {
	allowed = []string{"phpstan", "php"}
	phpstan := filepath.Join(dir, "vendor", "bin", "phpstan")
	phpstanWin := phpstan + ".bat"
	hasStan := fileExists(phpstan) || fileExists(phpstanWin) || diagBinAvailable(dir, "phpstan")
	hasNeon := fileExists(filepath.Join(dir, "phpstan.neon")) ||
		fileExists(filepath.Join(dir, "phpstan.neon.dist"))
	hasComposer := fileExists(filepath.Join(dir, "composer.json"))
	if hasStan && (hasNeon || hasComposer) {
		return []string{"vendor/bin/phpstan analyse --no-progress --no-interaction --error-format=raw"}, allowed, true
	}
	if hasComposer || hasNeon {
		// No phpstan binary: lint a few common entrypoints with php -l.
		var targets []string
		for _, rel := range []string{"artisan", "public/index.php", "index.php", "src", "app"} {
			p := filepath.Join(dir, rel)
			if fileExists(p) || dirExists(p) {
				targets = append(targets, rel)
			}
		}
		if len(targets) == 0 {
			return []string{"php -l ."}, allowed, true
		}
		// php -l on a directory doesn't recurse; emit one command per file-ish
		// entry and let verify run them. Prefer artisan/index when present.
		var lines []string
		for _, t := range targets {
			if st, err := os.Stat(filepath.Join(dir, t)); err == nil && !st.IsDir() {
				lines = append(lines, "php -l "+filepath.ToSlash(t))
			}
		}
		if len(lines) == 0 {
			lines = []string{"php -l public/index.php"}
		}
		if len(lines) > 4 {
			lines = lines[:4]
		}
		return lines, allowed, true
	}
	return nil, nil, false
}

// nodeDiagCmds picks a static check for package.json-only (no tsconfig) Node repos.
// Preference: local/global eslint → node --check on a few entrypoints.
func nodeDiagCmds(dir string) (cmds, allowed []string, ok bool) {
	allowed = []string{"node", "npx", "eslint"}
	hasLocalEslint := fileExists(filepath.Join(dir, "node_modules", ".bin", "eslint")) ||
		fileExists(filepath.Join(dir, "node_modules", ".bin", "eslint.cmd"))
	hasEslintCfg := false
	for _, rel := range []string{
		".eslintrc", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc.json", ".eslintrc.yml", ".eslintrc.yaml",
		"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs", "eslint.config.ts",
	} {
		if fileExists(filepath.Join(dir, rel)) {
			hasEslintCfg = true
			break
		}
	}
	// Prefer project-local eslint (or an eslint config that implies the project wants it).
	// Do not use a bare global eslint LookPath alone — that false-positives on clean JS beds.
	if hasLocalEslint || (hasEslintCfg && (hasLocalEslint || diagBinAvailable(dir, "eslint"))) {
		return []string{"npx --no-install eslint . --max-warnings 0"}, allowed, true
	}
	// Syntax-only fallback: node --check on a few common entrypoints.
	var targets []string
	seen := map[string]bool{}
	add := func(rel string) {
		rel = filepath.ToSlash(strings.TrimSpace(rel))
		if rel == "" || seen[rel] {
			return
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if st, err := os.Stat(p); err == nil && !st.IsDir() && strings.HasSuffix(strings.ToLower(rel), ".js") {
			seen[rel] = true
			targets = append(targets, rel)
		}
	}
	// package.json "main" / "module" when present
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pj struct {
			Main   string `json:"main"`
			Module string `json:"module"`
		}
		if json.Unmarshal(b, &pj) == nil {
			add(pj.Main)
			add(pj.Module)
		}
	}
	for _, rel := range []string{"index.js", "app.js", "server.js", "lib/index.js", "lib/express.js", "src/index.js", "bin/www"} {
		add(rel)
		if len(targets) >= 4 {
			break
		}
	}
	if len(targets) == 0 {
		return nil, nil, false
	}
	if len(targets) > 4 {
		targets = targets[:4]
	}
	lines := make([]string, 0, len(targets))
	for _, t := range targets {
		lines = append(lines, "node --check "+t)
	}
	return lines, allowed, true
}

func diagBinAvailable(dir, name string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	// Local vendor / node_modules shims (common on Windows + Unix).
	candidates := []string{
		filepath.Join(dir, "vendor", "bin", name),
		filepath.Join(dir, "vendor", "bin", name+".bat"),
		filepath.Join(dir, "node_modules", ".bin", name),
		filepath.Join(dir, "node_modules", ".bin", name+".cmd"),
		filepath.Join(dir, ".venv", "bin", name),
		filepath.Join(dir, ".venv", "Scripts", name+".exe"),
	}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, filepath.Join(dir, "vendor", "bin", name+".phar"))
	}
	for _, c := range candidates {
		if fileExists(c) {
			return true
		}
	}
	return false
}

// generatedPathMarkers are path segments that usually mean build/generated output
// rather than hand-edited source. Diagnostics from these drown actionable errors.
var generatedPathMarkers = []string{
	"/.next/", "/.next\\",
	"/dist/", "/dist\\",
	"/build/", "/build\\",
	"/out/", "/out\\",
	"/tmp/", "/tmp\\",
	"/coverage/", "/coverage\\",
	"/.turbo/", "/.turbo\\",
	"/.parcel-cache/", "/.parcel-cache\\",
	"/.output/", "/.output\\",
	"/node_modules/", "/node_modules\\",
	"/vendor/", "/vendor\\",
	"/__generated__/", "/__generated__\\",
	"/.svelte-kit/", "/.svelte-kit\\",
	"/storybook-static/",
	"/.vercel/",
	"/.netlify/",
	"/.angular/",
	"/.dart_tool/",
	"/.nyc_output/",
}

// isGeneratedDiagnosticPath reports whether file looks like generated/build output.
func isGeneratedDiagnosticPath(file string) bool {
	f := filepath.ToSlash(strings.ToLower(file))
	if !strings.HasPrefix(f, "/") {
		f = "/" + f
	}
	if !strings.HasSuffix(f, "/") {
		// keep as path for substring checks
	}
	for _, m := range generatedPathMarkers {
		marker := strings.ToLower(filepath.ToSlash(m))
		if strings.Contains(f, marker) {
			return true
		}
	}
	// Also catch leading .next/ without a slash prefix in relative paths.
	base := strings.TrimPrefix(f, "/")
	for _, prefix := range []string{".next/", "dist/", "build/", "out/", "coverage/", "node_modules/", ".turbo/", ".output/", ".svelte-kit/", "storybook-static/"} {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

// bucketDiagnostics splits problems into actionable (source) vs generated noise.
// Actionable is returned first so agents fix real errors before drowning in .next.
func bucketDiagnostics(in []diagnostic) (actionable, generated []diagnostic) {
	for _, d := range in {
		if isGeneratedDiagnosticPath(d.File) {
			generated = append(generated, d)
		} else {
			actionable = append(actionable, d)
		}
	}
	return actionable, generated
}
