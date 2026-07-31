package indexer

// Path-alias resolution for JS/TS projects.
//
// Real apps almost never import by relative path across module boundaries; they
// import through an alias (`@/lib/db`, `~/components/Button`, `#internal/cache`)
// declared in tsconfig/jsconfig `compilerOptions.paths`, a Vite/webpack
// `resolve.alias` table, or package.json `imports`. A parser sees only the raw
// specifier, so graph.ResolveSymrefs could not use those imports to disambiguate
// same-named symbols — the single most common precision loss on real front ends
// (three `index.ts`, four `Button`, a `handler` per route).
//
// DetectImportAliases reads the manifests once per index run and
// ExpandAliasImportEdges adds a SECOND imports edge carrying the repo-relative
// target. The raw edge is kept so nothing that already matched regresses.

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/internal/parser"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// maxAliasManifestBytes caps manifest reads; real configs are a few KB.
const maxAliasManifestBytes = 512 * 1024

// aliasFSRootKey stashes the absolute index root inside the alias table so
// ResolveAliasSpecifier can probe the filesystem when an alias has several
// candidate targets (tsconfig fallbacks, conventional @/ → src|resources/js).
// It can never match a real import specifier.
const aliasFSRootKey = "\x00fsroot"

// aliasConfigFiles are the manifests scanned for path aliases, in priority order.
var aliasConfigFiles = []string{
	"tsconfig.json", "tsconfig.base.json", "tsconfig.app.json", "jsconfig.json",
}

// viteAliasFiles are bundler configs scanned with a regex (they are code, not JSON).
var viteAliasFiles = []string{
	"vite.config.ts", "vite.config.js", "vite.config.mjs", "vite.config.mts",
	"webpack.config.js", "webpack.config.ts", "webpack.config.mjs",
	"nuxt.config.ts", "nuxt.config.js", "svelte.config.js", "rollup.config.js",
}

// aliasProbeExts / aliasProbeIndex are tried when a candidate has no extension
// (the usual `@/lib/db` → `src/lib/db` form).
var (
	aliasProbeExts = []string{
		".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts",
		".vue", ".svelte",
	}
	aliasProbeIndex = []string{
		"index.ts", "index.tsx", "index.js", "index.jsx",
		"index.mjs", "index.vue", "index.svelte",
	}
)

var (
	// '@': path.resolve(__dirname, './src')  /  '~': fileURLToPath(new URL('./src', import.meta.url))
	// The key must be QUOTED: that is how every sigil alias is written, and it
	// keeps unquoted config options (`plugins:`, `port:`) from being read as aliases.
	// Anything between the colon and the first string literal on the line is
	// skipped, so the wrapper call and its `__dirname` argument do not matter.
	bundlerAliasRe = regexp.MustCompile(
		`["']([@~#][@~#A-Za-z0-9_/\-.]*|[A-Za-z_][A-Za-z0-9_\-]*)["']\s*:\s*[^"'\n]*["']([^"'\n]+)["']`)
	jsonTrailingCommaRe = regexp.MustCompile(`,(\s*[}\]])`)
)

// DetectImportAliases returns alias prefix → candidate repo-relative targets.
//
// A prefix that ended in `/*` keeps its trailing slash (`@/` → `src/`) and is
// matched by prefix; an exact alias (`@app` → `src/app/index.ts`) has no slash
// and is matched whole. Targets are repo-relative, slash-separated, and never
// escape the repo root. When several targets share a prefix, ResolveAliasSpecifier
// probes the repo filesystem and keeps the first real hit.
func DetectImportAliases(root string) map[string][]string {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	out := map[string][]string{}
	for _, name := range aliasConfigFiles {
		mergeTSConfigAliases(root, name, out, 0)
	}
	for _, name := range viteAliasFiles {
		mergeBundlerAliases(root, name, out)
	}
	mergePackageImports(root, out)
	mergeConventionalAliases(root, out)
	if len(out) == 0 {
		return nil
	}
	// Absolute root for multi-candidate FS probing (skipped as an alias key).
	out[aliasFSRootKey] = []string{root}
	return out
}

