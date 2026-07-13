package mcpsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// projectBrief gathers the orientation a human (and an agent) wants up front so
// the project_context bootstrap is actually informative: the frameworks in use,
// key dependencies WITH versions, and a short summary of what the project is
// (from its README). Everything is best-effort and bounded — missing manifests
// just yield empty fields.
func projectBrief(root string) (frameworks, deps []string, summary string) {
	deps = keyDependencies(root)
	frameworks = detectFrameworks(root, deps)
	summary = readmeSummary(root)
	return
}

var goRequireRe = regexp.MustCompile(`^\s*([\w./-]+)\s+(v[\w.+-]+)`)

// keyDependencies parses the project's manifest(s) for direct dependencies and
// their versions, returning up to a bounded set of "name@version" strings.
func keyDependencies(root string) []string {
	const maxDeps = 18
	var out []string
	add := func(name, ver string) {
		name = strings.TrimSpace(name)
		ver = strings.TrimSpace(strings.Trim(ver, `"',`))
		if name == "" {
			return
		}
		if ver == "" {
			out = append(out, name)
		} else {
			out = append(out, name+"@"+ver)
		}
	}

	// Go
	if b, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.Contains(ln, "// indirect") {
				continue
			}
			if m := goRequireRe.FindStringSubmatch(ln); m != nil && m[1] != "module" && m[1] != "go" && m[1] != "require" {
				add(m[1], m[2])
			}
		}
	}
	// Node
	if m := readJSONMaps(filepath.Join(root, "package.json"), "dependencies", "devDependencies"); m != nil {
		for k, v := range m {
			add(k, v)
		}
	}
	// PHP
	if m := readJSONMaps(filepath.Join(root, "composer.json"), "require"); m != nil {
		for k, v := range m {
			if k == "php" {
				continue
			}
			add(k, v)
		}
	}
	// Python
	if b, err := os.ReadFile(filepath.Join(root, "requirements.txt")); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" || strings.HasPrefix(ln, "#") {
				continue
			}
			if i := strings.IndexAny(ln, "=<>~!"); i > 0 {
				add(ln[:i], strings.TrimLeft(ln[i:], "=<>~! "))
			} else {
				add(ln, "")
			}
		}
	}
	// Rust (naive [dependencies] table)
	if b, err := os.ReadFile(filepath.Join(root, "Cargo.toml")); err == nil {
		inDeps := false
		re := regexp.MustCompile(`^\s*([\w-]+)\s*=\s*(?:"([^"]+)"|\{[^}]*version\s*=\s*"([^"]+)")`)
		for _, ln := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, "[") {
				inDeps = t == "[dependencies]"
				continue
			}
			if inDeps {
				if m := re.FindStringSubmatch(ln); m != nil {
					ver := m[2]
					if ver == "" {
						ver = m[3]
					}
					add(m[1], ver)
				}
			}
		}
	}

	sort.Strings(out)
	out = dedupeStr(out)
	if len(out) > maxDeps {
		out = out[:maxDeps]
	}
	return out
}

