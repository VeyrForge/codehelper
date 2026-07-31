package skills

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed data/*
var data embed.FS

type manifest struct {
	Version string `json:"version"`
}

// Install copies embedded skills into destDir (e.g. ~/.cursor/skills).
func Install(destDir string) error {
	mf, _ := loadManifest()
	if mf.Version != "" {
		stampPath := filepath.Join(destDir, ".codehelper-skills-version")
		if b, err := os.ReadFile(stampPath); err == nil && strings.TrimSpace(string(b)) == mf.Version {
			return nil
		}
	}

	return fs.WalkDir(data, "data", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel("data", path)
		if err != nil {
			return err
		}
		out := filepath.Join(destDir, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		b, err := data.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return err
		}
		if path == "data/manifest.json" && mf.Version != "" {
			stampPath := filepath.Join(destDir, ".codehelper-skills-version")
			if err := os.WriteFile(stampPath, []byte(mf.Version+"\n"), 0o644); err != nil {
				return err
			}
		}
		return nil
	})
}

func loadManifest() (manifest, error) {
	var out manifest
	b, err := data.ReadFile("data/manifest.json")
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return manifest{}, err
	}
	return out, nil
}
