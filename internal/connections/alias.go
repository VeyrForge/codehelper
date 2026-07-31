package connections

import (
	"fmt"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/verify"
)

// CommandAlias is a declarative argv expansion (safe substitute for shell aliases).
type CommandAlias struct {
	Name             string   `json:"name"`
	Argv             []string `json:"argv,omitempty"`
	Cwd              string   `json:"cwd,omitempty"`
	RemoteHost       string   `json:"remote_host,omitempty"`
	RemoteRecipe     string   `json:"remote_recipe,omitempty"`
	RequiresApproval bool     `json:"requires_approval,omitempty"`
}

// AddAlias validates and upserts a command alias by name.
func (c *Config) AddAlias(a CommandAlias) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return fmt.Errorf("alias name is required")
	}
	if a.RemoteHost != "" && a.RemoteRecipe != "" {
		if c.FindSSH(a.RemoteHost) == nil {
			return fmt.Errorf("remote_host %q not configured", a.RemoteHost)
		}
		if _, r := c.FindRecipe(a.RemoteHost, a.RemoteRecipe); r == nil {
			return fmt.Errorf("remote_recipe %q not found on host %q", a.RemoteRecipe, a.RemoteHost)
		}
	} else if len(a.Argv) == 0 {
		return fmt.Errorf("alias %q needs argv or remote_host+remote_recipe", a.Name)
	} else if blocked, reason := verify.CommandBlocked(a.Argv); blocked {
		return fmt.Errorf("alias %q: %s", a.Name, reason)
	}
	out := c.Aliases[:0]
	for _, x := range c.Aliases {
		if !strings.EqualFold(x.Name, a.Name) {
			out = append(out, x)
		}
	}
	c.Aliases = append(out, a)
	sort.Slice(c.Aliases, func(i, j int) bool { return c.Aliases[i].Name < c.Aliases[j].Name })
	return nil
}

// RemoveAlias deletes an alias by name.
func (c *Config) RemoveAlias(name string) bool {
	name = strings.TrimSpace(name)
	removed := false
	out := c.Aliases[:0]
	for _, x := range c.Aliases {
		if strings.EqualFold(x.Name, name) {
			removed = true
			continue
		}
		out = append(out, x)
	}
	c.Aliases = out
	return removed
}

// FindAlias returns the alias with the given name, or nil.
func (c *Config) FindAlias(name string) *CommandAlias {
	for i := range c.Aliases {
		if strings.EqualFold(c.Aliases[i].Name, name) {
			return &c.Aliases[i]
		}
	}
	return nil
}
