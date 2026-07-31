package ops

import (
	"github.com/VeyrForge/codehelper/internal/connections"
)

// RemoteListResult summarizes configured ops capabilities (read-only).
type RemoteListResult struct {
	SSHHosts   []sshBrief   `json:"ssh_hosts,omitempty"`
	Databases  []dbBrief    `json:"databases,omitempty"`
	LogSources []logBrief   `json:"log_sources,omitempty"`
	Aliases    []aliasBrief `json:"aliases,omitempty"`
	Note       string       `json:"note,omitempty"`
}

type sshBrief struct {
	Name            string   `json:"name"`
	Hostname        string   `json:"hostname"`
	Enabled         bool     `json:"enabled"`
	AllowedCommands []string `json:"allowed_commands,omitempty"`
	Recipes         []string `json:"recipes,omitempty"`
}

type dbBrief struct {
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	ReadOnly bool   `json:"read_only,omitempty"`
	Enabled  bool   `json:"enabled"`
}

type logBrief struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	SSHHost string `json:"ssh_host,omitempty"`
	Enabled bool   `json:"enabled"`
}

type aliasBrief struct {
	Name         string `json:"name"`
	Local        bool   `json:"local,omitempty"`
	RemoteHost   string `json:"remote_host,omitempty"`
	RemoteRecipe string `json:"remote_recipe,omitempty"`
}

// ListCapabilities returns non-secret ops metadata for remote_list.
func ListCapabilities(repoRoot string) (*RemoteListResult, error) {
	cfg, err := connections.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	out := &RemoteListResult{Note: "read-only capability map — configure via `codehelper connections` CLI; secrets never listed"}
	for _, h := range cfg.SSHHosts {
		sb := sshBrief{Name: h.Name, Hostname: h.Hostname, Enabled: h.Enabled(), AllowedCommands: h.AllowedCommands}
		for _, r := range h.Recipes {
			sb.Recipes = append(sb.Recipes, r.Name)
		}
		out.SSHHosts = append(out.SSHHosts, sb)
	}
	for _, d := range cfg.Databases {
		out.Databases = append(out.Databases, dbBrief{Name: d.Name, Driver: d.Driver, ReadOnly: d.ReadOnly, Enabled: d.Enabled()})
	}
	for _, l := range cfg.LogSources {
		out.LogSources = append(out.LogSources, logBrief{Name: l.Name, Kind: l.Kind, SSHHost: l.SSHHost, Enabled: !l.Disabled})
	}
	for _, a := range cfg.Aliases {
		ab := aliasBrief{Name: a.Name, RemoteHost: a.RemoteHost, RemoteRecipe: a.RemoteRecipe}
		ab.Local = a.RemoteHost == "" && len(a.Argv) > 0
		out.Aliases = append(out.Aliases, ab)
	}
	return out, nil
}
