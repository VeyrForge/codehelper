// Package profile generates a heuristic project profile for orchestration and MCP responses.
package profile

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

const Filename = "project_profile.json"

// LanguageStat is one language's share of the codebase by bytes (GitHub-style).
type LanguageStat struct {
	Language string  `json:"language"`
	Percent  float64 `json:"percent"`
	Bytes    int64   `json:"bytes"`
}

// Dependency is one declared dependency from a manifest, with its declared
// version constraint and the ecosystem it came from.
type Dependency struct {
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	Ecosystem string `json:"ecosystem"` // go | npm | composer | cargo | pip | rubygems | maven
	Dev       bool   `json:"dev,omitempty"`
}

// SubProject is a self-contained project nested in the repo (a monorepo member,
// e.g. frontend/ + backend/ with the tool config at the root). Each carries its
// own stack so an agent knows the repo is multi-stack up front.
type SubProject struct {
	Path            string `json:"path"`
	ProjectType     string `json:"project_type"`
	Framework       string `json:"framework,omitempty"`
	Version         string `json:"version,omitempty"`
	PrimaryLanguage string `json:"primary_language,omitempty"`
}

// ProjectProfile is written to .codehelper/project_profile.json.
type ProjectProfile struct {
	ProjectType     string            `json:"project_type"`
	Framework       string            `json:"framework,omitempty"`        // detected app framework (laravel, django, nextjs, rails, spring, …)
	Version         string            `json:"version,omitempty"`          // primary stack version (engine/runtime/framework that defines ProjectType)
	Versions        map[string]string `json:"versions,omitempty"`         // every detected tech version (go, php, node, react, laravel, godot, unity, unreal, …)
	PrimaryLanguage string            `json:"primary_language,omitempty"` // most-used language by bytes
	Languages       []string          `json:"languages"`
	LanguageStats   []LanguageStat    `json:"language_stats,omitempty"` // GitHub-style percentage breakdown by bytes
	Dependencies    []Dependency      `json:"dependencies,omitempty"`   // declared dependencies + versions across manifests
	SubProjects     []SubProject      `json:"sub_projects,omitempty"`   // nested stacks (monorepo: frontend/backend/…)
	PackageManagers []string          `json:"package_managers"`
	Entrypoints     []string          `json:"entrypoints"`
	TestCommands    []string          `json:"test_commands"`
	LintCommands    []string          `json:"lint_commands"`
	DangerZones     []string          `json:"danger_zones"`
	CodingRules     []string          `json:"coding_rules"`
	Gotchas         []string          `json:"gotchas,omitempty"` // framework/language pitfalls an LLM commonly gets wrong
}

// Path returns the absolute path to the profile JSON file.
func Path(repoRoot string) string {
	return filepath.Join(paths.RepoIndexDir(repoRoot), Filename)
}

// Generate scans the repository root (bounded depth) and returns a profile,
// including detection of nested sub-projects (monorepo members).
func Generate(repoRoot string) (ProjectProfile, error) {
	return generateProfile(repoRoot, true)
}

