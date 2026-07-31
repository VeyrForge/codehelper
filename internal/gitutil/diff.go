package gitutil

import (
	"bytes"
	"os/exec"
	"strings"
)

// UnifiedDiff returns git diff text for working tree vs baseRef.
func UnifiedDiff(repoRoot, baseRef string) (string, error) {
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD~1"
	}
	if err := validateRef(baseRef); err != nil {
		return "", err
	}
	cmd := exec.Command("git", "-C", repoRoot, "diff", baseRef)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}