// tsConfigShape is the subset of tsconfig/jsconfig we read.
type tsConfigShape struct {
	Extends         string `json:"extends"`
	CompilerOptions struct {
		BaseURL string              `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
}

// mergeTSConfigAliases reads compilerOptions.paths from a tsconfig/jsconfig,
// following `extends` up to two levels (Nuxt/Next/Angular all extend a base).
func mergeTSConfigAliases(root, rel string, into map[string][]string, depth int) {
	if depth > 2 {
		return
	}
	raw := readManifest(filepath.Join(root, rel))
	if raw == "" {
		return
	}
	var cfg tsConfigShape
	if json.Unmarshal([]byte(relaxedJSON(raw)), &cfg) != nil {
		return
	}
	if ext := strings.TrimSpace(cfg.Extends); ext != "" &&
		(strings.HasPrefix(ext, "./") || strings.HasPrefix(ext, "../")) {
		base := path.Clean(path.Join(path.Dir(filepath.ToSlash(rel)), ext))
		if !strings.HasSuffix(strings.ToLower(base), ".json") {
			base += ".json"
		}
		if !strings.HasPrefix(base, "..") {
			mergeTSConfigAliases(root, base, into, depth+1)
		}
	}
	// paths are relative to baseUrl, which is itself relative to the config file.
	baseDir := path.Dir(filepath.ToSlash(rel))
	if baseDir == "." {
		baseDir = ""
	}
	baseURL := strings.TrimSpace(cfg.CompilerOptions.BaseURL)
	prefix := joinRepoRel(baseDir, baseURL)
	for pattern, targets := range cfg.CompilerOptions.Paths {
		key := aliasKey(pattern)
		if key == "" {
			continue
		}
		for _, t := range targets {
			addAliasTarget(into, key, joinRepoRel(prefix, strings.TrimSuffix(strings.TrimSpace(t), "*")))
		}
	}
}

// mergeBundlerAliases regex-scans a Vite/webpack/Nuxt config's resolve.alias table.
// Bundler configs are code, so only literal string targets are recovered — enough
// for the `'@': path.resolve(__dirname, './src')` form that dominates real repos.
func mergeBundlerAliases(root, rel string, into map[string][]string) {
	raw := readManifest(filepath.Join(root, rel))
	if raw == "" {
		return
	}
	lower := strings.ToLower(raw)
	idx := strings.Index(lower, "alias")
	if idx < 0 {
		return
	}
	// Bound the scan to the alias table region; whole-file scanning would treat
	// every `key: 'string'` option (plugins, server, build) as an alias.
	region := raw[idx:]
	if end := strings.Index(region, "\n\t\t},"); end > 0 {
		region = region[:end]
	}
	if len(region) > 4096 {
		region = region[:4096]
	}
	baseDir := path.Dir(filepath.ToSlash(rel))
	if baseDir == "." {
		baseDir = ""
	}
	for _, m := range bundlerAliasRe.FindAllStringSubmatch(region, -1) {
		if len(m) < 3 {
			continue
		}
		key, target := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		if key == "" || target == "" || !strings.HasPrefix(target, ".") {
			continue // only literal in-repo paths; skip package redirects
		}
		addAliasTarget(into, aliasKey(key), joinRepoRel(baseDir, target))
	}
}

// mergePackageImports reads package.json "imports" (Node subpath imports, `#lib/*`).
func mergePackageImports(root string, into map[string][]string) {
	raw := readManifest(filepath.Join(root, "package.json"))
	if raw == "" {
		return
	}
	var pkg struct {
		Imports map[string]json.RawMessage `json:"imports"`
	}
	if json.Unmarshal([]byte(relaxedJSON(raw)), &pkg) != nil {
		return
	}
	for pattern, rawTarget := range pkg.Imports {
		key := aliasKey(pattern)
		if key == "" {
			continue
		}
		for _, t := range packageImportTargets(rawTarget) {
			if !strings.HasPrefix(t, "./") {
				continue // bare package redirect, not an in-repo path
			}
			addAliasTarget(into, key, joinRepoRel("", strings.TrimSuffix(t, "*")))
		}
	}
}

// packageImportTargets flattens a package.json imports value, which is either a
// string or a conditional-export object ({"default": "./src/x.js", …}).
func packageImportTargets(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	var out []string
	for _, v := range obj {
		out = append(out, packageImportTargets(v)...)
	}
	return out
}

// mergeConventionalAliases adds framework defaults that are real but undeclared:
// Nuxt/SvelteKit resolve `~/` and `@/` to the project (or src/) root even with no
// tsconfig paths entry, and Laravel Vite apps alias `@` to resources/js.
func mergeConventionalAliases(root string, into map[string][]string) {
	dirExists := func(rel string) bool {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil && fi.IsDir()
	}
	if dirExists("src") {
		addAliasTarget(into, "~/", "src/")
		addAliasTarget(into, "@/", "src/")
	}
	if dirExists("resources/js") {
		addAliasTarget(into, "@/", "resources/js/")
	}
	if dirExists("resources/ts") {
		addAliasTarget(into, "@/", "resources/ts/")
	}
	if dirExists("app") && dirExists("components") {
		// Nuxt 3 layout: `~/components/X` resolves from the project root.
		addAliasTarget(into, "~/", "")
	}
}

// aliasKey normalizes an alias pattern into a lookup key: a wildcard pattern
// keeps its trailing slash, an exact alias keeps its literal form.
func aliasKey(pattern string) string {
	p := strings.TrimSpace(pattern)
	if p == "" || p == "*" {
		return ""
	}
	if strings.HasSuffix(p, "/*") {
		return strings.TrimSuffix(p, "*") // "@/*" → "@/"
	}
	if strings.HasSuffix(p, "*") {
		return strings.TrimSuffix(p, "*") // "@app*" → "@app"
	}
	return p
}

// joinRepoRel joins config-relative segments into a clean repo-relative path.
func joinRepoRel(base, rel string) string {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	rel = strings.TrimPrefix(rel, "./")
	if rel == "." {
		rel = ""
	}
	joined := rel
	if base != "" {
		joined = path.Join(base, rel)
	}
	if joined == "" || joined == "." {
		return ""
	}
	trailing := strings.HasSuffix(rel, "/") || rel == ""
	joined = path.Clean(joined)
	if strings.HasPrefix(joined, "..") {
		return ""
	}
	if trailing && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	return joined
}

func addAliasTarget(into map[string][]string, key, target string) {
	if into == nil || key == "" {
		return
	}
	// An empty target is legal: `~/` → repo root (Nuxt).
	for _, t := range into[key] {
		if t == target {
			return
		}
	}
	into[key] = append(into[key], target)
}

// readManifest reads a bounded manifest, returning "" when missing or oversized.
func readManifest(abs string) string {
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() || fi.Size() > maxAliasManifestBytes {
		return ""
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	return string(b)
}

// relaxedJSON strips // and /* */ comments and trailing commas so real tsconfig
// files (which are JSONC) parse with encoding/json.
func relaxedJSON(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	inStr, inLine, inBlock, escaped := false, false, false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				b.WriteByte(c)
			}
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
			}
		case inStr:
			b.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
		case c == '"':
			inStr = true
			b.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			inLine = true
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			inBlock = true
			i++
		default:
			b.WriteByte(c)
		}
	}
	return jsonTrailingCommaRe.ReplaceAllString(b.String(), "$1")
}

