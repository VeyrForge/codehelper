package eval

// IndexMode controls pre-benchmark index refresh behavior.
type IndexMode int

const (
	IndexAuto IndexMode = iota
	IndexSkip
	IndexForce
)

// Config tunes one benchmark variant (format + index policy).
type Config struct {
	IndexMode         IndexMode
	ManualFormat      string // toon | json
	OrchestrateFormat string // toon | json
}

// DefaultConfig is the recommended production-like variant.
func DefaultConfig() Config {
	return Config{IndexMode: IndexAuto, ManualFormat: "toon", OrchestrateFormat: "toon"}
}

// NamedConfig pairs a variant label with its config.
type NamedConfig struct {
	Name   string `json:"name"`
	Config Config `json:"config"`
}

// AllVariants returns every benchmark permutation we track.
func AllVariants() []NamedConfig {
	return []NamedConfig{
		{Name: "fresh_index_toon", Config: Config{IndexAuto, "toon", "toon"}},
		{Name: "fresh_index_manual_json", Config: Config{IndexAuto, "json", "toon"}},
		{Name: "skip_index_toon", Config: Config{IndexSkip, "toon", "toon"}},
		{Name: "force_index_toon", Config: Config{IndexForce, "toon", "toon"}},
		{Name: "orch_response_json", Config: Config{IndexAuto, "toon", "json"}},
	}
}

// ResolveVariants picks named variants or all.
func ResolveVariants(names []string) []NamedConfig {
	if len(names) == 0 {
		return []NamedConfig{{Name: "fresh_index_toon", Config: DefaultConfig()}}
	}
	if len(names) == 1 && (names[0] == "all" || names[0] == "*") {
		return AllVariants()
	}
	byName := map[string]NamedConfig{}
	for _, v := range AllVariants() {
		byName[v.Name] = v
	}
	var out []NamedConfig
	for _, n := range names {
		if v, ok := byName[n]; ok {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return []NamedConfig{{Name: "fresh_index_toon", Config: DefaultConfig()}}
	}
	return out
}