// generateProfile is the detection core. scanSub controls whether nested
// sub-projects are detected — it is false when generating a sub-project's own
// profile, to avoid unbounded recursion.
func generateProfile(repoRoot string, scanSub bool) (ProjectProfile, error) {
	repoRoot = filepath.Clean(repoRoot)
	var p ProjectProfile
	p.Versions = map[string]string{}
	langs := map[string]struct{}{}
	pms := map[string]struct{}{}
	var entries []string

	hasFile := func(rel string) bool {
		_, err := os.Stat(filepath.Join(repoRoot, rel))
		return err == nil
	}
	readRel := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			return ""
		}
		return string(b)
	}

	if hasFile("go.mod") {
		pms["go"] = struct{}{}
		langs["go"] = struct{}{}
		p.ProjectType = "go"
		entries = append(entries, "go.mod")
		p.TestCommands = append(p.TestCommands, "go test ./...")
		p.LintCommands = append(p.LintCommands, "go vet ./...")
		if v := goModVersion(readRel("go.mod")); v != "" {
			p.Versions["go"] = v
		}
	}
	if hasFile("package.json") {
		pms["npm"] = struct{}{}
		langs["javascript"] = struct{}{}
		p.TestCommands = append(p.TestCommands, "npm test")
		p.LintCommands = append(p.LintCommands, "npm run lint")
		if p.ProjectType == "" {
			p.ProjectType = "node"
		}
		entries = append(entries, "package.json")
		pj := readRel("package.json")
		for dep, label := range map[string]string{
			"react": "react", "vue": "vue", "next": "next", "node": "node",
			"@angular/core": "angular", "svelte": "svelte",
		} {
			if v := jsonDep(pj, dep); v != "" {
				p.Versions[label] = v
			}
		}
	}
	if hasFile("composer.json") {
		pms["composer"] = struct{}{}
		langs["php"] = struct{}{}
		p.TestCommands = append(p.TestCommands, "composer test")
		p.LintCommands = append(p.LintCommands, "composer lint")
		cj := readRel("composer.json")
		if v := composerRequire(cj, "php"); v != "" {
			p.Versions["php"] = v
		}
		if v := composerRequire(cj, "laravel/framework"); v != "" {
			p.Versions["laravel"] = v
		}
		// A PHP backend is the defining stack even when package.json (frontend
		// assets) is also present — don't let it mislabel as "node". The concrete
		// framework (WordPress site/plugin/theme, Laravel, Symfony) is resolved by
		// detectFramework below from file headers + deps, not the repo path.
		if p.ProjectType == "" || p.ProjectType == "node" {
			p.ProjectType = "php_composer"
		}
		entries = append(entries, "composer.json")
	}

	if hasFile("Cargo.toml") {
		pms["cargo"] = struct{}{}
		langs["rust"] = struct{}{}
		p.TestCommands = append(p.TestCommands, "cargo test")
		p.LintCommands = append(p.LintCommands, "cargo clippy", "cargo check")
		if p.ProjectType == "" {
			p.ProjectType = "rust"
		}
		entries = append(entries, "Cargo.toml")
		if v := cargoVersion(readRel("Cargo.toml")); v != "" {
			p.Versions["rust"] = v
		}
	}
	if hasFile("pyproject.toml") || hasFile("setup.py") || hasFile("requirements.txt") {
		pms["pip"] = struct{}{}
		langs["python"] = struct{}{}
		p.TestCommands = append(p.TestCommands, "pytest")
		p.LintCommands = append(p.LintCommands, "ruff check", "python3 -m compileall -q .")
		if p.ProjectType == "" {
			p.ProjectType = "python"
		}
		if hasFile("pyproject.toml") {
			entries = append(entries, "pyproject.toml")
			if v := pythonRequires(readRel("pyproject.toml")); v != "" {
				p.Versions["python"] = v
			}
		} else if hasFile("setup.py") {
			entries = append(entries, "setup.py")
		} else {
			entries = append(entries, "requirements.txt")
		}
	}
	// Elixir / Mix — overrides Node when both package.json (JS client assets) and
	// mix.exs are present, matching how composer.json overrides Node for PHP apps.
	if hasFile("mix.exs") {
		pms["mix"] = struct{}{}
		langs["elixir"] = struct{}{}
		p.TestCommands = append(p.TestCommands, "mix test")
		if p.ProjectType == "" || p.ProjectType == "node" {
			p.ProjectType = "elixir"
		}
		entries = append(entries, "mix.exs")
		if v := mixElixirVersion(readRel("mix.exs")); v != "" {
			p.Versions["elixir"] = v
		}
	}

	// Game engines DEFINE the project even when a language manifest is also present
	// (a Godot game with a Go client, Unity with C# + package.json tooling, etc.),
	// so they override the runtime-derived type. Most specific first.
	if ver, ok := detectUnreal(repoRoot); ok {
		p.ProjectType = "unreal"
		langs["cpp"] = struct{}{}
		if ver != "" {
			p.Versions["unreal"] = ver
		}
		entries = append(entries, "*.uproject")
	} else if ver, ok := detectUnity(repoRoot); ok {
		p.ProjectType = "unity"
		langs["csharp"] = struct{}{}
		if ver != "" {
			p.Versions["unity"] = ver
		}
		entries = append(entries, "ProjectSettings/ProjectVersion.txt")
	} else if ver, ok := detectGodot(repoRoot); ok {
		p.ProjectType = "godot"
		if ver != "" {
			p.Versions["godot"] = ver
		}
		entries = append(entries, "project.godot")
	}

	// Declared dependencies (with versions) across every manifest present.
	p.Dependencies = collectDependencies(repoRoot)

	// Refine the type into a concrete framework (WordPress site/plugin/theme,
	// Laravel, Symfony, Next.js, Nuxt, SvelteKit, Angular, NestJS, Vue, React,
	// Django, Flask, FastAPI, Rails, Spring) — unless a game engine already claimed
	// the type. File-header + dependency based, never path based.
	if p.ProjectType != "godot" && p.ProjectType != "unity" && p.ProjectType != "unreal" {
		detectFramework(repoRoot, &p, p.Dependencies)
	}

	stats, primary := collectLangStats(repoRoot, langs)
	p.LanguageStats = stats
	p.PrimaryLanguage = primary

	for k := range langs {
		p.Languages = append(p.Languages, k)
	}
	for k := range pms {
		p.PackageManagers = append(p.PackageManagers, k)
	}
	p.Entrypoints = dedupe(entries)

	if len(p.DangerZones) == 0 {
		p.DangerZones = []string{"auth", "payments", "checkout", "migrations", "public api"}
	}
	if len(p.CodingRules) == 0 {
		p.CodingRules = []string{
			"Validate inputs at boundaries.",
			"Preserve public contracts unless intentionally breaking.",
			"Run verify before claiming done.",
		}
	}

	// No manifest or engine matched but there IS code → use the dominant language
	// as the type, so a repo reads as "unknown" only when it has no source at all.
	if p.ProjectType == "" {
		if primary != "" {
			p.ProjectType = primary
		} else {
			p.ProjectType = "unknown"
		}
	}

	// Resolve the single headline version for the project's defining stack:
	// engine → framework → language runtime, in that order of specificity. Skip if
	// detection already set it directly (e.g. a WordPress plugin's own version).
	if p.Version == "" {
		switch {
		case p.ProjectType == "godot" || p.ProjectType == "unity" || p.ProjectType == "unreal":
			p.Version = p.Versions[p.ProjectType]
		case p.Framework != "" && p.Versions[p.Framework] != "":
			p.Version = p.Versions[p.Framework]
		default:
			p.Version = primaryVersionFor(p.ProjectType, p.Versions)
		}
	}
	// Fall back to the dominant language's runtime version when nothing else gave a
	// headline version (e.g. a Go web framework still reports the Go version).
	if p.Version == "" {
		p.Version = primaryVersionFor(primary, p.Versions)
	}
	if len(p.Versions) == 0 {
		p.Versions = nil
	}

	// Multi-stack repos (monorepos): report each nested project independently.
	if scanSub {
		p.SubProjects = detectSubProjects(repoRoot)
	}

	// Framework/language pitfalls the LLM should know up front (fewer mistakes,
	// fewer discovery tool calls) — for the root stack AND every sub-project's
	// stack, so a Nuxt-frontend + Laravel-backend monorepo surfaces both sets.
	p.Gotchas = gotchasFor(&p)
	for _, sp := range p.SubProjects {
		tmp := ProjectProfile{ProjectType: sp.ProjectType, Framework: sp.Framework, PrimaryLanguage: sp.PrimaryLanguage}
		p.Gotchas = appendUniq(p.Gotchas, gotchasFor(&tmp)...)
	}

	return p, nil
}

