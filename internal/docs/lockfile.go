package docs

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// LockPin is an exact installed version read from a lockfile (not a constraint).
type LockPin struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Ecosystem string `json:"ecosystem"`
	Lockfile  string `json:"lockfile"`
	Dev       bool   `json:"dev,omitempty"`
}

// ListLockPins reads supported lockfiles under repoRoot and returns exact
// installed versions. Prefer these over manifest constraints for docs pinning.
func ListLockPins(repoRoot string) []LockPin {
	var pins []LockPin
	pins = append(pins, readPackageLock(repoRoot)...)
	pins = append(pins, readYarnLock(repoRoot)...)
	pins = append(pins, readPnpmLock(repoRoot)...)
	pins = append(pins, readGoSum(repoRoot)...)
	pins = append(pins, readCargoLock(repoRoot)...)
	pins = append(pins, readComposerLock(repoRoot)...)
	pins = append(pins, readPipfileLock(repoRoot)...)
	pins = append(pins, readPoetryLock(repoRoot)...)
	return dedupeLockPins(pins)
}

// ResolveLockVersion finds an exact lockfile pin for libName (same matching
// rules as ResolveVersion). Returns version, ecosystem, and lockfile path.
func ResolveLockVersion(repoRoot, libName string) (version, ecosystem, lockfile string) {
	want := strings.ToLower(strings.TrimSpace(libName))
	if want == "" {
		return "", "", ""
	}
	for _, p := range ListLockPins(repoRoot) {
		dn := strings.ToLower(p.Name)
		if dn == want || shortName(dn) == want || strings.HasSuffix(dn, "/"+want) {
			return p.Version, p.Ecosystem, p.Lockfile
		}
	}
	return "", "", ""
}

