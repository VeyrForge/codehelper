package ghrelease

import (
	"strings"
	"testing"
)

func TestParseChecksumLine(t *testing.T) {
	hash, base, ok := parseChecksumLine("deadbeefcafebabe0123456789abcdef0123456789abcdef0123456789abcdef  codehelper_windows_amd64.zip")
	if !ok || base != "codehelper_windows_amd64.zip" || !strings.HasPrefix(hash, "deadbeef") {
		t.Fatalf("got ok=%v hash=%q base=%q", ok, hash, base)
	}
	hash, base, ok = parseChecksumLine("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa *codehelper.exe")
	if !ok || base != "codehelper.exe" || hash != strings.Repeat("a", 64) {
		t.Fatalf("binary mode: ok=%v hash=%q base=%q", ok, hash, base)
	}
	if _, _, ok := parseChecksumLine("# comment"); ok {
		t.Fatal("comment must be skipped")
	}
	if _, _, ok := parseChecksumLine("onlyhash"); ok {
		t.Fatal("single field must be skipped")
	}
	if _, _, ok := parseChecksumLine(""); ok {
		t.Fatal("empty must be skipped")
	}
}

func FuzzParseChecksumLine(f *testing.F) {
	seeds := []string{
		"",
		"# comment",
		"deadbeef",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  codehelper.zip",
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb *codehelper.exe",
		"not-a-hash  file.zip",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\t./nested/codehelper.zip",
		"  cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc   name  ",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		hash, base, ok := parseChecksumLine(line)
		if !ok {
			return
		}
		if hash == "" || base == "" || base == "." {
			t.Fatalf("ok=true but empty parts: hash=%q base=%q line=%q", hash, base, line)
		}
		// Basename must not retain path separators (volume-like "C:" is OK on Windows).
		if strings.ContainsAny(base, `/\`) {
			t.Fatalf("base must not contain separators: %q", base)
		}
		if strings.HasPrefix(hash, "sha256:") {
			t.Fatalf("hash must strip sha256: prefix: %q", hash)
		}
		if validSHA256Hex(hash) && len(hash) != 64 {
			t.Fatalf("validSHA256Hex invariant broken for %q", hash)
		}
	})
}
