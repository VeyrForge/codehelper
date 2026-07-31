package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/spf13/cobra"
)

// codehelperHookMarker identifies hooks we manage, so install is idempotent and
// we never clobber a user's existing hook.
const codehelperHookMarker = "# >>> codehelper managed hook >>>"

// hookBody runs an incremental reindex in the background so commits/merges stay
// fast. It is a no-op when codehelper isn't on PATH.
const hookBody = codehelperHookMarker + `
# Keeps the codehelper index fresh after history changes (git gives the changed
# set for free). Runs detached so it never slows the git operation.
if command -v codehelper >/dev/null 2>&1; then
  (codehelper analyze >/dev/null 2>&1 &)
fi
# <<< codehelper managed hook <<<
`

// hooksToInstall are the git hooks where a reindex is worthwhile: a commit, a
// merge, and a branch checkout all change which code is current.
var hooksToInstall = []string{"post-commit", "post-merge", "post-checkout"}

func hooksCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hooks",
		Short: "Manage git hooks that keep the codehelper index fresh",
	}
	c.AddCommand(hooksInstallCmd(), hooksUninstallCmd())
	return c
}

func hooksInstallCmd() *cobra.Command {
	var pathArg string
	c := &cobra.Command{
		Use:   "install [path]",
		Short: "Install git hooks (post-commit/-merge/-checkout) that reindex on history changes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := firstNonEmpty(pathArg, argOrCwd(args))
			hooksDir, err := gitHooksDir(root)
			if err != nil {
				return err
			}
			for _, name := range hooksToInstall {
				if err := installOneHook(hooksDir, name); err != nil {
					return err
				}
			}
			fmt.Printf("installed codehelper reindex hooks in %s: %s\n", hooksDir, strings.Join(hooksToInstall, ", "))
			return nil
		},
	}
	c.Flags().StringVar(&pathArg, "path", "", "repository path (default: current directory)")
	return c
}

func hooksUninstallCmd() *cobra.Command {
	var pathArg string
	c := &cobra.Command{
		Use:   "uninstall [path]",
		Short: "Remove codehelper-managed git hooks",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := firstNonEmpty(pathArg, argOrCwd(args))
			hooksDir, err := gitHooksDir(root)
			if err != nil {
				return err
			}
			removed := 0
			for _, name := range hooksToInstall {
				p := filepath.Join(hooksDir, name)
				b, rerr := os.ReadFile(p)
				if rerr != nil || !strings.Contains(string(b), codehelperHookMarker) {
					continue // not ours / absent — leave it alone
				}
				if err := os.Remove(p); err != nil {
					return err
				}
				removed++
			}
			fmt.Printf("removed %d codehelper-managed hook(s) from %s\n", removed, hooksDir)
			return nil
		},
	}
	c.Flags().StringVar(&pathArg, "path", "", "repository path (default: current directory)")
	return c
}

// installOneHook writes (or refuses to clobber) a single git hook.
func installOneHook(hooksDir, name string) error {
	p := filepath.Join(hooksDir, name)
	if existing, err := os.ReadFile(p); err == nil {
		if strings.Contains(string(existing), codehelperHookMarker) {
			return nil // already installed — idempotent
		}
		// A foreign hook exists: append our block rather than overwrite it.
		merged := strings.TrimRight(string(existing), "\n") + "\n\n" + hookBody
		return os.WriteFile(p, []byte(merged), 0o755)
	}
	return os.WriteFile(p, []byte("#!/bin/sh\n"+hookBody), 0o755)
}

// gitHooksDir resolves the .git/hooks directory for the repo containing root.
func gitHooksDir(root string) (string, error) {
	gitRoot, err := gitutil.FindGitRoot(root)
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	dir := filepath.Join(gitRoot, ".git", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func argOrCwd(args []string) string {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return args[0]
	}
	wd, _ := os.Getwd()
	return wd
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
