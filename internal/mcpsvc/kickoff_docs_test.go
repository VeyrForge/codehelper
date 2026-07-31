package mcpsvc

import "testing"

func TestLibrariesFromRelevantDocs_ScopedNpm(t *testing.T) {
	relevant := []string{
		"@nestjs/core@10.0.0 — call `docs` for version-correct API before coding against it",
		"github.com/spf13/cobra@v1.10.2 — call `docs` for version-correct API before coding against it",
	}
	got := librariesFromRelevantDocs(relevant, nil)
	if len(got) != 2 || got[0] != "@nestjs/core" || got[1] != "github.com/spf13/cobra" {
		t.Fatalf("got %v want [@nestjs/core github.com/spf13/cobra]", got)
	}
	if n := depNameOnly("@scope/pkg@1.2.3"); n != "@scope/pkg" {
		t.Errorf("depNameOnly scoped = %q", n)
	}
	if n := depNameOnly("react@18.2.0"); n != "react" {
		t.Errorf("depNameOnly plain = %q", n)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Errorf("short = %q", got)
	}
	if got := truncateRunes("abcdefghij", 5); got != "abcde…" {
		t.Errorf("trunc = %q", got)
	}
}