// detectFrameworks maps known dependency names and marker files to frameworks
// (including game engines), so the agent knows the stack without reading code.
func detectFrameworks(root string, deps []string) []string {
	set := map[string]struct{}{}
	depName := func(d string) string {
		if i := strings.LastIndex(d, "@"); i > 0 {
			return d[:i]
		}
		return d
	}
	depMap := map[string]string{ // dependency -> framework label
		"react": "react", "next": "nextjs", "vue": "vue", "nuxt": "nuxt",
		"svelte": "svelte", "@sveltejs/kit": "sveltekit", "@angular/core": "angular",
		"express": "express", "fastapi": "fastapi", "flask": "flask", "django": "django",
		"laravel/framework": "laravel", "symfony/framework-bundle": "symfony",
		"rails": "rails", "@capacitor/core": "capacitor", "electron": "electron",
		"@nestjs/core": "nestjs", "gin-gonic/gin": "gin", "github.com/gin-gonic/gin": "gin",
		"github.com/gofiber/fiber/v2": "fiber", "tailwindcss": "tailwind",
	}
	for _, d := range deps {
		if fw, ok := depMap[depName(d)]; ok {
			set[fw] = struct{}{}
		}
	}
	// Engine / marker files
	exists := func(rel string) bool { _, err := os.Stat(filepath.Join(root, rel)); return err == nil }
	// Marker / config files — catch a framework even when it isn't a parsed direct
	// dependency (a framework's own repo, or a manifest the parser didn't read).
	for _, m := range []struct{ file, fw string }{
		{"manage.py", "django"}, {"config/application.rb", "rails"}, {"bin/rails", "rails"},
		{"artisan", "laravel"}, {"angular.json", "angular"}, {"next.config.js", "nextjs"},
		{"next.config.mjs", "nextjs"}, {"nuxt.config.ts", "nuxt"}, {"svelte.config.js", "sveltekit"},
		{"astro.config.mjs", "astro"}, {"remix.config.js", "remix"}, {"gatsby-config.js", "gatsby"},
		{"vite.config.ts", "vite"}, {"vite.config.js", "vite"}, {"nest-cli.json", "nestjs"},
	} {
		if exists(m.file) {
			set[m.fw] = struct{}{}
		}
	}
	if exists("project.godot") {
		set["godot"] = struct{}{}
	}
	if exists("ProjectSettings/ProjectVersion.txt") || exists("Assets") && exists("ProjectSettings") {
		set["unity"] = struct{}{}
	}
	if hasGlob(root, "*.uproject") {
		set["unreal"] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasGlob(root, pattern string) bool {
	matches, _ := filepath.Glob(filepath.Join(root, pattern))
	return len(matches) > 0
}

// readmeSummary returns a short prose summary of the project from its README:
// the title plus the first couple of prose paragraphs, badges/images/HTML
// stripped, capped in length.
func readmeSummary(root string) string {
	var path string
	for _, n := range []string{"README.md", "Readme.md", "readme.md", "README.markdown", "README.rst", "README.txt", "README"} {
		p := filepath.Join(root, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			path = p
			break
		}
	}
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := string(b)
	if len(text) > 16*1024 {
		text = text[:16*1024]
	}

	var paras []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			paras = append(paras, s)
		}
		cur.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			flush()
		case strings.HasPrefix(t, "#"):
			flush()
			if h := strings.TrimSpace(strings.TrimLeft(t, "# ")); h != "" && len(paras) == 0 {
				paras = append(paras, h)
			}
		case strings.HasPrefix(t, "!["), strings.HasPrefix(t, "[!["), strings.HasPrefix(t, "<"),
			strings.HasPrefix(t, "---"), strings.HasPrefix(t, "==="), strings.HasPrefix(t, "```"):
			// skip badges, images, raw HTML, rules, code fences
		default:
			cur.WriteString(t)
			cur.WriteByte(' ')
		}
		if len(paras) >= 3 {
			break
		}
	}
	flush()
	if len(paras) > 3 {
		paras = paras[:3]
	}
	out := strings.Join(strings.Fields(strings.Join(paras, " — ")), " ")
	if len(out) > 600 {
		out = strings.TrimSpace(out[:600]) + "…"
	}
	return out
}

