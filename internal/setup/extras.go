package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/VeyrForge/codehelper/internal/green"
	"github.com/VeyrForge/codehelper/internal/web"
)

// EnsureBrowser provisions the managed Chromium used by the browser MCP tool.
// Best-effort: failures are returned so callers can log and continue setup.
func EnsureBrowser(ctx context.Context) error {
	if !web.BrowserAvailable() {
		return nil
	}
	if _, err := web.EnsureBrowser(ctx); err != nil {
		return fmt.Errorf("browser: %w", err)
	}
	if _, err := web.EnsureAxe(ctx); err != nil {
		return fmt.Errorf("browser axe-core: %w", err)
	}
	return nil
}

// EnsureGreenConfig writes an embed-only ~/.codehelper/green.json when ge is on
// PATH (or in binDir) and no config exists yet. Embed-only keeps first-use size
// ~195 MB (Granite) instead of pulling a multi-GB chat GGUF. Does not start
// servers — the model downloads on the user's first `codehelper green start`,
// `bash scripts/install-local-embed.sh`, or MCP connect. See docs/LOCAL_EMBED.md.
func EnsureGreenConfig(binDir string) (bool, error) {
	_, ok, err := green.Load()
	if err != nil {
		return false, err
	}
	if ok {
		return false, nil
	}
	geCmd := "ge"
	if binDir != "" {
		if p := filepath.Join(binDir, "ge"); fileExecutable(p) {
			geCmd = p
		} else if p := filepath.Join(binDir, "ge.exe"); fileExecutable(p) {
			geCmd = p
		}
	}
	if geCmd == "ge" && !hasExecutable("ge", binDir) {
		return false, nil
	}
	if err := green.Save(green.DefaultEmbedOnlyConfig(geCmd)); err != nil {
		return false, err
	}
	return true, nil
}

func hasExecutable(name, binDir string) bool {
	if binDir != "" {
		ext := ""
		if filepath.Ext(name) == "" && os.PathSeparator == '\\' {
			ext = ".exe"
		}
		if p := filepath.Join(binDir, name+ext); fileExecutable(p) {
			return true
		}
	}
	if p, err := exec.LookPath(name); err == nil && fileExecutable(p) {
		return true
	}
	return false
}

func fileExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	// Windows does not honor Unix execute bits; presence of a regular file is
	// enough for install-time ge / ge.exe detection.
	if os.PathSeparator == '\\' {
		return true
	}
	return fi.Mode()&0o111 != 0
}

// RunExtras runs optional post-install provisioning (browser + green config).
func RunExtras(binDir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := EnsureBrowser(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "setup: browser:", err)
	} else if web.BrowserAvailable() {
		fmt.Fprintln(os.Stderr, "setup: browser ready (managed Chromium + axe-core)")
	}
	if wrote, err := EnsureGreenConfig(binDir); err != nil {
		fmt.Fprintln(os.Stderr, "setup: green config:", err)
	} else if wrote {
		fmt.Fprintln(os.Stderr, "setup: wrote ~/.codehelper/green.json (embed-only via ge, ~195 MB on first start) — run `codehelper green start` or see docs/LOCAL_EMBED.md")
	}
}
