package contracts

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// GraphQLContract is a lightweight discovery record for GraphQL schemas.
// It is a hook for cross-repo API edges — not a full GraphQL validator.
type GraphQLContract struct {
	Path       string   `json:"path"`
	Format     string   `json:"format"` // graphql
	Types      []string `json:"types,omitempty"`
	Operations []string `json:"operations,omitempty"` // Query.field / Mutation.field / Subscription.field
	RepoHint   string   `json:"repo_hint,omitempty"`
}

var (
	gqlTypeRe  = regexp.MustCompile(`(?m)^\s*(?:extend\s+)?(?:type|interface|enum|input|union|scalar)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	gqlFieldRe = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*(?:\([^)]*\))?\s*:`)
)

// DiscoverGraphQL finds GraphQL schema files under root (common paths + shallow dirs).
// Shallow only — does not recurse nested packages; not a full SDL/AST validator.
func DiscoverGraphQL(root string) []GraphQLContract {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	candidates := []string{
		"schema.graphql", "schema.gql",
		"api/schema.graphql", "api/schema.gql",
		"graphql/schema.graphql", "graphql/schema.gql",
		"src/schema.graphql", "src/graphql/schema.graphql",
		"schema/schema.graphql", "docs/schema.graphql",
		"schemas/schema.graphql", "schemas/schema.gql",
	}
	seen := map[string]struct{}{}
	var out []GraphQLContract
	add := func(abs string) {
		c, ok := parseGraphQLFile(abs)
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
	for _, dir := range []string{".", "graphql", "schema", "schemas", "api", "docs", "src", "src/graphql", "src/schema"} {
		base := filepath.Join(root, filepath.FromSlash(dir))
		if dir == "." {
			base = root
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(e.Name())
			if strings.HasSuffix(name, ".graphql") || strings.HasSuffix(name, ".gql") {
				add(filepath.Join(base, e.Name()))
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func parseGraphQLFile(abs string) (GraphQLContract, bool) {
	b, err := os.ReadFile(abs)
	if err != nil || len(b) == 0 {
		return GraphQLContract{}, false
	}
	ext := strings.ToLower(filepath.Ext(abs))
	if ext != ".graphql" && ext != ".gql" {
		return GraphQLContract{}, false
	}
	text := stripGraphQLComments(string(b))
	types := gqlTypeNames(text)
	if len(types) == 0 && !looksLikeGraphQL(text) {
		return GraphQLContract{}, false
	}
	ops := extractGraphQLOperations(text)
	sort.Strings(types)
	sort.Strings(ops)
	return GraphQLContract{
		Path:       abs,
		Format:     "graphql",
		Types:      types,
		Operations: ops,
	}, true
}

func gqlTypeNames(text string) []string {
	var out []string
	for _, m := range gqlTypeRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			out = append(out, m[1])
		}
	}
	return uniqueStrings(out)
}

func looksLikeGraphQL(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "type query") ||
		strings.Contains(lower, "type mutation") ||
		strings.Contains(lower, "type subscription") ||
		strings.Contains(lower, "schema {")
}

func stripGraphQLComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			if i > 0 {
				b.WriteByte('\n')
			}
			continue
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

func extractGraphQLOperations(text string) []string {
	lines := strings.Split(text, "\n")
	var ops []string
	currentRoot := ""
	braceDepth := 0
	inRoot := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if m := gqlTypeRe.FindStringSubmatch(line); len(m) >= 2 {
			name := m[1]
			switch name {
			case "Query", "Mutation", "Subscription":
				currentRoot = name
				inRoot = true
				braceDepth = strings.Count(line, "{") - strings.Count(line, "}")
				if braceDepth < 0 {
					braceDepth = 0
				}
				continue
			default:
				if inRoot {
					inRoot = false
					currentRoot = ""
					braceDepth = 0
				}
			}
		}
		if !inRoot || currentRoot == "" {
			continue
		}
		open := strings.Count(line, "{")
		closeN := strings.Count(line, "}")
		if m := gqlFieldRe.FindStringSubmatch(line); len(m) >= 2 {
			field := m[1]
			switch field {
			case "type", "interface", "enum", "input", "union", "scalar", "extend", "schema":
				// skip keywords
			default:
				ops = append(ops, currentRoot+"."+field)
			}
		}
		braceDepth += open - closeN
		if braceDepth <= 0 && strings.Contains(line, "}") {
			inRoot = false
			currentRoot = ""
			braceDepth = 0
		}
	}
	return uniqueStrings(ops)
}
