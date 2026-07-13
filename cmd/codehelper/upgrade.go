package main

import (
	"os"
	"path/filepath"

	"github.com/VeyrForge/codehelper/internal/ghrelease"
	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/spf13/cobra"
)

func upgradeCmd() *cobra.Command {
	var repo string
	var tag string
	var force bool
	c := &cobra.Command{
		Use:   "upgrade",
		Short: "Install the latest official release from GitHub (no Go or compiler needed)",
		Long: "Downloads the published archive for this OS/architecture (see .goreleaser.yaml), " +
			"verifies checksums.txt when the release includes it, and replaces this codehelper executable.\n\n" +
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
				GitHubRepo:     repo,
				Tag:            tag,
				CurrentVersion: version.Current(),
				Force:          force,
				GitHubToken:    os.Getenv("GITHUB_TOKEN"),
				UserAgent:      "codehelper/" + version.Current(),
			})
		},
	}
	c.Flags().StringVar(&repo, "repo", ghrelease.DefaultRepo, "GitHub repository owner/name")
	c.Flags().StringVar(&tag, "tag", "latest", `release tag (e.g. v2.4.1) or "latest"`)
	c.Flags().BoolVar(&force, "force", false, "re-download even if the embedded version already matches")
	return c
}
