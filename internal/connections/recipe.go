package connections

import (
	"fmt"
	"strings"

	"github.com/VeyrForge/codehelper/internal/verify"
)

// Recipe is a named, parameterized remote command template for an SSH host.
// The agent picks a recipe by name — never free-form argv.
type Recipe struct {
	Name     string   `json:"name"`
	Argv     []string `json:"argv"`
	Params   []string `json:"params,omitempty"`
	ReadOnly bool     `json:"read_only,omitempty"`
}

// AddRecipe validates and upserts a recipe on an SSH host by name.
func (c *Config) AddRecipe(hostName string, r Recipe) error {
	hostName = strings.TrimSpace(hostName)
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fmt.Errorf("recipe name is required")
	}
	if len(r.Argv) == 0 {
		return fmt.Errorf("recipe %q needs argv", r.Name)
	}
	if blocked, reason := verify.SSHAllowlistBlocked(strings.TrimSpace(r.Argv[0])); blocked {
		return fmt.Errorf("recipe %q: %s", r.Name, reason)
	}
	h := c.FindSSH(hostName)
	if h == nil {
		return fmt.Errorf("ssh host %q not found", hostName)
	}
	idx := -1
	for i := range c.SSHHosts {
		if strings.EqualFold(c.SSHHosts[i].Name, hostName) {
			idx = i
			break
		}
	}
	out := c.SSHHosts[idx].Recipes[:0]
	for _, x := range c.SSHHosts[idx].Recipes {
		if !strings.EqualFold(x.Name, r.Name) {
			out = append(out, x)
		}
	}
	c.SSHHosts[idx].Recipes = append(out, r)
	return nil
}

// FindRecipe returns a recipe on the named host, or nil.
func (c *Config) FindRecipe(hostName, recipeName string) (*SSHHost, *Recipe) {
	h := c.FindSSH(hostName)
	if h == nil {
		return nil, nil
	}
	for i := range h.Recipes {
		if strings.EqualFold(h.Recipes[i].Name, recipeName) {
			return h, &h.Recipes[i]
		}
	}
	return h, nil
}

// ExpandRecipe substitutes {param} placeholders in argv. Param values must be
// alphanumeric plus _-. only — no shell metacharacters.
func ExpandRecipe(r Recipe, params map[string]string) ([]string, error) {
	out := make([]string, len(r.Argv))
	for i, tok := range r.Argv {
		s := tok
		for k, v := range params {
			v = strings.TrimSpace(v)
			if v != "" && !safeParamValue(v) {
				return nil, fmt.Errorf("unsafe param %q value", k)
			}
			s = strings.ReplaceAll(s, "{"+k+"}", v)
		}
		if strings.Contains(s, "{") || strings.Contains(s, "}") {
			return nil, fmt.Errorf("recipe %q: missing param substitution in %q", r.Name, tok)
		}
		out[i] = s
	}
	if blocked, reason := verify.SSHAllowlistBlocked(strings.TrimSpace(out[0])); blocked {
		return nil, fmt.Errorf("recipe %q: %s", r.Name, reason)
	}
	return out, nil
}

func safeParamValue(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '/':
		default:
			return false
		}
	}
	return true
}
