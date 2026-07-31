package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// DevOps Lite densify — Dockerfile stages, Compose services, Makefile targets.
// Honest Low/Lite: locate-oriented symbols + sparse reads edges only.
// No runtime graph, no multi-doc YAML AST, no Make expansion/macro fidelity.

var (
	reDockerFROM     = regexp.MustCompile(`(?i)^\s*FROM\s+(\S+)(?:\s+(?:AS|as)\s+([A-Za-z_][\w.-]*))?`)
	reDockerCopyFrom = regexp.MustCompile(`(?i)--from=([A-Za-z_][\w.-]*)`)
	reMakeTarget     = regexp.MustCompile(`^([A-Za-z_][\w.-]*)\s*:(?::=|[^:=]|$)`)
	reMakeSpecial    = regexp.MustCompile(`^\.[A-Z]`)
	reComposeKey     = regexp.MustCompile(`^([A-Za-z_][\w.-]*):\s*(.*)$`)
)

// devopsKind returns dockerfile | compose | makefile for basename-routed paths.
func devopsKind(relPath string) string {
	base := strings.ToLower(filepath.Base(relPath))
	switch base {
	case "dockerfile", "containerfile":
		return "dockerfile"
	case "makefile", "gnumakefile":
		return "makefile"
	case "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml":
		return "compose"
	}
	if strings.HasPrefix(base, "dockerfile.") || strings.HasPrefix(base, "containerfile.") {
		return "dockerfile"
	}
	return ""
}

// DevOpsLanguage returns dockerfile|compose|makefile for basename-routed
// paths, kubernetes|ansible for path-hinted ops YAML, or "" otherwise.
func DevOpsLanguage(relPath string) string {
	if k := devopsKind(relPath); k != "" {
		return k
	}
	return opsYAMLKind(relPath)
}

// IsDevOpsSourcePath reports whether walk/index should include this path
// even when it has no registered SourceExtensions suffix (Dockerfile /
// Makefile / compose basenames, or path-hinted Kubernetes/Ansible YAML).
func IsDevOpsSourcePath(relPath string) bool {
	return devopsKind(relPath) != "" || opsYAMLKind(relPath) != ""
}

func parseDockerfileLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	lines := strings.Split(string(buf), "\n")
	var curStage string
	var curSym string
	stageSym := map[string]string{}
	line := 0
	for _, raw := range lines {
		line++
		ln := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Dockerfile line continuations — keep simple: only match on logical starts.
		if m := reDockerFROM.FindStringSubmatch(trimmed); m != nil {
			stage := m[2]
			if stage == "" {
				// Unnamed final/base stages still get a locate handle.
				stage = fmt.Sprintf("stage@%d", line)
			}
			sym := symbol(repoID, relPath, stage, types.SymbolKindNamespace, line, line, "dockerfile", "FROM "+m[1], "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			stageSym[stage] = sym.ID
			curStage = stage
			curSym = sym.ID
			continue
		}
		if curSym == "" {
			continue
		}
		for _, m := range reDockerCopyFrom.FindAllStringSubmatch(trimmed, -1) {
			from := m[1]
			if from == "" || strings.EqualFold(from, curStage) {
				continue
			}
			tgt := stageSym[from]
			if tgt == "" {
				tgt = fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, from)
			}
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, curSym, tgt, "reads"),
				RepoID:     repoID,
				Kind:       types.RefKindReads,
				SourceID:   curSym,
				TargetID:   tgt,
				Confidence: 0.65,
			})
		}
	}
	return out, nil
}

func parseComposeLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	lines := strings.Split(string(buf), "\n")
	section := "" // services | networks | volumes | other
	sectionIndent := -1
	var curSvc string
	var curSym string
	svcSym := map[string]string{}
	inDepends := false
	dependsIndent := -1

	line := 0
	for _, raw := range lines {
		line++
		ln := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(ln) == "" || strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		indent := composeIndent(ln)
		trimmed := strings.TrimSpace(ln)

		// Top-level section keys (indent 0).
		if indent == 0 {
			inDepends = false
			curSvc, curSym = "", ""
			if m := reComposeKey.FindStringSubmatch(trimmed); m != nil {
				section = strings.ToLower(m[1])
				sectionIndent = 0
			} else {
				section = ""
				sectionIndent = -1
			}
			continue
		}

		if section == "" {
			continue
		}

		// Left the current depends_on block.
		if inDepends && (indent <= dependsIndent || indent <= sectionIndent+2 && !strings.HasPrefix(trimmed, "-")) {
			// still inside service if indent > sectionIndent; depends ends when
			// a sibling key at service+2 appears without list dash.
			if !strings.HasPrefix(trimmed, "-") && indent <= dependsIndent {
				inDepends = false
			}
		}

		switch section {
		case "services":
			// Service name: indent just under services: (typically 2).
			if indent == sectionIndent+2 {
				inDepends = false
				if m := reComposeKey.FindStringSubmatch(trimmed); m != nil {
					name := m[1]
					if name == "" {
						continue
					}
					sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, line, line, "compose", "service", "")
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
					svcSym[name] = sym.ID
					curSvc, curSym = name, sym.ID
				}
				continue
			}
			if curSym == "" {
				continue
			}
			if indent >= sectionIndent+4 {
				key, rest := composeSplitKey(trimmed)
				switch key {
				case "depends_on":
					inDepends = true
					dependsIndent = indent
					// Inline form: depends_on: [a, b] or depends_on: a
					for _, dep := range composeDepsInline(rest) {
						emitComposeDepends(out, repoID, relPath, curSym, curSvc, dep, svcSym)
					}
				case "image", "build":
					// Signature hint only — do not invent image registry symbols.
					if rest != "" && rest != "|" && rest != ">" {
						for i := range out.Symbols {
							if out.Symbols[i].ID == curSym && out.Symbols[i].Signature == "service" {
								out.Symbols[i].Signature = key + " " + stripYAMLScalar(rest)
								break
							}
						}
					}
				default:
					if inDepends && strings.HasPrefix(trimmed, "-") {
						dep := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
						dep = stripYAMLScalar(dep)
						// Long syntax: - service: web  /  - web
						if strings.Contains(dep, ":") {
							if k, v := composeSplitKey(dep); k == "service" || k == "" {
								dep = stripYAMLScalar(v)
							} else if v == "" {
								dep = k
							}
						}
						emitComposeDepends(out, repoID, relPath, curSym, curSvc, dep, svcSym)
					} else if inDepends && indent > dependsIndent {
						if k, v := composeSplitKey(trimmed); k != "" && v == "" {
							// mapping form under depends_on:
							//   api:
							emitComposeDepends(out, repoID, relPath, curSym, curSvc, k, svcSym)
						}
					}
				}
			}
		case "networks", "volumes":
			if indent == sectionIndent+2 {
				if m := reComposeKey.FindStringSubmatch(trimmed); m != nil {
					name := strings.TrimSuffix(section, "s") + ":" + m[1] // network:foo / volume:bar
					sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "compose", section, "")
					out.Symbols = append(out.Symbols, sym)
					out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				}
			}
		}
	}
	return out, nil
}

func emitComposeDepends(out *ParseResult, repoID, relPath, curSym, curSvc, dep string, svcSym map[string]string) {
	dep = strings.TrimSpace(dep)
	if dep == "" || dep == curSvc {
		return
	}
	tgt := svcSym[dep]
	if tgt == "" {
		tgt = fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, dep)
	}
	out.Edges = append(out.Edges, types.Reference{
		ID:         edgeID(repoID, curSym, tgt, "reads"),
		RepoID:     repoID,
		Kind:       types.RefKindReads,
		SourceID:   curSym,
		TargetID:   tgt,
		Confidence: 0.7,
	})
}

func composeIndent(line string) int {
	n := 0
	for _, r := range line {
		if r == ' ' {
			n++
		} else if r == '\t' {
			n += 2
		} else {
			break
		}
	}
	return n
}

func composeSplitKey(trimmed string) (key, rest string) {
	m := reComposeKey.FindStringSubmatch(trimmed)
	if m == nil {
		return "", ""
	}
	return m[1], strings.TrimSpace(m[2])
}

func composeDepsInline(rest string) []string {
	rest = strings.TrimSpace(rest)
	if rest == "" || rest == "|" || rest == ">" {
		return nil
	}
	rest = strings.Trim(rest, "[]")
	parts := strings.Split(rest, ",")
	var out []string
	for _, p := range parts {
		p = stripYAMLScalar(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func stripYAMLScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	// Drop inline comments.
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func parseMakefileLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	lines := strings.Split(string(buf), "\n")
	tgtSym := map[string]string{}
	line := 0
	for _, raw := range lines {
		line++
		ln := strings.TrimRight(raw, "\r")
		if ln == "" || strings.HasPrefix(ln, "\t") || strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		// Skip variable assignments and recipe-looking lines.
		if strings.Contains(ln, "=") && !strings.Contains(ln, ":") {
			continue
		}
		m := reMakeTarget.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		name := m[1]
		if name == "" || reMakeSpecial.MatchString(name) {
			continue
		}
		// Split prerequisites after first ':'.
		rest := ""
		if i := strings.Index(ln, ":"); i >= 0 {
			rest = strings.TrimSpace(ln[i+1:])
			if strings.HasPrefix(rest, "=") {
				// VAR:=value style — not a target.
				continue
			}
			if strings.HasPrefix(rest, ":=") || strings.HasPrefix(rest, "+=") || strings.HasPrefix(rest, "?=") {
				continue
			}
		}
		sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "makefile", "target", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		tgtSym[name] = sym.ID

		for _, dep := range strings.Fields(rest) {
			dep = strings.TrimSpace(dep)
			if dep == "" || dep == name || strings.HasPrefix(dep, "$") {
				continue
			}
			// Order-only prereq marker.
			if dep == "|" {
				continue
			}
			tgt := tgtSym[dep]
			if tgt == "" {
				tgt = fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, dep)
			}
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, sym.ID, tgt, "reads"),
				RepoID:     repoID,
				Kind:       types.RefKindReads,
				SourceID:   sym.ID,
				TargetID:   tgt,
				Confidence: 0.6,
			})
		}
	}
	return out, nil
}
