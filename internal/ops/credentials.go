// Package ops implements security-gated external operations (logs, SSH recipes,
// read-only DB, aliases, env detection, CI status) driven by connections profiles.
// Secrets never flow through MCP responses — only env:/secret refs at connect time.
package ops

import (
	"fmt"
	"os"
	"strings"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/secrets"
)

const maxLogLines = 1000
const maxLogBytes = 256 * 1024
const maxDBRows = 100
const maxDBBytes = 128 * 1024

// ResolveRef resolves env:VAR or secret store refs for local process use only.
func ResolveRef(repoRoot, ref, secretName string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if strings.HasPrefix(ref, "env:") {
		v := strings.TrimSpace(strings.TrimPrefix(ref, "env:"))
		if v == "" {
			return "", fmt.Errorf("empty env ref")
		}
		return os.Getenv(v), nil
	}
	if strings.EqualFold(ref, connections.SecretRef) {
		pt, ok, err := secrets.Get(repoRoot, secretName)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("secret %q not in store", secretName)
		}
		return pt, nil
	}
	return "", fmt.Errorf("unsupported secret ref scheme")
}

// ValidateGitHubTokenRef ensures token refs are env-only.
func ValidateGitHubTokenRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "env:") && strings.TrimSpace(strings.TrimPrefix(ref, "env:")) != "" {
		return nil
	}
	return fmt.Errorf("github token_ref must be env:VAR — never inline")
}
