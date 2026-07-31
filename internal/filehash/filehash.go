package filehash

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// OfFile returns hex sha256 of file contents.
func OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// OfBytes returns hex sha256 of an in-memory buffer. It is byte-for-byte
// identical to OfFile over the same contents, so a hash produced here during
// ingest matches one OfFile produces later during change detection — callers
// that already hold the file contents should use this to avoid a second read.
func OfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