func readJSONMaps(path string, keys ...string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := raw[k]; ok {
			var m map[string]string
			if json.Unmarshal(v, &m) == nil {
				for dk, dv := range m {
					out[dk] = dv
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func dedupeStr(in []string) []string {
	seen := map[string]struct{}{}
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// hostOS is the operating system codehelper (and thus the user) is running on.
func hostOS() string { return runtime.GOOS }

// gitFacts captures whether the project is git-connected and where.
type gitFacts struct {
	Branch string `json:"branch,omitempty"`
	Remote string `json:"remote,omitempty"`
}

// gitInfo reads .git directly (no shelling out): current branch + first remote
// URL, with any embedded credentials stripped. Returns nil when not a git repo.
func gitInfo(root string) *gitFacts {
	if st, err := os.Stat(filepath.Join(root, ".git")); err != nil || !st.IsDir() {
		return nil
	}
	g := &gitFacts{}
	if b, err := os.ReadFile(filepath.Join(root, ".git", "HEAD")); err == nil {
		s := strings.TrimSpace(string(b))
		if ref := strings.TrimPrefix(s, "ref: refs/heads/"); ref != s {
			g.Branch = ref
		} else if len(s) >= 7 {
			g.Branch = "detached@" + s[:7]
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, ".git", "config")); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(ln)
			if u := strings.TrimPrefix(t, "url = "); u != t {
				g.Remote = sanitizeRemote(strings.TrimSpace(u))
				break
			}
		}
	}
	return g
}

// sanitizeRemote drops embedded credentials (https://user:pass@host) so a token
// in the remote URL never lands in a response.
func sanitizeRemote(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 && at < strings.IndexAny(rest+"/", "/") {
			u = u[:i+3] + rest[at+1:]
		}
	}
	return strings.TrimSuffix(u, ".git")
}

// projectScripts surfaces the runnable commands a human would use: npm scripts,
// composer scripts, and Makefile targets.
func projectScripts(root string) []string {
	var out []string
	if m := readJSONMaps(filepath.Join(root, "package.json"), "scripts"); m != nil {
		for k, v := range m {
			cmd := "npm run " + k
			if vv := strings.TrimSpace(v); vv != "" && len(vv) <= 50 {
				cmd += ": " + vv
			}
			out = append(out, cmd)
		}
	}
	if m := readJSONMaps(filepath.Join(root, "composer.json"), "scripts"); m != nil {
		for k := range m {
			out = append(out, "composer "+k)
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil {
		re := regexp.MustCompile(`(?m)^([a-zA-Z][\w-]*):`)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			out = append(out, "make "+m[1])
		}
	}
	sort.Strings(out)
	out = dedupeStr(out)
	if len(out) > 14 {
		out = out[:14]
	}
	return out
}

// projectSurfaces classifies what KIND of project this is so the agent knows the
// terrain: frontend, backend, api, native/c++, kernel, game, mobile/desktop,
// database, infra/ci. Best-effort from languages + frameworks + marker files.
func projectSurfaces(root string, langs, frameworks []string) []string {
	L := toLowerSet(langs)
	F := toLowerSet(frameworks)
	S := map[string]struct{}{}
	ex := func(rel string) bool { _, err := os.Stat(filepath.Join(root, rel)); return err == nil }
	anyF := func(keys ...string) bool {
		for _, k := range keys {
			if F[k] {
				return true
			}
		}
		return false
	}
	if L["css"] || L["javascript"] || anyF("react", "nextjs", "vue", "nuxt", "svelte", "sveltekit", "angular") || ex("index.html") || ex("public") || ex("src/components") {
		S["frontend"] = struct{}{}
	}
	if anyF("express", "fastapi", "flask", "django", "laravel", "rails", "gin", "fiber", "nestjs", "symfony") || L["go"] || L["php"] || L["python"] || ex("cmd") || ex("server") || ex("controllers") || ex("routes") {
		S["backend"] = struct{}{}
	}
	if ex("openapi.yaml") || ex("openapi.json") || ex("openapi.yml") || ex("swagger.json") || hasGlob(root, "*.proto") || ex("api") {
		S["api"] = struct{}{}
	}
	if L["rust"] || ex("CMakeLists.txt") || hasGlob(root, "*.cpp") || hasGlob(root, "*.cc") || hasGlob(root, "*.c") {
		S["native"] = struct{}{}
	}
	if ex("Kconfig") || ex("Kbuild") {
		S["kernel"] = struct{}{}
	}
	if anyF("godot", "unity", "unreal") {
		S["game"] = struct{}{}
	}
	if anyF("capacitor", "electron") {
		S["mobile/desktop"] = struct{}{}
	}
	if hasGlob(root, "*.sql") || ex("migrations") || ex("db") {
		S["database"] = struct{}{}
	}
	if ex("Dockerfile") || ex("docker-compose.yml") || ex("docker-compose.yaml") || hasGlob(root, "*.tf") || ex(".github/workflows") {
		S["infra/ci"] = struct{}{}
	}
	out := make([]string, 0, len(S))
	for k := range S {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func toLowerSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[strings.ToLower(strings.TrimSpace(s))] = true
	}
	return m
}
