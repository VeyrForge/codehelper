package usage

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestRedactSensitive(t *testing.T) {
	in := `password=hunter2 token: abc123 "api_key":"xyz" Authorization=BearerZ connection_string=postgres://u:p@h/db cookie='sess=1' dsn=secretval`
	out := RedactSensitive(in)
	for _, leak := range []string{"hunter2", "abc123", "xyz", "BearerZ", "postgres://", "sess=1", "secretval"} {
		if strings.Contains(out, leak) {
			t.Fatalf("secret %q still present in %q", leak, out)
		}
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected redaction markers, got %q", out)
	}
}

func TestPreviewRedacts(t *testing.T) {
	got := Preview(`{"password":"secret","ok":true}`, 200)
	if strings.Contains(got, `"secret"`) || strings.Contains(got, "password\":\"secret") {
		t.Fatalf("preview leaked secret: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redaction in preview: %q", got)
	}
}

func TestAppendUsesPrivatePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not meaningful on Windows")
	}
	root := t.TempDir()
	if err := Append(root, Event{Tool: "query", Args: Preview(`password=x`, MaxArgsChars)}); err != nil {
		t.Fatal(err)
	}
	dir := Dir(root)
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("usage dir should be private (0700), got %o", fi.Mode().Perm())
	}
	p := EventsPath(root)
	fi, err = os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("events file should be private (0600), got %o", fi.Mode().Perm())
	}
}
