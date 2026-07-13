package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/spf13/cobra"
)

// Version is set by legacy build scripts via -ldflags "-X main.Version=...".
// Prefer editing repo-root VERSION and rebuilding; see `codehelper version set`.
var Version string

func init() {
	if strings.TrimSpace(Version) != "" {
		version.SetBuildVersion(Version)
	}
}

func versionCmd() *cobra.Command {
	var exePath bool
	var full bool
	c := &cobra.Command{
		Use:   "version",
		Short: "Print codehelper version",
		Run: func(cmd *cobra.Command, args []string) {
			if exePath {
				printExecutablePath()
				return
			}
			if full {
				fmt.Println("version:", version.Current())
				if fileVer, err := versionFileNearCwd(); err == nil && fileVer != "" {
					fmt.Println("VERSION file:", fileVer)
				}
				printExecutablePathLine()
				return
			}
			fmt.Println(version.Current())
		},
	}
	c.Flags().BoolVar(&exePath, "path", false, "print the resolved filesystem path of this codehelper binary (useful when PATH has multiple installs)")
	c.Flags().BoolVar(&full, "full", false, "print embedded version and executable path")
	c.AddCommand(versionSetCmd())
	return c
}

func versionSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <version>",
		Short: "Write repo-root VERSION (then rebuild: npm run build:go or codehelper update)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ver := strings.TrimSpace(args[0])
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if repoRoot, err := gitutil.FindGitRoot(root); err == nil {
				root = repoRoot
			}
			if err := version.WriteToDir(root, ver); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "wrote %s\n", filepath.Join(root, "VERSION"))
			fmt.Fprintf(os.Stderr, "embedded version is still %s until you rebuild\n", version.Current())
			return nil
		},
	}
}

func versionFileNearCwd() (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if repoRoot, err := gitutil.FindGitRoot(root); err == nil {
		root = repoRoot
	}
	return version.ReadFromDir(root)
}

func printExecutablePath() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Println("(could not resolve executable:", err.Error()+")")
		return
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	fmt.Println(exe)
}

func printExecutablePathLine() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Println("executable:", "(unknown:", err.Error()+")")
		return
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	fmt.Println("executable:", exe)
}
