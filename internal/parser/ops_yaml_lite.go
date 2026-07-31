package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Ops YAML Lite — Kubernetes Deployment/Service/Ingress + Ansible playbooks/roles.
// Honest Low/Lite: locate-oriented symbols + sparse reads edges only.
// Path-gated (not all .yml); no full YAML AST, no Helm template eval, no
// Ansible fact/Jinja graph. Empty fanout ≠ isolation.

var (
	reK8sKind     = regexp.MustCompile(`(?i)^kind:\s*([A-Za-z][\w]*)$`)
	reK8sName     = regexp.MustCompile(`(?i)^name:\s*(.+)$`)
	reAnsibleName = regexp.MustCompile(`(?i)^(?:-\s*)?name:\s*(.+)$`)
)

// opsYAMLKind returns kubernetes | ansible for path-hinted YAML, or "".
// Compose basenames are handled by devopsKind and take precedence in Extract.
func opsYAMLKind(relPath string) string {
	ext := strings.ToLower(filepath.Ext(relPath))
	if ext != ".yml" && ext != ".yaml" {
		return ""
	}
	if devopsKind(relPath) != "" {
		return ""
	}
	if ansiblePathHint(relPath) {
		return "ansible"
	}
	if kubernetesPathHint(relPath) {
		return "kubernetes"
	}
	return ""
}

// OpsYAMLLanguage returns kubernetes|ansible for path-hinted YAML, or "".
func OpsYAMLLanguage(relPath string) string {
	return opsYAMLKind(relPath)
}

// IsOpsYAMLSourcePath reports whether walk/index should include this YAML path
// even when .yml/.yaml is not a blanket SourceExtensions suffix.
func IsOpsYAMLSourcePath(relPath string) bool {
	return opsYAMLKind(relPath) != ""
}

func kubernetesPathHint(relPath string) bool {
	slash := strings.ToLower(filepath.ToSlash(relPath))
	parts := strings.Split(slash, "/")
	for i, p := range parts {
		if i == len(parts)-1 {
			break
		}
		switch p {
		case "k8s", "kubernetes", "manifests", "kube", "charts":
			return true
		}
	}
	base := parts[len(parts)-1]
	name := strings.TrimSuffix(strings.TrimSuffix(base, ".yml"), ".yaml")
	for _, needle := range []string{
		"deployment", "service", "ingress", "statefulset", "daemonset",
		"configmap", "cronjob", "networkpolicy",
	} {
		if strings.Contains(name, needle) {
			return true
		}
	}
	return false
}

func ansiblePathHint(relPath string) bool {
	slash := strings.ToLower(filepath.ToSlash(relPath))
	parts := strings.Split(slash, "/")
	for i, p := range parts {
		if i == len(parts)-1 {
			break
		}
		switch p {
		case "playbooks", "group_vars", "host_vars":
			return true
		case "roles":
			// roles/<name>/tasks|handlers|vars|defaults|meta/...
			return true
		}
	}
	base := parts[len(parts)-1]
	switch base {
	case "site.yml", "site.yaml", "playbook.yml", "playbook.yaml":
		return true
	}
	return false
}

func ansibleRoleFromPath(relPath string) string {
	orig := strings.Split(filepath.ToSlash(relPath), "/")
	for i, p := range orig {
		if strings.EqualFold(p, "roles") && i+1 < len(orig)-1 {
			return orig[i+1]
		}
	}
	return ""
}

func parseKubernetesLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	symByName := map[string]string{}
	type pendingRead struct{ from, to string }
	var pending []pendingRead

	for _, doc := range splitYAMLDocs(string(buf)) {
		kind, name, backends, kindLine, nameLine := scanK8sDoc(doc)
		if kind == "" {
			continue
		}
		switch strings.ToLower(kind) {
		case "deployment", "service", "ingress", "statefulset", "daemonset",
			"configmap", "secret", "cronjob", "job", "namespace":
		default:
			continue
		}
		symName := name
		if symName == "" {
			symName = fmt.Sprintf("%s@doc", kind)
		}
		line := doc.startLine
		if nameLine > 0 {
			line = nameLine
		} else if kindLine > 0 {
			line = kindLine
		}
		sym := symbol(repoID, relPath, symName, types.SymbolKindNamespace, line, line, "kubernetes", kind, "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		symByName[symName] = sym.ID
		if strings.EqualFold(kind, "Ingress") {
			for _, svc := range backends {
				if svc != "" && svc != symName {
					pending = append(pending, pendingRead{from: sym.ID, to: svc})
				}
			}
		}
	}
	for _, p := range pending {
		tgt := symByName[p.to]
		if tgt == "" {
			tgt = fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, p.to)
		}
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, p.from, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   p.from,
			TargetID:   tgt,
			Confidence: 0.65,
		})
	}
	return out, nil
}

