package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportSARIF(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sample.sarif")
	content := `{"runs":[{"tool":{"driver":{"name":"semgrep"}},"results":[{"ruleId":"sql-injection","level":"high","message":{"text":"bad"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"internal/api/users.go"}}}]}]}]}`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write sarif: %v", err)
	}
	issues, err := ImportSARIF(p)
	if err != nil {
		t.Fatalf("ImportSARIF: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Tool != "semgrep" {
		t.Fatalf("unexpected tool: %s", issues[0].Tool)
	}
}
