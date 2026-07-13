package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/daemon"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/spf13/cobra"
)

type doctorReport struct {
	Healthy         bool     `json:"healthy"`
	EmbeddedVersion string   `json:"embedded_version,omitempty"`
	Executable      string   `json:"executable,omitempty"`
	Issues          []string `json:"issues,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	Checks          []string `json:"checks,omitempty"`
}

func doctorCmd() *cobra.Command {
	var asJSON bool
	var strict bool
	c := &cobra.Command{
		Use:   "doctor [path]",
		Short: "Run environment and index health diagnostics",
		Long:  doctorLongHelp,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return err
			}

			rep := &doctorReport{Healthy: true, EmbeddedVersion: version.Current()}
			if exe, xerr := os.Executable(); xerr == nil {
				if abs, xerr := filepath.Abs(exe); xerr == nil {
					exe = abs
				}
				if resolved, xerr := filepath.EvalSymlinks(exe); xerr == nil {
					exe = resolved
				}
				rep.Executable = exe
				rep.Checks = append(rep.Checks, "executable")
			}
			fresh := freshness.Inspect(root)
			rep.Checks = append(rep.Checks, "freshness")
			if fresh.Stale {
				rep.Healthy = false
				rep.Issues = append(rep.Issues, "index stale: "+fresh.StaleReason)
			}
			if !fresh.WatchRunning {
				rep.Warnings = append(rep.Warnings, "watch daemon is not running; consider `codehelper watch --daemon`")
			}

			rep.Checks = append(rep.Checks, "meta")
			m, mErr := meta.Read(root)
			if mErr != nil {
				rep.Healthy = false
				rep.Issues = append(rep.Issues, "meta missing or unreadable: "+mErr.Error())
			} else {
				if m.FileCount == 0 || m.SymbolCount == 0 {
					rep.Healthy = false
					rep.Issues = append(rep.Issues, "index appears empty; run `codehelper analyze --force`")
				}
			}

			rep.Checks = append(rep.Checks, "graph_counts_match_meta")
			if mErr == nil && m != nil && m.SymbolCount > 0 {
				repoID := strings.TrimSpace(m.RepoName)
				if repoID == "" {
					repoID = filepath.Base(root)
				}
				st, gerr := graph.Open(paths.DBPath(root))
				if gerr != nil {
					rep.Warnings = append(rep.Warnings, "graph db unreadable: "+gerr.Error())
				} else {
					dbSyms, _, _, cerr := st.Counts(context.Background(), repoID)
					st.Close()
					if cerr != nil {
						rep.Warnings = append(rep.Warnings, "graph counts failed: "+cerr.Error())
					} else {
						drift := m.SymbolCount - dbSyms
						if drift < 0 {
							drift = -drift
						}
						threshold := 50
						if m.SymbolCount > 0 && drift*100/m.SymbolCount > 5 {
							rep.Healthy = false
							rep.Issues = append(rep.Issues,
								fmt.Sprintf("meta symbol_count=%d but graph has %d; run `codehelper analyze --force`", m.SymbolCount, dbSyms))
						} else if drift > threshold {
							rep.Healthy = false
							rep.Issues = append(rep.Issues,
								fmt.Sprintf("meta symbol_count=%d but graph has %d (drift %d); run `codehelper analyze --force`", m.SymbolCount, dbSyms, drift))
						}
					}
				}
			}

			rep.Checks = append(rep.Checks, "registry")
			reg, regErr := registry.Load()
			if regErr != nil {
				rep.Warnings = append(rep.Warnings, "registry unavailable: "+regErr.Error())
			} else {
				found := false
				for _, e := range reg.List() {
					if filepath.Clean(e.RootPath) == filepath.Clean(root) {
						found = true
						break
					}
				}
				if !found {
					rep.Warnings = append(rep.Warnings, "repo not present in registry; run `codehelper analyze`")
				}
			}

			rep.Checks = append(rep.Checks, "watch_state")
			if st, err := daemon.ReadState(root); err == nil && st != nil {
				if strings.TrimSpace(st.Status) == "" {
					rep.Warnings = append(rep.Warnings, "watch state status is empty")
				}
			}

			if asJSON {
				b, _ := json.MarshalIndent(rep, "", "  ")
				fmt.Println(string(b))
			} else {
				if rep.Executable != "" {
					fmt.Println("  executable:", rep.Executable)
				}
				fmt.Println("  embedded version:", rep.EmbeddedVersion)
				if rep.Healthy {
					fmt.Println("doctor: healthy")
				} else {
					fmt.Println("doctor: issues found")
				}
				for _, s := range rep.Issues {
					fmt.Println("  ISSUE:", s)
				}
				for _, s := range rep.Warnings {
					fmt.Println("  WARN :", s)
				}
				fmt.Println("  checks:", strings.Join(rep.Checks, ", "))
			}

			if strict && (!rep.Healthy || len(rep.Warnings) > 0) {
				return fmt.Errorf("doctor strict mode failed")
			}
			if !rep.Healthy {
				return fmt.Errorf("doctor found issues")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable doctor report")
	c.Flags().BoolVar(&strict, "strict", false, "fail on warnings as well as issues")
	return c
}