// langExt maps a file extension to a language id (consistent with the indexer's
// language ids). Data/markup/config formats are excluded so the byte percentage
// reflects code.
var langExt = map[string]string{
	".go": "go", ".php": "php", ".py": "python", ".rs": "rust", ".rb": "ruby",
	".js": "javascript", ".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".ts": "typescript", ".tsx": "typescript", ".mts": "typescript", ".cts": "typescript",
	".vue": "vue", ".svelte": "svelte",
	".cs": "csharp", ".cpp": "cpp", ".cc": "cpp", ".cxx": "cpp", ".hpp": "cpp", ".hh": "cpp",
	".c": "c", ".h": "c", ".m": "objc", ".mm": "objc",
	".gd": "gdscript", ".lua": "lua", ".java": "java", ".kt": "kotlin", ".kts": "kotlin",
	".ex": "elixir", ".exs": "elixir",
	".swift": "swift", ".sh": "shell", ".bash": "shell",
	".css": "css", ".scss": "css", ".sass": "css", ".less": "css",
	".html": "html", ".htm": "html", ".sql": "sql",
	".gdshader": "gdshader", ".glsl": "glsl", ".shader": "shaderlab",
}

// gitignoreSimpleDirs reads the repo's root .gitignore and returns the set of
// plain top-level directory names it excludes (e.g. ".testbeds", "scratch-experiments").
// It deliberately ignores glob/nested patterns — this is a cheap fast-skip for the
// language scan, not a full gitignore engine.
func gitignoreSimpleDirs(repoRoot string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "!") {
			continue
		}
		l = strings.TrimSuffix(strings.TrimPrefix(l, "/"), "/")
		if l == "" || strings.ContainsAny(l, "*?[]") || strings.Contains(l, "/") {
			continue
		}
		out[l] = true
	}
	return out
}

