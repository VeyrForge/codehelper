package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstLineContainingFile_PrefersClassOverImport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api_key.py")
	src := "" +
		"from typing import Annotated\n" +
		"\n" +
		"from fastapi.openapi.models import APIKey, APIKeyIn\n" +
		"from fastapi.security.base import SecurityBase\n" +
		"\n" +
		"class APIKeyBase(SecurityBase):\n" +
		"    model: APIKey\n" +
		"\n" +
		"class APIKeyQuery(APIKeyBase):\n" +
		"    pass\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	line := firstLineContainingFile(path, "APIKey")
	if line != 6 && line != 9 {
		t.Fatalf("expected class definition line (6 or 9), got %d (import is 3)", line)
	}
	if line2 := firstLineContainingFile(path, "class APIKeyQuery"); line2 != 9 {
		t.Fatalf("expected class APIKeyQuery at 9, got %d", line2)
	}
}

func TestLibrarySecurityGuidance_FastAPICitesDefinition(t *testing.T) {
	parent := t.TempDir()
	named := filepath.Join(parent, "fastapi")
	sec := filepath.Join(named, "fastapi", "security")
	if err := os.MkdirAll(sec, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sec, "api_key.py"), []byte(""+
		"from fastapi.openapi.models import APIKey, APIKeyIn\n"+
		"\n"+
		"class APIKeyQuery:\n"+
		"    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sec, "oauth2.py"), []byte(""+
		"from fastapi.openapi.models import OAuth2 as OAuth2Model\n"+
		"\n"+
		"class OAuth2PasswordBearer:\n"+
		"    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := LibrarySecurityGuidance(named, ShapeFrameworkCore, 4)
	if len(findings) == 0 {
		t.Fatal("expected library security findings")
	}
	var sawAPIKey bool
	for _, f := range findings {
		if strings.Contains(f.File, "api_key") {
			sawAPIKey = true
			if f.Line < 3 {
				t.Fatalf("api_key cite should be class def not import, got line %d evidence=%q", f.Line, f.Evidence)
			}
		}
		if strings.Contains(f.File, "oauth2") && f.Line < 3 {
			t.Fatalf("oauth2 cite should be class def not import, got line %d", f.Line)
		}
	}
	if !sawAPIKey {
		t.Fatalf("expected api_key finding, got %+v", findings)
	}
}
