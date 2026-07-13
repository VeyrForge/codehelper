package indexer

import (
	"encoding/json"
	"io"
)

type progressEvt struct {
	ChProgress          bool   `json:"_ch_progress"`
	Phase               string `json:"phase"`
	Current             int    `json:"current,omitempty"`
	Total               int    `json:"total,omitempty"`
	SkippedByGitignore  int    `json:"skipped_by_gitignore,omitempty"`
	EligibleSourceFiles int    `json:"eligible_source_files,omitempty"`
	Path                string `json:"path,omitempty"`
	Message             string `json:"message,omitempty"`
}

// WriteProgressJSON emits one newline-terminated JSON object for IDE progress bars (--progress-json on stderr).
func WriteProgressJSON(w io.Writer, phase string, current, total, skippedGit, eligible int, path, msg string) {
	if w == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(progressEvt{
		ChProgress:          true,
		Phase:               phase,
		Current:             current,
		Total:               total,
		SkippedByGitignore:  skippedGit,
		EligibleSourceFiles: eligible,
		Path:                path,
		Message:             msg,
	})
}
