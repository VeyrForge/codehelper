package gitutil

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// validateRef rejects a base ref that git could interpret as an option rather
// than a revision (git option injection). base_ref arrives from an MCP/LLM
// argument, so a value like "--output=/etc/x" must never reach `git diff <ref>`.
// Real refs (HEAD, HEAD~1, SHAs, branch/tag names) never start with '-' and
// never contain control characters.
func validateRef(ref string) error {
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("gitutil: refusing ref %q (must not start with '-')", ref)
	}
	if strings.ContainsAny(ref, "\x00\n\r") {
		return fmt.Errorf("gitutil: refusing ref %q (control characters)", ref)
	}
	return nil
}

// UnbornHEAD is returned by HeadCommit when the repo is initialized but has no
// commits yet (git's "unborn" HEAD). Incremental git diff against this sentinel
// is invalid; callers should treat it like an empty base and do a full index.
const UnbornHEAD = "0000000000000000000000000000000000000000"

// IsUnbornCommit reports whether sha is the sentinel for a repo without commits.
func IsUnbornCommit(sha string) bool {
	return sha == UnbornHEAD
}

// HeadCommit returns the current HEAD sha. For a git repo with no commits yet it
// returns UnbornHEAD and a nil error so indexing can proceed on the working tree.
func HeadCommit(repoRoot string) (string, error) {
	if !IsGitRepo(repoRoot) {
		return "", fmt.Errorf("gitutil: not a git repository: %s", repoRoot)
	}
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		// `git init` with no commits: HEAD exists but does not resolve.
		return UnbornHEAD, nil
	}
	return strings.TrimSpace(out.String()), nil
}

// IsGitRepo returns true if .git exists under root.
func IsGitRepo(root string) bool {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false
	}
	return strings.TrimSpace(out.String()) == "true"
}

// UntrackedFiles lists files git is not tracking and that are not gitignored
// (new files the symbol index has never seen). Returns forward-slashed paths.
func UntrackedFiles(repoRoot string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoRoot, "ls-files", "--others", "--exclude-standard")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(out.String())
	if raw == "" {
		return nil, nil
	}
	lines := strings.Split(raw, "\n")
	for i := range lines {
		lines[i] = filepath.ToSlash(strings.TrimSpace(lines[i]))
	}
	return lines, nil
}

// ChangedFiles returns paths changed vs HEAD~1 or working tree vs HEAD.
func ChangedFiles(repoRoot string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--name-only", "HEAD")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	for i := range lines {
		lines[i] = filepath.ToSlash(lines[i])
	}
	return lines, nil
}

// DiffNameStatus returns changed files with status (for detect_changes).
// DiffAgainst lists files changed between baseRef and the working tree (including staged).
func DiffAgainst(repoRoot, baseRef string) ([]string, error) {
	if err := validateRef(baseRef); err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--name-only", baseRef)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(out.String())
	if raw == "" {
		return nil, nil
	}
	lines := strings.Split(raw, "\n")
	for i := range lines {
		lines[i] = filepath.ToSlash(lines[i])
	}
	return lines, nil
}

// LogNameOnly returns, for each of the last maxCommits non-merge commits, the list
// of files that commit changed (newest first). Used to mine evolutionary coupling
// ("files that change together") — a signal the call graph cannot see. Best-effort:
// returns nil on any git error (not a repo, shallow clone) so callers degrade
// silently rather than failing the tool.
func LogNameOnly(repoRoot string, maxCommits int) ([][]string, error) {
	if maxCommits <= 0 {
		maxCommits = 2000
	}
	cmd := exec.Command("git", "-C", repoRoot, "log", "--no-merges", "--name-only",
		"--pretty=format:%x00", fmt.Sprintf("-n%d", maxCommits))
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var commits [][]string
	// Each commit's files are preceded by a NUL marker line from --pretty.
	for _, block := range strings.Split(out.String(), "\x00") {
		var files []string
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				files = append(files, filepath.ToSlash(line))
			}
		}
		if len(files) > 0 {
			commits = append(commits, files)
		}
	}
	return commits, nil
}

func DiffNameStatus(repoRoot string, baseRef string) ([]string, error) {
	if baseRef == "" {
		baseRef = "HEAD"
	}
	if err := validateRef(baseRef); err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--name-status", baseRef)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(out.String())
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}