// ExpandAliasImportEdges appends a repo-relative imports edge for every alias
// import in edges. The original edge is preserved, so an alias that also happens
// to match by package suffix keeps working. Non-alias specifiers are untouched.
// When DetectImportAliases supplied several targets for one prefix, only the
// filesystem-backed candidate is expanded (see ResolveAliasSpecifier).
func ExpandAliasImportEdges(repoID, relPath string, edges []types.Reference, aliases map[string][]string) []types.Reference {
	if len(aliases) == 0 || len(edges) == 0 {
		return edges
	}
	seen := map[string]bool{}
	for _, e := range edges {
		if e.Kind == types.RefKindImports {
			seen[e.TargetID] = true
		}
	}
	var added []types.Reference
	fid := parser.FileNodeID(repoID, relPath)
	for _, e := range edges {
		if e.Kind != types.RefKindImports || !strings.HasPrefix(e.TargetID, "mod:") {
			continue
		}
		spec := moduleSpecifier(e.TargetID)
		for _, resolved := range ResolveAliasSpecifier(spec, aliases) {
			target := parser.ModuleNodeID(repoID, resolved)
			if seen[target] {
				continue
			}
			seen[target] = true
			added = append(added, types.Reference{
				ID:         parser.EdgeID(repoID, fid, target, "imports"),
				RepoID:     repoID,
				Kind:       types.RefKindImports,
				SourceID:   fid,
				TargetID:   target,
				Confidence: e.Confidence,
			})
		}
	}
	if len(added) == 0 {
		return edges
	}
	return append(edges, added...)
}

