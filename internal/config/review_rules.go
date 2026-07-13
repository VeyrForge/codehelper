package config

import (
	"encoding/json"
	"os"
)

type LayerRule struct {
	Name          string   `json:"name"`
	Path          string   `json:"path"`
	MayImport     []string `json:"may_import,omitempty"`
	MustNotImport []string `json:"must_not_import,omitempty"`
}

type ReviewRules struct {
	Layers []LayerRule `json:"layers,omitempty"`
}

func LoadReviewRules(path string) (ReviewRules, error) {
	var out ReviewRules
	b, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, err
	}
	return out, nil
}