func dedupeLockPins(in []LockPin) []LockPin {
	seen := map[string]struct{}{}
	var out []LockPin
	for _, p := range in {
		if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Version) == "" {
			continue
		}
		key := p.Ecosystem + "|" + strings.ToLower(p.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out
}

// overlayLockPins replaces manifest Version/Raw with lockfile-exact pins when
// present, and sets Manifest to the lockfile name for transparency.
func overlayLockPins(deps []Dependency, pins []LockPin) []Dependency {
	byKey := map[string]LockPin{}
	for _, p := range pins {
		byKey[p.Ecosystem+"|"+strings.ToLower(p.Name)] = p
	}
	for i := range deps {
		key := deps[i].Ecosystem + "|" + strings.ToLower(deps[i].Name)
		if p, ok := byKey[key]; ok {
			deps[i].Version = p.Version
			deps[i].Raw = p.Version
			deps[i].Manifest = p.Lockfile
		}
	}
	// Also surface lock-only packages (transitive) that manifests omit? Keep
	// scope to declared deps — lock overlay only upgrades versions, doesn't
	// invent new top-level deps (avoids flooding docs resolution).
	return deps
}

func readPackageLock(repoRoot string) []LockPin {
	b, err := os.ReadFile(filepath.Join(repoRoot, "package-lock.json"))
	if err != nil {
		return nil
	}
	var root struct {
		Packages     map[string]struct {
			Version string `json:"version"`
			Dev     bool   `json:"dev"`
		} `json:"packages"`
		Dependencies map[string]struct {
			Version string `json:"version"`
			Dev     bool   `json:"dev"`
		} `json:"dependencies"`
	}
	if json.Unmarshal(b, &root) != nil {
		return nil
	}
	var out []LockPin
	// npm lockfileVersion >= 2: packages["node_modules/foo"] or "node_modules/@scope/pkg"
	for path, meta := range root.Packages {
		if path == "" || meta.Version == "" {
			continue
		}
		name := strings.TrimPrefix(path, "node_modules/")
		if name == path || name == "" {
			continue // root package entry
		}
		// Nested node_modules: take the last node_modules segment.
		if i := strings.LastIndex(name, "node_modules/"); i >= 0 {
			name = name[i+len("node_modules/"):]
		}
		out = append(out, LockPin{
			Name: name, Version: normalizeVersion(meta.Version), Ecosystem: "npm",
			Lockfile: "package-lock.json", Dev: meta.Dev,
		})
	}
	if len(out) > 0 {
		return out
	}
	// lockfileVersion 1: top-level dependencies map
	for name, meta := range root.Dependencies {
		if meta.Version == "" {
			continue
		}
		out = append(out, LockPin{
			Name: name, Version: normalizeVersion(meta.Version), Ecosystem: "npm",
			Lockfile: "package-lock.json", Dev: meta.Dev,
		})
	}
	return out
}

var yarnLockEntry = regexp.MustCompile(`^"?(@?[^@\s]+)@`)
var yarnVersionLine = regexp.MustCompile(`^\s+version\s+"([^"]+)"`)

func readYarnLock(repoRoot string) []LockPin {
	f, err := os.Open(filepath.Join(repoRoot, "yarn.lock"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []LockPin
	var current []string
	sc := bufio.NewScanner(f)
	// Yarn lockfiles can have long lines; raise the scanner buffer.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			current = nil
			header := strings.TrimSuffix(strings.TrimSpace(line), ":")
			for _, part := range strings.Split(header, ",") {
				part = strings.TrimSpace(part)
				if m := yarnLockEntry.FindStringSubmatch(part); m != nil {
					current = append(current, m[1])
				}
			}
			continue
		}
		if m := yarnVersionLine.FindStringSubmatch(line); m != nil && len(current) > 0 {
			ver := normalizeVersion(m[1])
			seen := map[string]struct{}{}
			for _, name := range current {
				if _, ok := seen[name]; ok {
					continue
				}
				seen[name] = struct{}{}
				out = append(out, LockPin{
					Name: name, Version: ver, Ecosystem: "npm", Lockfile: "yarn.lock",
				})
			}
			current = nil
		}
	}
	return out
}

var pnpmPkgKey = regexp.MustCompile(`^'?(@?[^'@\s/]+(?:/[^'@\s]+)?)@([^':\s]+)'?:?\s*$`)
var pnpmSlashKey = regexp.MustCompile(`^'?/@?([^'@\s]+(?:/[^'@\s]+)?)/([^'/:\s]+)'?:?\s*$`)

func readPnpmLock(repoRoot string) []LockPin {
	b, err := os.ReadFile(filepath.Join(repoRoot, "pnpm-lock.yaml"))
	if err != nil {
		return nil
	}
	var out []LockPin
	inPackages := false
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "packages:") {
			inPackages = true
			continue
		}
		if inPackages && len(line) > 0 && line[0] != ' ' && line[0] != '\t' && !strings.HasPrefix(line, "packages") {
			inPackages = false
		}
		if !inPackages {
			continue
		}
		t := strings.TrimSpace(line)
		if !strings.HasSuffix(t, ":") {
			continue
		}
		key := strings.TrimSuffix(t, ":")
		key = strings.Trim(key, "'\"")
		name, ver := "", ""
		if m := pnpmPkgKey.FindStringSubmatch(key); m != nil {
			name, ver = m[1], m[2]
		} else if m := pnpmSlashKey.FindStringSubmatch(key); m != nil {
			name, ver = m[1], m[2]
			// pnpm v6 slash form: /name/version or /@scope/name/version
			if strings.Count(key, "/") >= 3 && strings.HasPrefix(key, "/@") {
				// /@scope/pkg/1.2.3 → name=@scope/pkg
				parts := strings.Split(strings.TrimPrefix(key, "/"), "/")
				if len(parts) >= 3 {
					name = parts[0] + "/" + parts[1]
					ver = parts[2]
				}
			}
		}
		ver = normalizeVersion(ver)
		if name == "" || ver == "" {
			continue
		}
		out = append(out, LockPin{
			Name: name, Version: ver, Ecosystem: "npm", Lockfile: "pnpm-lock.yaml",
		})
	}
	return out
}

