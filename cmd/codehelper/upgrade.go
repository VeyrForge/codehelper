package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/ghrelease"
	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/spf13/cobra"
)

func upgradeCmd() *cobra.Command {
	var repo string
	var tag string
	var force bool
	var allowUnverified bool
	c := &cobra.Command{
		Use:   "upgrade",
		Short: "Install the latest official release from GitHub (no Go or compiler needed)",
		Long: "Downloads the published archive for this OS/architecture (see .goreleaser.yaml), " +
			"verifies checksums.txt (fail-closed if missing or incomplete), and replaces this codehelper executable.\n\n" +
			"Default release repo: **VeyrForge/codehelper**. Private/dev builds can override with " +
			"`--repo VeyrForge/codehelper` or env **CODEHELPER_UPGRADE_REPO**.\n\n" +
			"Checksum verification is mandatory. Pass **--allow-unverified** only to opt out when a " +
			"release lacks checksums.txt (not recommended).\n\n" +
			"Requires a GitHub release built by CI (git tag v*). Optional **GITHUB_TOKEN** improves API limits.\n\n" +
			"On Windows, if the exe was locked, replacement may be deferred — open a **new** terminal and run `codehelper version`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			exe, err = filepath.Abs(exe)
			if err != nil {
				return err
			}
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				exe = resolved
			}
			return ghrelease.Upgrade(exe, replaceRunningBinary, ghrelease.Options{
				GitHubRepo:      strings.TrimSpace(repo),
				Tag:             tag,
				CurrentVersion:  version.Current(),
				Force:           force,
				AllowUnverified: allowUnverified,
				GitHubToken:     os.Getenv("GITHUB_TOKEN"),
				UserAgent:       "codehelper/" + version.Current(),
			})
		},
	}
	c.Flags().StringVar(&repo, "repo", "", "GitHub repository owner/name (default VeyrForge/codehelper; or CODEHELPER_UPGRADE_REPO)")
	c.Flags().StringVar(&tag, "tag", "latest", `release tag (e.g. v2.4.1) or "latest"`)
	c.Flags().BoolVar(&force, "force", false, "re-download even if the embedded version already matches")
	c.Flags().BoolVar(&allowUnverified, "allow-unverified", false, "allow install when checksums.txt is missing or lacks this archive (unsafe)")
	return c
}
