// Package secrets stores connection passwords encrypted at rest, OUTSIDE the
// repository, and hands them out only to the local codehelper process — never to
// the LLM/agent. This is the trust boundary for the connections feature: the
// agent sees that a profile HAS a secret (a bool) and whether it's enabled, but
// the plaintext lives only here.
//
// Design:
//   - Master key: ~/.codehelper/secret.key (0600), 32 random bytes, generated
//     once per machine. Global, never in a repo, independent of the index home.
//   - Per-repo store: ~/.codehelper/secrets/<hash(repoRoot)>.json (0600), a map of
//     profile name -> AES-256-GCM ciphertext (base64 of nonce||ciphertext).
//     Keying by a hash of the repo root guarantees secrets never touch the repo
//     even when the index itself lives in-repo (CODEHELPER_INDEX_HOME unset).
//
// Only Get/Set/Delete/Has/Names are exported; there is no bulk-export of
// plaintext, and nothing here is surfaced through an MCP tool response.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
)

const keyFilename = "secret.key"

// keyPath is the master-key location under the global config dir.
func keyPath() (string, error) {
	dir, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, keyFilename), nil
}

// storePath is the per-repo encrypted secret file, keyed by a hash of the repo
// root so it lives globally and never in the project tree.
func storePath(repoRoot string) (string, error) {
	dir, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(repoRoot)))
	return filepath.Join(dir, "secrets", hex.EncodeToString(sum[:8])+".json"), nil
}

// loadOrCreateKey returns the 32-byte master key, generating it (0600) on first
// use. A key file with the wrong size is a hard error, not a silent regen, so a
// truncated key can't quietly orphan every stored secret.
func loadOrCreateKey() ([]byte, error) {
	p, err := keyPath()
	if err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(p); err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("secrets: master key %s is corrupt (%d bytes, want 32)", p, len(b))
		}
		return b, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func gcm() (cipher.AEAD, error) {
	key, err := loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encrypt(plaintext string) (string, error) {
	aead, err := gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func decrypt(enc string) (string, error) {
	aead, err := gcm()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	ns := aead.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("secrets: ciphertext too short")
	}
	pt, err := aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("secrets: decrypt failed (wrong key or corrupt store): %w", err)
	}
	return string(pt), nil
}

func loadStore(repoRoot string) (map[string]string, string, error) {
	p, err := storePath(repoRoot)
	if err != nil {
		return nil, "", err
	}
	m := map[string]string{}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return m, p, nil
		}
		return nil, p, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, p, fmt.Errorf("secrets: malformed store %s: %w", p, err)
	}
	return m, p, nil
}

func saveStore(p string, m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Set stores (encrypts) the plaintext secret for a profile name. An empty
// plaintext deletes the entry, so `set --password ""` clears a secret.
func Set(repoRoot, name, plaintext string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("secret name is required")
	}
	if plaintext == "" {
		_, err := Delete(repoRoot, name)
		return err
	}
	m, p, err := loadStore(repoRoot)
	if err != nil {
		return err
	}
	enc, err := encrypt(plaintext)
	if err != nil {
		return err
	}
	m[name] = enc
	return saveStore(p, m)
}

// Get returns the decrypted secret for a profile, or ok=false when none is
// stored. This is the ONLY read path and is for the local process at connect
// time — it is never wired into an MCP tool response.
func Get(repoRoot, name string) (string, bool, error) {
	m, _, err := loadStore(repoRoot)
	if err != nil {
		return "", false, err
	}
	enc, ok := m[name]
	if !ok {
		return "", false, nil
	}
	pt, err := decrypt(enc)
	if err != nil {
		return "", false, err
	}
	return pt, true, nil
}

// Delete removes a stored secret, reporting whether one existed.
func Delete(repoRoot, name string) (bool, error) {
	m, p, err := loadStore(repoRoot)
	if err != nil {
		return false, err
	}
	if _, ok := m[name]; !ok {
		return false, nil
	}
	delete(m, name)
	return true, saveStore(p, m)
}

// Has reports whether a secret is stored for name (no decryption). Safe to use
// when building the non-secret brief the agent sees.
func Has(repoRoot, name string) bool {
	m, _, err := loadStore(repoRoot)
	if err != nil {
		return false
	}
	_, ok := m[name]
	return ok
}

// Names lists profile names that have a stored secret, sorted. Names are not
// secret; the values they map to are.
func Names(repoRoot string) []string {
	m, _, err := loadStore(repoRoot)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
