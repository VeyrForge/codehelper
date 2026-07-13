package ops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/verify"
)

// CIStatusResult is read-only GitHub/CI summary.
type CIStatusResult struct {
	Repo      string `json:"repo,omitempty"`
	PR        string `json:"pr_summary,omitempty"`
	Runs      string `json:"workflow_runs,omitempty"`
	Error     string `json:"error,omitempty"`
	GHMissing bool   `json:"gh_missing,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
}

var ghRepoRe = regexp.MustCompile(`github\.com[:/]([^/]+/[^/.]+)`)

// CIStatus runs read-only gh commands when GitHub policy is configured.
func CIStatus(ctx context.Context, repoRoot string) (*CIStatusResult, error) {
	cfg, err := connections.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	gh := cfg.Policy.GitHub
	if gh == nil || gh.Disabled {
		return &CIStatusResult{Disabled: true, Error: "github integration not configured — set via `codehelper connections policy set --github-repo owner/name --github-token-ref env:GITHUB_TOKEN`"}, nil
	}
	if err := ValidateGitHubTokenRef(gh.TokenRef); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return &CIStatusResult{GHMissing: true, Error: "gh CLI not found on PATH"}, nil
	}
	repo := strings.TrimSpace(gh.Repo)
	if repo == "" {
		repo, _ = gitHubRepoFromRemote(ctx, repoRoot)
	}
	out := &CIStatusResult{Repo: repo}
	env := os.Environ()
	if tokRef := strings.TrimSpace(gh.TokenRef); tokRef != "" {
		tok, err := ResolveRef(repoRoot, tokRef, "github")
		if err != nil {
			return nil, err
		}
		if tok != "" {
			env = append(env, "GH_TOKEN="+tok, "GITHUB_TOKEN="+tok)
		}
	}
	if repo != "" {
		if s, err := runGH(ctx, env, repoRoot, "pr", "status", "-R", repo); err == nil {
			out.PR = trimOut(s)
		}
		if s, err := runGH(ctx, env, repoRoot, "run", "list", "-R", repo, "-L", "5"); err == nil {
			out.Runs = trimOut(s)
		}
	} else {
		out.Error = "could not detect github repo — set policy github.repo"
	}
	return out, nil
}

func runGH(ctx context.Context, env []string, dir string, argv ...string) (string, error) {
	if blocked, reason := verify.CommandBlocked(argv); blocked {
		return "", fmt.Errorf("%s", reason)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), err
	}
	return buf.String(), nil
}

func gitHubRepoFromRemote(ctx context.Context, repoRoot string) (string, error) {
	argv := []string{"git", "remote", "get-url", "origin"}
	if blocked, reason := verify.CommandBlocked(argv); blocked {
		return "", fmt.Errorf("%s", reason)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "remote", "get-url", "origin")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	if m := ghRepoRe.FindStringSubmatch(string(out)); len(m) > 1 {
		return strings.TrimSuffix(m[1], ".git"), nil
	}
	return "", fmt.Errorf("origin is not a github remote")
}

func trimOut(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 8000 {
		s = s[:8000] + "\n…(truncated)"
	}
	return s
}
