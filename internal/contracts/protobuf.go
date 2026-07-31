package contracts

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ProtobufContract is a lightweight discovery record for .proto / gRPC IDL.
// Hook for cross-repo service/message edges — not a full protoc validator.
type ProtobufContract struct {
	Path     string   `json:"path"`
	Package  string   `json:"package,omitempty"`
	Services []string `json:"services,omitempty"`
	Messages []string `json:"messages,omitempty"`
	Enums    []string `json:"enums,omitempty"`
	RPCs     []string `json:"rpcs,omitempty"` // Service.Method
	Imports  []string `json:"imports,omitempty"`
	RepoHint string   `json:"repo_hint,omitempty"`
}

var (
	protoPackageRe = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][\w.]*)\s*;`)
	protoImportRe  = regexp.MustCompile(`(?m)^\s*import\s+(?:public\s+|weak\s+)?\"([^\"]+)\"\s*;`)
	protoMessageRe = regexp.MustCompile(`(?m)^\s*(?:optional\s+)?message\s+([A-Za-z_][\w]*)\s*\{`)
	protoEnumRe    = regexp.MustCompile(`(?m)^\s*enum\s+([A-Za-z_][\w]*)\s*\{`)
	protoServiceRe = regexp.MustCompile(`(?m)^\s*service\s+([A-Za-z_][\w]*)\s*\{`)
	protoRPCRe     = regexp.MustCompile(`(?m)^\s*rpc\s+([A-Za-z_][\w]*)\s*\(`)
)

// DiscoverProtobuf finds .proto files under root (common paths + shallow dirs).
func DiscoverProtobuf(root string) []ProtobufContract {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	candidates := []string{
		"api.proto", "service.proto", "schema.proto",
		"proto/api.proto", "proto/service.proto",
		"api/api.proto", "api/service.proto",
		"protos/api.proto", "idl/api.proto",
		"pkg/api/api.proto", "internal/api/api.proto",
	}
	seen := map[string]struct{}{}
	var out []ProtobufContract
	add := func(abs string) {
		c, ok := parseProtobufFile(abs)
		if !ok {
			return
		}
		key := filepath.ToSlash(c.Path)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	for _, rel := range candidates {
		add(filepath.Join(root, filepath.FromSlash(rel)))
	}
	for _, dir := range []string{"proto", "protos", "api", "idl", "protobuf", "grpc", "pkg/api", "internal/api"} {
		base := filepath.Join(root, filepath.FromSlash(dir))
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				// one level: proto/common/*.proto
				sub := filepath.Join(base, e.Name())
				subEntries, err := os.ReadDir(sub)
				if err != nil {
					continue
				}
				for _, se := range subEntries {
					if se.IsDir() {
						continue
					}
					if strings.HasSuffix(strings.ToLower(se.Name()), ".proto") {
						add(filepath.Join(sub, se.Name()))
					}
				}
				continue
			}
			if strings.HasSuffix(strings.ToLower(e.Name()), ".proto") {
				add(filepath.Join(base, e.Name()))
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func parseProtobufFile(abs string) (ProtobufContract, bool) {
	b, err := os.ReadFile(abs)
	if err != nil || len(b) == 0 {
		return ProtobufContract{}, false
	}
	if strings.ToLower(filepath.Ext(abs)) != ".proto" {
		return ProtobufContract{}, false
	}
	text := stripProtoLineComments(string(b))
	if !looksLikeProtobuf(text) {
		return ProtobufContract{}, false
	}
	pkg := ""
	if m := protoPackageRe.FindStringSubmatch(text); len(m) >= 2 {
		pkg = m[1]
	}
	var imports []string
	for _, m := range protoImportRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			imports = append(imports, m[1])
		}
	}
	var messages, enums, services []string
	for _, m := range protoMessageRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			messages = append(messages, m[1])
		}
	}
	for _, m := range protoEnumRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			enums = append(enums, m[1])
		}
	}
	for _, m := range protoServiceRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			services = append(services, m[1])
		}
	}
	rpcs := extractProtobufRPCs(text)
	if len(messages) == 0 && len(enums) == 0 && len(services) == 0 && len(rpcs) == 0 {
		return ProtobufContract{}, false
	}
	sort.Strings(messages)
	sort.Strings(enums)
	sort.Strings(services)
	sort.Strings(rpcs)
	sort.Strings(imports)
	return ProtobufContract{
		Path:     abs,
		Package:  pkg,
		Services: uniqueStrings(services),
		Messages: uniqueStrings(messages),
		Enums:    uniqueStrings(enums),
		RPCs:     uniqueStrings(rpcs),
		Imports:  uniqueStrings(imports),
	}, true
}

func looksLikeProtobuf(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "syntax") ||
		strings.Contains(lower, "message ") ||
		strings.Contains(lower, "service ") ||
		strings.Contains(lower, "import \"") ||
		strings.Contains(lower, "package ")
}

func stripProtoLineComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		b.WriteString(line)
	}
	return b.String()
}

func extractProtobufRPCs(text string) []string {
	lines := strings.Split(text, "\n")
	var out []string
	currentService := ""
	braceDepth := 0
	inService := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if m := protoServiceRe.FindStringSubmatch(line); len(m) >= 2 {
			currentService = m[1]
			inService = true
			braceDepth = strings.Count(line, "{") - strings.Count(line, "}")
			if braceDepth < 0 {
				braceDepth = 0
			}
			continue
		}
		if !inService || currentService == "" {
			continue
		}
		open := strings.Count(line, "{")
		closeN := strings.Count(line, "}")
		if m := protoRPCRe.FindStringSubmatch(line); len(m) >= 2 {
			out = append(out, currentService+"."+m[1])
		}
		braceDepth += open - closeN
		if braceDepth <= 0 && strings.Contains(line, "}") {
			inService = false
			currentService = ""
			braceDepth = 0
		}
	}
	return out
}
