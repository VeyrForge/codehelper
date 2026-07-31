package filehash

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOfBytesMatchesOfFile locks in the invariant the indexer relies on: a hash
// computed from an in-memory buffer during ingest must equal the hash OfFile
// computes later during change detection, or incremental indexing breaks.
func TestOfBytesMatchesOfFile(t *testing.T) {
	cases := map[string][]byte{
		"empty":         {},
		"ascii":         []byte("package main\n\nfunc main() {}\n"),
		"binary":        {0x00, 0x01, 0x02, 0xff, 0xfe, 0x7f},
		"newline heavy": []byte("a\nb\r\nc\n\n"),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(p, content, 0o644); err != nil {
				t.Fatal(err)
			}
			fromFile, err := OfFile(p)
			if err != nil {
				t.Fatal(err)
			}
			fromBytes := OfBytes(content)
			if fromFile != fromBytes {
				t.Fatalf("hash mismatch: OfFile=%s OfBytes=%s", fromFile, fromBytes)
			}
		})
	}
}

func TestOfBytesEmpty(t *testing.T) {
	// Known sha256 of the empty input.
	const want = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := OfBytes(nil); got != want {
		t.Fatalf("OfBytes(nil) = %s, want %s", got, want)
	}
}

// Benchmarks the marginal per-file cost the indexer eliminated: the old worker
// did os.Stat + OfFile (a second open+full read) on top of the ReadFile that
// parsing already needs; the new worker hashes the buffer it already holds.
func benchFile(b *testing.B) string {
	p := filepath.Join(b.TempDir(), "f.go")
	buf := make([]byte, 16*1024) // ~typical source file
	for i := range buf {
		buf[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		b.Fatal(err)
	}
	return p
}

func BenchmarkOldPath_StatPlusOfFile(b *testing.B) {
	p := benchFile(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := os.Stat(p); err != nil {
			b.Fatal(err)
		}
		if _, err := OfFile(p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNewPath_OfBytes(b *testing.B) {
	p := benchFile(b)
	buf, err := os.ReadFile(p) // parse already holds this; hashing it is free I/O
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = OfBytes(buf)
		_ = int64(len(buf))
	}
}
