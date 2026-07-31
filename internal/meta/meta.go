package meta

import (
	"encoding/json"
	"os"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
)

const SchemaVersion = 2

// Data is persisted to .codehelper/meta.json.
type Data struct {
	SchemaVersion int       `json:"schema_version"`
	ParserVersion int       `json:"parser_version"`
	RepoName      string    `json:"repo_name"`
	RootPath      string    `json:"root_path"`
	LastCommit    string    `json:"last_commit"`
	IndexedAt     time.Time `json:"indexed_at"`
	SymbolCount   int       `json:"symbol_count"`
	EdgeCount     int       `json:"edge_count"`
	FileCount     int       `json:"file_count"`
}

// Read loads meta from repo root.
func Read(repoRoot string) (*Data, error) {
	p := paths.MetaPath(repoRoot)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var d Data
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Write saves meta.
func Write(repoRoot string, d *Data) error {
	if err := os.MkdirAll(paths.RepoIndexDir(repoRoot), 0o755); err != nil {
		return err
	}
	d.SchemaVersion = SchemaVersion
	d.IndexedAt = time.Now().UTC()
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := paths.MetaPath(repoRoot) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, paths.MetaPath(repoRoot))
}
