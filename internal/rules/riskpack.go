// Package rules loads optional framework rule packs from .codehelper/.
package rules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// RiskPattern is an executable check template for patch review.
type RiskPattern struct {
	ID          string   `json:"id"`
	Match       string   `json:"match,omitempty"`
	Requires    string   `json:"requires,omitempty"`
	RequiresAny []string `json:"requires_any,omitempty"`
	MatchAny    []string `json:"match_any,omitempty"`
	Severity    string   `json:"severity"`
}

// Pack is rules-<name>.json merged schema (backward compatible).
type Pack struct {
	Name         string        `json:"name"`
	Checks       []string      `json:"checks,omitempty"`
	RiskPatterns []RiskPattern `json:"risk_patterns,omitempty"`
}

// LoadInstalledPacks reads all .codehelper/rules-*.json under repoRoot.
func LoadInstalledPacks(repoRoot string) ([]Pack, []RiskPattern, error) {
	dir := paths.RepoIndexDir(repoRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var packs []Pack
	var flat []RiskPattern
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "rules-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var p Pack
		if err := json.Unmarshal(b, &p); err != nil {
			continue
		}
		packs = append(packs, p)
		for _, rp := range p.RiskPatterns {
			if strings.TrimSpace(rp.ID) == "" {
				continue
			}
			flat = append(flat, rp)
		}
	}
	return packs, flat, nil
}