// ResolveAliasSpecifier expands one import specifier through the alias table,
// returning repo-relative paths. When an alias has several candidates and the
// table carries an FS root (from DetectImportAliases), only the first path that
// exists on disk is returned — matching TypeScript path-fallback semantics and
// avoiding spurious imports edges for every conventional/bundler root.
// If nothing exists yet (incomplete tree), all candidates are kept as before.
func ResolveAliasSpecifier(spec string, aliases map[string][]string) []string {
	return resolveAliasSpecifier(spec, aliases, aliasTableRoot(aliases))
}

func resolveAliasSpecifier(spec string, aliases map[string][]string, root string) []string {
	spec = strings.TrimSpace(filepath.ToSlash(spec))
	if spec == "" || strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") {
		return nil
	}
	// Longest matching prefix wins so `@/components/` beats `@/`.
	bestKey, bestLen := "", -1
	for key := range aliases {
		if key == aliasFSRootKey {
			continue
		}
		match := false
		if strings.HasSuffix(key, "/") {
			match = strings.HasPrefix(spec, key)
		} else {
			match = spec == key || strings.HasPrefix(spec, key+"/")
		}
		if match && len(key) > bestLen {
			bestKey, bestLen = key, len(key)
		}
	}
	if bestKey == "" {
		return nil
	}
	rest := strings.TrimPrefix(spec, bestKey)
	rest = strings.TrimPrefix(rest, "/")
	var out []string
	for _, base := range aliases[bestKey] {
		joined := rest
		if base != "" {
			joined = path.Join(base, rest)
		}
		joined = path.Clean(joined)
		if joined == "" || joined == "." || strings.HasPrefix(joined, "..") {
			continue
		}
		out = append(out, joined)
	}
	if root != "" && len(out) > 1 {
		if hit := preferExistingAliasTarget(root, out); hit != "" {
			return []string{hit}
		}
	}
	return out
}

func aliasTableRoot(aliases map[string][]string) string {
	if aliases == nil {
		return ""
	}
	if roots := aliases[aliasFSRootKey]; len(roots) > 0 {
		return roots[0]
	}
	return ""
}

// preferExistingAliasTarget returns the first candidate that exists under root
// (file, directory, or common extension/index variants). Empty when none hit.
func preferExistingAliasTarget(root string, candidates []string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	for _, rel := range candidates {
		if aliasTargetExists(root, rel) {
			return rel
		}
	}
	return ""
}

func aliasTargetExists(root, rel string) bool {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if rel == "" || strings.HasPrefix(rel, "..") {
		return false
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err == nil {
		return true // file or directory
	}
	// Extensionless module path (`src/lib/db`).
	if filepath.Ext(abs) == "" {
		for _, ext := range aliasProbeExts {
			if _, err := os.Stat(abs + ext); err == nil {
				return true
			}
		}
		for _, idx := range aliasProbeIndex {
			if _, err := os.Stat(filepath.Join(abs, idx)); err == nil {
				return true
			}
		}
	}
	return false
}

// moduleSpecifier extracts the import path from a `mod:repoID:spec` node id.
func moduleSpecifier(id string) string {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}
