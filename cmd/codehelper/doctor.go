package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/connections"
	"github.com/VeyrForge/codehelper/internal/daemon"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/projcfg"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/setup"
	"github.com/VeyrForge/codehelper/internal/setupsuggest"
	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/VeyrForge/codehelper/internal/web"
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
			var st *graph.Store
			if mErr == nil && m != nil && m.SymbolCount > 0 {
				repoID := strings.TrimSpace(m.RepoName)
				if repoID == "" {
					repoID = filepath.Base(root)
				}
				var gerr error
				st, gerr = graph.Open(paths.DBPath(root))
				if gerr != nil {
					rep.Warnings = append(rep.Warnings, "graph db unreadable: "+gerr.Error())
				} else {
					dbSyms, dbEdges, _, cerr := st.Counts(context.Background(), repoID)
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
						// Contains-only nuance: every edge is essentially file→symbol
						// contains (edge_count ≈ symbol_count) → no blast-radius graph.
						if dbSyms > 20 && dbEdges > 0 && dbEdges <= dbSyms {
							rep.Warnings = append(rep.Warnings, fmt.Sprintf(
								"graph looks contains-only (edge_count=%d ≈ symbol_count=%d) — call/import fanout is missing; MCP impact/context will be thin until parsers emit denser edges",
								dbEdges, dbSyms))
						}
					}
				}
			}

			rep.Checks = append(rep.Checks, "primary_language_graph")
			if st != nil {
				repoID := ""
				if m != nil {
					repoID = strings.TrimSpace(m.RepoName)
				}
				if repoID == "" {
					repoID = filepath.Base(root)
				}
				warnPrimaryLanguageGraph(rep, st, root, repoID)
				st.Close()
				st = nil
			} else if mErr == nil && m != nil {
				// Empty or unreadable graph already warned above; still try when meta exists.
				repoID := strings.TrimSpace(m.RepoName)
				if repoID == "" {
					repoID = filepath.Base(root)
				}
				if gst, gerr := graph.Open(paths.DBPath(root)); gerr == nil {
					warnPrimaryLanguageGraph(rep, gst, root, repoID)
					gst.Close()
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

			rep.Checks = append(rep.Checks, "browser_tier")
			if !web.BrowserAvailable() {
				rep.Warnings = append(rep.Warnings,
					"browser tier not compiled into this binary (rebuild with codehelper update / install.sh; default includes -tags rod)")
			}

			rep.Checks = append(rep.Checks, "setup_suggestions")
			var setupText string
			if prof, perr := profile.ReadOrGenerate(root); perr == nil && prof != nil {
				conn, _ := connections.Load(root)
				pcfg, _ := projcfg.Load(root)
				sug := setupsuggest.Build(setupsuggest.Input{
					RepoRoot:    root,
					ProjectType: prof.ProjectType,
					Framework:   prof.Framework,
					Connections: conn,
					Projcfg:     pcfg,
					IncludeMCP:  true,
					BinaryHint:  setup.ResolveBinary(),
				})
				setupText = setupsuggest.FormatText(sug)
			}

			if asJSON {
				b, _ := json.MarshalIndent(rep, "", "  ")
				fmt.Println(string(b))
				if setupText != "" {
					fmt.Fprintln(os.Stderr, setupText)
				}
			} else {
				if rep.Executable != "" {
					fmt.Println("  executable:", rep.Executable)
				}
				fmt.Println("  embedded version:", rep.EmbeddedVersion)
				switch {
				case !rep.Healthy:
					fmt.Println("doctor: issues found")
				case hasGraphQualityWarning(rep.Warnings):
					// Inventory-only / sparse graphs must not be labeled "healthy".
					fmt.Println("doctor: warnings (graph quality — index present but not MCP-ready)")
				case len(rep.Warnings) > 0:
					fmt.Println("doctor: healthy (with warnings)")
				default:
					fmt.Println("doctor: healthy")
				}
				for _, s := range rep.Issues {
					fmt.Println("  ISSUE:", s)
				}
				for _, s := range rep.Warnings {
					fmt.Println("  WARN :", s)
				}
				fmt.Println("  checks:", strings.Join(rep.Checks, ", "))
				if setupText != "" {
					fmt.Print(setupText)
				}
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

// warnPrimaryLanguageGraph appends WARN entries when the primary language has
// zero indexed symbols, only contains/reads edges (inventory-only), or a sparse
// call graph. These previously left doctor "healthy" on broken extractors.
func warnPrimaryLanguageGraph(rep *doctorReport, st *graph.Store, root, repoID string) {
	if rep == nil || st == nil {
		return
	}
	prof, err := profile.ReadOrGenerate(root)
	if err != nil || prof == nil {
		return
	}
	lang := strings.TrimSpace(prof.PrimaryLanguage)
	if lang == "" {
		return
	}
	// Markup-only "primary" is already demoted in pickPrimaryLanguage; still skip
	// shell/css noise if somehow selected.
	switch lang {
	case "css", "html", "sql", "shell":
		return
	}
	syms, calls, imports, herr := st.LanguageIndexHealth(context.Background(), repoID, lang)
	if herr != nil {
		rep.Warnings = append(rep.Warnings, "primary language graph check failed: "+herr.Error())
		return
	}
	if syms == 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"primary language %q has 0 indexed symbols — parser may be broken; run `codehelper analyze --force`", lang))
		return
	}
	if calls == 0 && imports == 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"primary language %q has %d symbols but 0 call/import edges (inventory-only) — MCP blast-radius tools will be unreliable; reanalyze after upgrading parsers", lang, syms))
		return
	}
	if calls == 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"primary language %q has %d symbols but 0 call edges (contains-only) — call graph is unavailable; reanalyze after upgrading parsers", lang, syms))
		return
	}
	// Sparse: enough symbols for a real graph but almost no call edges per symbol.
	const sparseCallDensity = 0.05
	density := float64(calls) / float64(syms)
	if density < sparseCallDensity {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"primary language %q call graph is sparse (%.3f calls/symbol; %d calls / %d symbols) — prefer path=/docs over empty impact; do not treat 0 callers as proof a change is isolated",
			lang, density, calls, syms))
	}
}

// hasGraphQualityWarning reports whether doctor warnings include primary-language
// graph quality problems (inventory-only / contains-only / sparse / empty).
func hasGraphQualityWarning(warnings []string) bool {
	for _, w := range warnings {
		lw := strings.ToLower(w)
		if strings.Contains(lw, "inventory-only") ||
			strings.Contains(lw, "contains-only") ||
			strings.Contains(lw, "edge_count=") ||
			strings.Contains(lw, "call graph is sparse") ||
			strings.Contains(lw, "0 indexed symbols") ||
			strings.Contains(lw, "primary language graph check failed") {
			return true
		}
	}
	return false
}
