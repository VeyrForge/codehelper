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

// EnsureGreenConfig writes ~/.codehelper/green.json when ge is on PATH (or in
// binDir) and no config exists yet. Does not start servers — model downloads
// stay on the user's first `codehelper green start` or MCP connect.
func EnsureGreenConfig(binDir string) (bool, error) {
	_, ok, err := green.Load()
	if err != nil {
		return false, err
	}
	if ok {
		return false, nil
	}
	if !hasExecutable("ge", binDir) {
		return false, nil
	}
	cfg := green.Config{
		Enabled: true,
		Servers: []green.Server{
			{
				Name:       "embed",
				Cmd:        "ge",
				Args:       []string{"embed", "serve", "--port", "{{port}}"},
				Port:       8766,
				HealthPath: "/v1/models",
				URLEnv:     "CODEHELPER_EMBED_URL",
			},
			{
				Name:       "llm",
				Cmd:        "ge",
				Args:       []string{"run", "--port", "{{port}}"},
				Port:       8767,
				HealthPath: "/v1/models",
				URLEnv:     "CODEHELPER_ENRICH_URL",
				Env:        map[string]string{"CODEHELPER_ENRICH_MODEL": "qwen2.5-coder:7b"},
			},
		},
	}
	if err := green.Save(cfg); err != nil {
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
	return fi.Mode()&0o111 != 0 || fi.Mode()&0o100 != 0
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
		fmt.Fprintln(os.Stderr, "setup: wrote ~/.codehelper/green.json (embed + llm servers via ge) — run `codehelper green start` when ready")
	}
}