func readGoSum(repoRoot string) []LockPin {
	f, err := os.Open(filepath.Join(repoRoot, "go.sum"))
	if err != nil {
		return nil
	}
	defer f.Close()
	best := map[string]string{} // module → version (first / highest seen; go.sum lists all)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		mod, ver := fields[0], fields[1]
		if strings.HasSuffix(ver, "/go.mod") {
			ver = strings.TrimSuffix(ver, "/go.mod")
		}
		ver = strings.TrimPrefix(ver, "v")
		if ver == "" {
			continue
		}
		// Prefer the first concrete version; go.sum may list many — keep the
		// one that matches go.mod via later overlay (ResolveVersion prefers
		// lock then manifest). Storing first is fine for pinning docs.
		if _, ok := best[mod]; !ok {
			best[mod] = ver
		}
	}
	var out []LockPin
	for mod, ver := range best {
		out = append(out, LockPin{
			Name: mod, Version: ver, Ecosystem: "go", Lockfile: "go.sum",
		})
	}
	return out
}

func readCargoLock(repoRoot string) []LockPin {
	b, err := os.ReadFile(filepath.Join(repoRoot, "Cargo.lock"))
	if err != nil {
		return nil
	}
	var out []LockPin
	var name, version string
	flush := func() {
		if name != "" && version != "" {
			out = append(out, LockPin{
				Name: name, Version: normalizeVersion(version), Ecosystem: "cargo",
				Lockfile: "Cargo.lock",
			})
		}
		name, version = "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == "[[package]]" {
			flush()
			continue
		}
		if strings.HasPrefix(t, "name = ") {
			name = strings.Trim(strings.TrimPrefix(t, "name = "), "\"")
		}
		if strings.HasPrefix(t, "version = ") {
			version = strings.Trim(strings.TrimPrefix(t, "version = "), "\"")
		}
	}
	flush()
	return out
}

func readComposerLock(repoRoot string) []LockPin {
	b, err := os.ReadFile(filepath.Join(repoRoot, "composer.lock"))
	if err != nil {
		return nil
	}
	var root struct {
		Packages    []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"packages"`
		PackagesDev []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"packages-dev"`
	}
	if json.Unmarshal(b, &root) != nil {
		return nil
	}
	var out []LockPin
	add := func(name, ver string, dev bool) {
		if name == "" || name == "php" || strings.HasPrefix(name, "ext-") {
			return
		}
		out = append(out, LockPin{
			Name: name, Version: normalizeVersion(ver), Ecosystem: "composer",
			Lockfile: "composer.lock", Dev: dev,
		})
	}
	for _, p := range root.Packages {
		add(p.Name, p.Version, false)
	}
	for _, p := range root.PackagesDev {
		add(p.Name, p.Version, true)
	}
	return out
}

func readPipfileLock(repoRoot string) []LockPin {
	b, err := os.ReadFile(filepath.Join(repoRoot, "Pipfile.lock"))
	if err != nil {
		return nil
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(b, &root) != nil {
		return nil
	}
	var out []LockPin
	parseSection := func(key string, dev bool) {
		raw, ok := root[key]
		if !ok {
			return
		}
		var section map[string]struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &section) != nil {
			return
		}
		for name, meta := range section {
			ver := strings.TrimPrefix(strings.TrimSpace(meta.Version), "==")
			ver = normalizeVersion(ver)
			if ver == "" {
				continue
			}
			out = append(out, LockPin{
				Name: name, Version: ver, Ecosystem: "pip",
				Lockfile: "Pipfile.lock", Dev: dev,
			})
		}
	}
	parseSection("default", false)
	parseSection("develop", true)
	return out
}

var poetryNameLine = regexp.MustCompile(`^name\s*=\s*"([^"]+)"`)
var poetryVersionLine = regexp.MustCompile(`^version\s*=\s*"([^"]+)"`)

func readPoetryLock(repoRoot string) []LockPin {
	b, err := os.ReadFile(filepath.Join(repoRoot, "poetry.lock"))
	if err != nil {
		return nil
	}
	var out []LockPin
	var name, version string
	flush := func() {
		if name != "" && version != "" {
			out = append(out, LockPin{
				Name: name, Version: normalizeVersion(version), Ecosystem: "pip",
				Lockfile: "poetry.lock",
			})
		}
		name, version = "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == "[[package]]" {
			flush()
			continue
		}
		if m := poetryNameLine.FindStringSubmatch(t); m != nil {
			name = m[1]
		}
		if m := poetryVersionLine.FindStringSubmatch(t); m != nil {
			version = m[1]
		}
	}
	flush()
	return out
}