type k8sDocScan struct {
	startLine int
	text      string
}

func splitYAMLDocs(src string) []k8sDocScan {
	lines := strings.Split(src, "\n")
	var docs []k8sDocScan
	start := 1
	var b strings.Builder
	flush := func(endLine int) {
		text := b.String()
		if strings.TrimSpace(text) == "" {
			b.Reset()
			return
		}
		docs = append(docs, k8sDocScan{startLine: start, text: text})
		b.Reset()
		start = endLine + 1
	}
	for i, raw := range lines {
		line := i + 1
		ln := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(ln) == "---" {
			flush(line - 1)
			start = line + 1
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(ln)
	}
	flush(len(lines))
	return docs
}

func scanK8sDoc(doc k8sDocScan) (kind, name string, backends []string, kindLine, nameLine int) {
	lines := strings.Split(doc.text, "\n")
	inMetadata := false
	metaIndent := -1
	inBackend := false
	backendIndent := -1
	inService := false
	serviceIndent := -1

	for i, raw := range lines {
		line := doc.startLine + i
		ln := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := composeIndent(ln)

		if indent == 0 {
			inMetadata = false
			inBackend = false
			inService = false
			if m := reK8sKind.FindStringSubmatch(trimmed); m != nil {
				kind = m[1]
				kindLine = line
			}
			key, _ := composeSplitKey(trimmed)
			if strings.EqualFold(key, "metadata") {
				inMetadata = true
				metaIndent = indent
			}
			continue
		}

		if inMetadata {
			if indent <= metaIndent {
				inMetadata = false
			} else if name == "" {
				if m := reK8sName.FindStringSubmatch(trimmed); m != nil && indent == metaIndent+2 {
					name = stripYAMLScalar(m[1])
					nameLine = line
				}
			}
		}

		key, rest := composeSplitKey(trimmed)
		switch {
		case strings.EqualFold(key, "backend"):
			inBackend = true
			backendIndent = indent
			inService = false
		case inBackend && indent <= backendIndent && !strings.HasPrefix(trimmed, "-"):
			inBackend = false
			inService = false
		}

		if inBackend {
			if strings.EqualFold(key, "serviceName") && rest != "" {
				backends = append(backends, stripYAMLScalar(rest))
			}
			if strings.EqualFold(key, "service") {
				inService = true
				serviceIndent = indent
			}
			if inService {
				if indent <= serviceIndent && !strings.EqualFold(key, "service") {
					inService = false
				} else if strings.EqualFold(key, "name") && rest != "" {
					backends = append(backends, stripYAMLScalar(rest))
				}
			}
		}

		// List-item backends: - backend: / serviceName under path items.
		if strings.HasPrefix(trimmed, "-") {
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			ik, ir := composeSplitKey(item)
			if strings.EqualFold(ik, "backend") {
				inBackend = true
				backendIndent = indent
			}
			if strings.EqualFold(ik, "serviceName") && ir != "" {
				backends = append(backends, stripYAMLScalar(ir))
			}
		}
	}
	return kind, name, backends, kindLine, nameLine
}

func parseAnsibleLite(_ context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	roleSym := map[string]string{}

	if role := ansibleRoleFromPath(relPath); role != "" {
		sym := symbol(repoID, relPath, role, types.SymbolKindNamespace, 1, 1, "ansible", "role", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		roleSym[role] = sym.ID
	}

	lines := strings.Split(string(buf), "\n")
	inRoles := false
	rolesIndent := -1
	inIncludeRole := false
	includeIndent := -1
	var playSym string
	line := 0

	for _, raw := range lines {
		line++
		ln := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := composeIndent(ln)

		if inRoles && indent <= rolesIndent && !strings.HasPrefix(trimmed, "-") {
			inRoles = false
		}
		if inIncludeRole && indent <= includeIndent {
			inIncludeRole = false
		}

		key, rest := composeSplitKey(strings.TrimPrefix(trimmed, "- "))
		if key == "" && strings.HasPrefix(trimmed, "-") {
			key, rest = composeSplitKey(strings.TrimSpace(strings.TrimPrefix(trimmed, "-")))
		}

		switch {
		case strings.EqualFold(key, "roles") && (rest == "" || rest == "|" || rest == ">" || strings.HasPrefix(rest, "[")):
			inRoles = true
			rolesIndent = indent
			for _, r := range composeDepsInline(rest) {
				emitAnsibleRoleRead(out, repoID, relPath, playSym, r, roleSym)
			}
		case strings.EqualFold(key, "include_role") || strings.EqualFold(key, "import_role") ||
			strings.EqualFold(key, "include_tasks") || strings.EqualFold(key, "import_tasks"):
			inIncludeRole = true
			includeIndent = indent
			if rest != "" && rest != "|" && rest != ">" {
				emitAnsibleRoleRead(out, repoID, relPath, playSym, stripYAMLScalar(rest), roleSym)
			}
		case inIncludeRole && strings.EqualFold(key, "name") && rest != "":
			emitAnsibleRoleRead(out, repoID, relPath, playSym, stripYAMLScalar(rest), roleSym)
		case inRoles && strings.HasPrefix(trimmed, "-"):
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			ik, ir := composeSplitKey(item)
			roleName := ""
			switch {
			case ir == "" && ik != "" && !strings.EqualFold(ik, "role"):
				roleName = ik
			case strings.EqualFold(ik, "role") && ir != "":
				roleName = stripYAMLScalar(ir)
			case item != "" && !strings.Contains(item, ":"):
				roleName = stripYAMLScalar(item)
			}
			if roleName != "" {
				emitAnsibleRoleRead(out, repoID, relPath, playSym, roleName, roleSym)
			}
		}

		// Task / play names: "- name: Ensure nginx" or top-level "name:" under a play.
		if m := reAnsibleName.FindStringSubmatch(trimmed); m != nil {
			taskName := stripYAMLScalar(m[1])
			if taskName == "" || strings.EqualFold(taskName, "main") {
				continue
			}
			// Skip role: name: under include_role (already handled as reads).
			if inIncludeRole && !strings.HasPrefix(trimmed, "-") {
				continue
			}
			kind := types.SymbolKindFunction
			sig := "task"
			if indent <= 2 && strings.HasPrefix(trimmed, "-") {
				// Play name often appears as first - name: at low indent with hosts sibling.
				sig = "play_or_task"
			}
			sym := symbol(repoID, relPath, taskName, kind, line, line, "ansible", sig, "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if playSym == "" {
				playSym = sym.ID
			}
		}
	}
	return out, nil
}

func emitAnsibleRoleRead(out *ParseResult, repoID, relPath, fromID, role string, roleSym map[string]string) {
	role = strings.TrimSpace(role)
	if role == "" {
		return
	}
	tgt := roleSym[role]
	if tgt == "" {
		// Mint a role namespace so plays can locate it within the same file scan.
		sym := symbol(repoID, relPath, role, types.SymbolKindNamespace, 1, 1, "ansible", "role", "")
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		roleSym[role] = sym.ID
		tgt = sym.ID
	}
	src := fromID
	if src == "" {
		src = FileNodeID(repoID, relPath)
	}
	if src == tgt {
		return
	}
	out.Edges = append(out.Edges, types.Reference{
		ID:         edgeID(repoID, src, tgt, "reads"),
		RepoID:     repoID,
		Kind:       types.RefKindReads,
		SourceID:   src,
		TargetID:   tgt,
		Confidence: 0.6,
	})
}
