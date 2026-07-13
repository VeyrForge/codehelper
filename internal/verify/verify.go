package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ExecMode controls how a command string is dispatched to the OS.
//
// argv (default): split with shlex (POSIX-ish quoting) and run via exec
// without invoking a shell. This avoids shell metacharacter injection
// (per OWASP "OS Command Injection Defense" guidance), at the cost of
// not supporting pipes/redirection in the command line.
//
// shell: explicit opt-in to running the command line through `sh -lc`
// (or `cmd /C` on Windows). Required only when the lint/build/test
// command relies on shell features like pipes or env var expansion.
type ExecMode string

const (
	// ExecArgv is the default secure mode (no shell).
	ExecArgv ExecMode = "argv"
	// ExecShell wraps the command in a shell. Opt-in only.
	ExecShell ExecMode = "shell"
)

// Request is input to the verify tool.
type Request struct {
	RepoRoot     string   `json:"repo_root"`
	LintCmd      string   `json:"lint_cmd,omitempty"`
	BuildCmd     string   `json:"build_cmd,omitempty"`
	TestCmd      string   `json:"test_cmd,omitempty"`
	PatchUnified string   `json:"patch_unified,omitempty"`
	Threshold    float64  `json:"threshold,omitempty"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	// ExecMode selects argv (default) or shell dispatch.
	ExecMode ExecMode `json:"exec_mode,omitempty"`
	// AllowedCommands optionally restricts the executable basename allowed
	// in argv mode (e.g. ["go","npm","pnpm","make","python"]). Empty means
	// allow any basename not on the deny-list.
	AllowedCommands []string `json:"allowed_commands,omitempty"`
	// BlockPolicy optional per-project overrides (e.g. AllowGit).
	BlockPolicy BlockPolicy `json:"-"`
	// TimeoutSeconds caps per-command runtime. 0 means use DefaultTimeout.
	TimeoutSeconds int   `json:"timeout_seconds,omitempty"`
	Judge          Judge `json:"-"`
}

// ExecStat captures structured telemetry for a single sub-process.
type ExecStat struct {
	Cmd       string  `json:"cmd"`
	DurationS float64 `json:"duration_s"`
	ExitCode  int     `json:"exit_code"`
	TimedOut  bool    `json:"timed_out,omitempty"`
	Mode      string  `json:"mode"`
}

// Result is structured verification output.
type Result struct {
	Accepted    bool       `json:"accepted"`
	Confidence  float64    `json:"confidence"`
	Reasons     []string   `json:"reasons"`
	LintOutput  string     `json:"lint_output,omitempty"`
	BuildOutput string     `json:"build_output,omitempty"`
	TestOutput  string     `json:"test_output,omitempty"`
	Abstain     bool       `json:"abstain,omitempty"`
	Stats       []ExecStat `json:"stats,omitempty"`
	ExecMode    string     `json:"exec_mode,omitempty"`
}

const wBuild, wTest, wLint = 0.45, 0.35, 0.20

// DefaultTimeout caps a single sub-process when none is supplied.
const DefaultTimeout = 5 * time.Minute

// MaxTimeout protects against absurdly long timeouts.
const MaxTimeout = 30 * time.Minute

// Run executes deterministic gates with weighted confidence; optional Judge hook (no default network).
func Run(ctx context.Context, req Request) (*Result, error) {
	if req.Threshold <= 0 {
		req.Threshold = 0.65
	}
	mode := req.ExecMode
	if mode == "" {
		mode = ExecArgv
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	res := &Result{Accepted: true, Confidence: 0.0, Reasons: []string{}, ExecMode: string(mode)}
	baseScore := 0.0
	gate := func(label, cmdline string, weight float64, partialOnFail float64) (string, error) {
		out, stat, err := runCommand(ctx, req.RepoRoot, cmdline, mode, req.AllowedCommands, req.BlockPolicy, timeout)
		stat.Cmd = label
		res.Stats = append(res.Stats, stat)
		if err != nil {
			res.Accepted = false
			baseScore += weight * partialOnFail
			res.Reasons = append(res.Reasons, label+" failed: "+err.Error())
			return out, err
		}
		baseScore += weight * 1.0
		return out, nil
	}
	if req.BuildCmd != "" {
		out, _ := gate("build", req.BuildCmd, wBuild, 0.0)
		res.BuildOutput = out
	}
	if req.TestCmd != "" {
		out, _ := gate("tests", req.TestCmd, wTest, 0.0)
		res.TestOutput = out
	}
	if req.LintCmd != "" {
		out, _ := gate("lint", req.LintCmd, wLint, 0.4)
		res.LintOutput = out
	}
	if req.PatchUnified != "" {
		lines := strings.Count(req.PatchUnified, "\n")
		if lines > 500 {
			res.Reasons = append(res.Reasons, "large patch: manual review suggested")
		}
	}
	cmdSignal := req.LintCmd != "" || req.BuildCmd != "" || req.TestCmd != ""
	if len(req.ChangedPaths) > 0 {
		res.Reasons = append(res.Reasons, targetedTestHint(req.TestCmd, req.ChangedPaths))
	}
	if !cmdSignal {
		res.Abstain = true
		res.Accepted = false
		res.Confidence = 0.0
		res.Reasons = append(res.Reasons, "abstain: no lint/build/test signal")
		return res, nil
	}
	present := 0.0
	if req.BuildCmd != "" {
		present += wBuild
	}
	if req.TestCmd != "" {
		present += wTest
	}
	if req.LintCmd != "" {
		present += wLint
	}
	scale := present
	if scale <= 0 {
		scale = 1
	}
	res.Confidence = baseScore / scale
	if req.Judge != nil {
		if c, rs, err := req.Judge.Score(ctx, req, res); err == nil && c >= 0 {
			res.Confidence = (res.Confidence*0.7 + c*0.3)
			res.Reasons = append(res.Reasons, rs...)
		}
	}
	if res.Confidence < req.Threshold {
		res.Accepted = false
		res.Reasons = append(res.Reasons, "confidence below threshold")
	}
	return res, nil
}

func targetedTestHint(testCmd string, paths []string) string {
	if testCmd == "" || len(paths) == 0 {
		return "changed paths recorded for review"
	}
	return "changed paths: " + strings.Join(paths, ", ") + " — narrow " + testCmd + " to affected packages when supported"
}

// ErrCommandNotAllowed is returned when an argv-mode command's executable
// basename is not in AllowedCommands.
var ErrCommandNotAllowed = errors.New("command not in allowlist")

// ErrCommandBlocked is returned when a command is rejected by the safety policy
// (shell-injection operators, command substitution, or destructive patterns).
var ErrCommandBlocked = errors.New("command blocked by safety policy")

// ErrEmptyCommand is returned when a non-empty cmdline parses to zero argv.
var ErrEmptyCommand = errors.New("empty command")

func runCommand(ctx context.Context, dir, cmdline string, mode ExecMode, allow []string, blockPol BlockPolicy, timeout time.Duration) (string, ExecStat, error) {
	stat := ExecStat{Mode: string(mode)}
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" {
		return "", stat, nil
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t0 := time.Now()
	var c *exec.Cmd
	switch mode {
	case ExecShell:
		c = newShellCmd(cctx, cmdline)
	default:
		argv, err := splitArgv(cmdline)
		if err != nil {
			stat.DurationS = time.Since(t0).Seconds()
			stat.ExitCode = -1
			return "", stat, fmt.Errorf("parse: %w", err)
		}
		if len(argv) == 0 {
			stat.DurationS = time.Since(t0).Seconds()
			stat.ExitCode = -1
			return "", stat, ErrEmptyCommand
		}
		// Defense-in-depth: reject shell-injection operators and destructive
		// patterns up front, with a clear reason, rather than relying on the
		// target binary to choke on the malformed argv.
		if blocked, reason := CommandBlockedWithPolicy(argv, blockPol); blocked {
			stat.DurationS = time.Since(t0).Seconds()
			stat.ExitCode = -1
			return "", stat, fmt.Errorf("%w: %s", ErrCommandBlocked, reason)
		}
		if len(allow) > 0 && !allowed(argv[0], allow) {
			stat.DurationS = time.Since(t0).Seconds()
			stat.ExitCode = -1
			return "", stat, fmt.Errorf("%w: %s", ErrCommandNotAllowed, argv[0])
		}
		c = exec.CommandContext(cctx, argv[0], argv[1:]...)
	}
	c.Dir = dir
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	stat.DurationS = time.Since(t0).Seconds()
	if c.ProcessState != nil {
		stat.ExitCode = c.ProcessState.ExitCode()
	}
	if cctx.Err() == context.DeadlineExceeded {
		stat.TimedOut = true
		if err == nil {
			err = context.DeadlineExceeded
		}
	}
	return buf.String(), stat, err
}

func allowed(cmd string, allow []string) bool {
	base := commandBasename(cmd)
	for _, a := range allow {
		if strings.EqualFold(strings.TrimSpace(a), base) {
			return true
		}
	}
	return false
}

func commandBasename(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if i := strings.LastIndexAny(cmd, `/\`); i >= 0 {
		cmd = cmd[i+1:]
	}
	return cmd
}

// ResultJSON serializes result.
func ResultJSON(r *Result) ([]byte, error) {
	if r != nil && r.Reasons == nil {
		r.Reasons = []string{}
	}
	return json.MarshalIndent(r, "", "  ")
}