// collectLangStats walks the repo (bounded depth, vendored/engine trees pruned),
// fills langs with the language ids present, and returns the GitHub-style byte
// breakdown (percent per language) plus the dominant language id.
func collectLangStats(repoRoot string, langs map[string]struct{}) ([]LanguageStat, string) {
	bytesByLang := map[string]int64{}
	// Honor the repo's own .gitignore for top-level directory excludes, so
	// gitignored vendored/fixture trees (e.g. codehelper's 14GB .testbeds/ of
	// reference repos) don't dominate the language breakdown — matching how GitHub
	// excludes ignored/vendored code.
	ignored := gitignoreSimpleDirs(repoRoot)
	_ = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		if d.IsDir() {
			base := filepath.Base(path)
			// Never skip the repo root itself (its base name may coincide with a
			// gitignore entry like "/codehelper" for the built binary).
			if rel != "." && ignored[base] {
				return filepath.SkipDir
			}
			switch base {
			case "node_modules", "vendor", ".vendor", ".git", ".codehelper",
				// Unity/Godot/Unreal generated + cache trees: huge and not source.
				"Library", "Temp", "Obj", "obj", "Build", "Binaries", "Intermediate", "DerivedDataCache", ".godot", "PackageCache",
				"dist", "build", "target", "__pycache__", ".venv", "venv",
				".gradle":
				return filepath.SkipDir
			}
			if strings.Count(rel, string(filepath.Separator)) > 8 {
				return filepath.SkipDir
			}
			return nil
		}
		lang, ok := langExt[strings.ToLower(filepath.Ext(path))]
		if !ok {
			return nil
		}
		langs[lang] = struct{}{}
		if info, ierr := d.Info(); ierr == nil {
			bytesByLang[lang] += info.Size()
		}
		return nil
	})

	var total int64
	for _, b := range bytesByLang {
		total += b
	}
	stats := make([]LanguageStat, 0, len(bytesByLang))
	for lang, b := range bytesByLang {
		pct := 0.0
		if total > 0 {
			pct = float64(int64(float64(b)/float64(total)*1000+0.5)) / 10 // one decimal place
		}
		stats = append(stats, LanguageStat{Language: lang, Percent: pct, Bytes: b})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Bytes != stats[j].Bytes {
			return stats[i].Bytes > stats[j].Bytes
		}
		return stats[i].Language < stats[j].Language
	})
	primary := pickPrimaryLanguage(stats)
	return stats, primary
}

// markupOrDataLangs are kept in language_stats for transparency but must not
// win primary_language when any real programming language is present (e.g.
// Spring Petclinic CSS bytes dominating Java).
var markupOrDataLangs = map[string]bool{
	"css": true, "html": true, "sql": true, "shell": true,
}

func pickPrimaryLanguage(stats []LanguageStat) string {
	if len(stats) == 0 {
		return ""
	}
	for _, s := range stats {
		if !markupOrDataLangs[s.Language] {
			return s.Language
		}
	}
	return stats[0].Language
}

func dedupe(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// Write generates and writes project_profile.json under .codehelper.
func Write(repoRoot string) (ProjectProfile, error) {
	p, err := Generate(repoRoot)
	if err != nil {
		return ProjectProfile{}, err
	}
	dir := paths.RepoIndexDir(repoRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ProjectProfile{}, err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return ProjectProfile{}, err
	}
	if err := os.WriteFile(Path(repoRoot), append(b, '\n'), 0o644); err != nil {
		return ProjectProfile{}, err
	}
	return p, nil
}

// Read loads project_profile.json if present.
func Read(repoRoot string) (*ProjectProfile, error) {
	b, err := os.ReadFile(Path(repoRoot))
	if err != nil {
		return nil, err
	}
	var p ProjectProfile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ReadOrGenerate returns the persisted profile, falling back to an in-memory
// Generate when the file is absent or unreadable. This keeps project_context
// correct for projects that were indexed before profiles were written at index
// time (or by an older binary) without forcing a reindex — the cost is one
// bounded-depth scan, acceptable for the once-per-session bootstrap.
func ReadOrGenerate(repoRoot string) (*ProjectProfile, error) {
	if p, err := Read(repoRoot); err == nil && p != nil {
		return p, nil
	}
	p, err := Generate(repoRoot)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
