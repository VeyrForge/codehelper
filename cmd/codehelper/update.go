package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/setup"
	"github.com/VeyrForge/codehelper/internal/version"
	"github.com/spf13/cobra"
)

func updateCmd() *cobra.Command {
	var skipSetup bool
	var skipAnalyze bool
	var forceAnalyze bool
	var skipProjects bool
	c := &cobra.Command{
		Use:   "update [path]",
		Short: "Rebuild and update codehelper from local source",
		Long: "Rebuild codehelper from local source, replace current binary, run setup, refresh index, and ensure watch daemon is running.\n\n" +
			"Machines **without Go or a C compiler**: use **`codehelper upgrade`** to install the latest official GitHub release binary instead.\n\n" +
			"**Windows bootstrap build** (fixed output path): run **`powershell -ExecutionPolicy Bypass -File .\\scripts\\build-local.ps1`** from the repo — downloads WinLibs into `.vendor` when needed and builds **`bin\\codehelper.exe`**.\n\n" +
			"On Windows x86_64, if gcc is not on PATH, `update` downloads portable MinGW into .vendor/winlibs-mingw64 (first time ~260 MiB, then cached in the repo). " +
			"Linux and macOS **source rebuild** requires gcc/cc on PATH (e.g. build-essential / Xcode CLT). Prebuilt Linux binaries still come from **`upgrade`**.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				root = args[0]
			}
			root, err = filepath.Abs(root)
			if err != nil {
				return err
			}

			// Find repository root from the given path (or parent path).
			repoRoot, err := gitutil.FindGitRoot(root)
			if err != nil {
				return fmt.Errorf("could not find git repository for update path %q: %w", root, err)
			}
			exePath, err := os.Executable()
			if err != nil {
				return err
			}
			exePath, err = filepath.Abs(exePath)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "update: replacing executable:\n  %s\n", exePath)
			fileVer, _ := version.ReadFromDir(repoRoot)
			fmt.Fprintf(os.Stderr, "  running embedded version: %s  repo VERSION file: %s\n", version.Current(), fileVer)

			if err := rebuildBinaryFromSource(repoRoot, exePath); err != nil {
				return err
			}
			if err := ensureWindowsUserPathPrefix(filepath.Dir(exePath)); err != nil {
				return err
			}
			if err := ensureWindowsGoBinLauncher(exePath); err != nil {
				return err
			}

			if !skipSetup {
				if pruned, err := setup.CursorGlobal(); err != nil {
					return err
				} else if pruned {
					fmt.Fprintln(os.Stderr, "update: removed stray global Cursor MCP entry (codehelper is per-project) — reload Cursor to drop the duplicate")
				}
			}

			if !skipAnalyze {
				if err := reanalyzeAfterUpdate(repoRoot, forceAnalyze); err != nil {
					return err
				}
			}

			// Restart watch so the daemon runs the new binary (old PID may still be pre-update).
			_ = stopDaemon(repoRoot)
			autoEnsureWatchDaemon(repoRoot, "")
			autoEnsureCodehelperGitignore(repoRoot)

			// Self-heal EVERY other registered project too: rewrite client rules +
			// MCP config, reindex on schema/parser change, restart stale daemons — so
			// updating the binary never silently breaks a project you're not in.
			if !skipProjects {
				fmt.Fprintln(os.Stderr, "update: repairing all registered projects…")
				repairAllProjects()
			}
			fmt.Println("codehelper update complete")
			return nil
		},
	}
	c.Flags().BoolVar(&skipSetup, "skip-setup", false, "skip MCP/skills setup step")
	c.Flags().BoolVar(&skipAnalyze, "skip-analyze", false, "skip post-update analyze")
	c.Flags().BoolVar(&forceAnalyze, "force-analyze", false, "force full reindex on post-update analyze")
	c.Flags().BoolVar(&skipProjects, "skip-projects", false, "do not repair other registered projects (rules/config/reindex)")
	return c
}

// buildTags returns the `go build` tag flags for a source rebuild. The `rod`
// tag compiles in the headless-browser tier (screenshot/console MCP tools); it's
// pure Go (no cgo), so it's on by default and dropped only when CODEHELPER_NO_ROD
// is set, keeping `update` in sync with scripts/build-go.mjs and install.sh.
func buildTags() []string {
	if os.Getenv("CODEHELPER_NO_ROD") != "" {
		return nil
	}
	return []string{"-tags", "rod"}
}

func rebuildBinaryFromSource(repoRoot, target string) error {
	if err := ensureVendorCCompiler(repoRoot); err != nil {
		return err
	}
	tmp := target + ".new"
	fmt.Fprintf(os.Stderr, "update: build output (staging): %s\n", tmp)
	ldflags, err := version.LdflagsX(repoRoot)
	if err != nil {
		return err
	}
	args := append([]string{"build", "-trimpath"}, buildTags()...)
	args = append(args, "-ldflags", ldflags, "-o", tmp, "./cmd/codehelper")
	build := exec.Command("go", args...)
	build.Dir = repoRoot
	build.Env = buildEnvForUpdate(repoRoot)
	out, err := build.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("go build failed: %s", msg)
	}
	if err := replaceRunningBinary(tmp, target); err != nil {
		return err
	}
	// The primary binary is updated, but a second install copy (the ~/.local/bin
	// vs ~/go/bin split) and the codehelper-mcp thin binary would otherwise stay
	// stale — so a client pointed at the other path keeps running the old build.
	// Fan the fresh binaries out to every known install dir.
	if err := fanOutBinaries(repoRoot, target); err != nil {
		fmt.Fprintln(os.Stderr, "update: sync install locations:", err)
	}
	return nil
}

// installDirs returns the deduped set of directories that may hold a codehelper
// install: the just-updated binary's dir, plus the conventional ~/.local/bin and
// ~/go/bin (the documented drift pair). Only existing dirs are returned.
func installDirs(primaryTarget string) []string {
	cands := []string{filepath.Dir(primaryTarget)}
	if home, err := os.UserHomeDir(); err == nil {
		cands = append(cands, filepath.Join(home, ".local", "bin"), filepath.Join(home, "go", "bin"))
	}
	seen := map[string]struct{}{}
	var out []string
	for _, d := range cands {
		abs, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		if _, dup := seen[abs]; dup {
			continue
		}
		seen[abs] = struct{}{}
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			out = append(out, abs)
		}
	}
	return out
}

// fanOutBinaries makes every install dir hold the fresh codehelper AND a freshly
// built codehelper-mcp, so updating once updates whatever binary each client is
// configured to launch (no ~/.local/bin vs ~/go/bin drift, mcp binary included).
func fanOutBinaries(repoRoot, primaryTarget string) error {
	dirs := installDirs(primaryTarget)
	freshMain, err := os.ReadFile(primaryTarget)
	if err != nil {
		return fmt.Errorf("read fresh binary: %w", err)
	}

	// Build codehelper-mcp once to a temp.
	mcpTmp := primaryTarget + "-mcp.build"
	ldflags, lerr := version.LdflagsX(repoRoot)
	if lerr != nil {
		return lerr
	}
	margs := append([]string{"build", "-trimpath"}, buildTags()...)
	margs = append(margs, "-ldflags", ldflags, "-o", mcpTmp, "./cmd/codehelper-mcp")
	build := exec.Command("go", margs...)
	build.Dir = repoRoot
	build.Env = buildEnvForUpdate(repoRoot)
	if out, berr := build.CombinedOutput(); berr != nil {
		return fmt.Errorf("build codehelper-mcp: %s", strings.TrimSpace(string(out)))
	}
	defer os.Remove(mcpTmp)
	freshMCP, err := os.ReadFile(mcpTmp)
	if err != nil {
		return err
	}

	var synced []string
	for _, d := range dirs {
		mainDst := filepath.Join(d, "codehelper")
		if mainDst != primaryTarget {
			if err := installBinaryBytes(freshMain, mainDst); err != nil {
				fmt.Fprintf(os.Stderr, "update: sync %s: %v\n", mainDst, err)
				continue
			}
		}
		mcpDst := filepath.Join(d, "codehelper-mcp")
		if err := installBinaryBytes(freshMCP, mcpDst); err != nil {
			fmt.Fprintf(os.Stderr, "update: sync %s: %v\n", mcpDst, err)
			continue
		}
		if err := installAlias(d); err != nil {
			fmt.Fprintf(os.Stderr, "update: alias %s in %s: %v\n", aliasName, d, err)
		}
		synced = append(synced, d)
	}
	if len(synced) > 0 {
		fmt.Fprintf(os.Stderr, "update: synced codehelper + codehelper-mcp (+ %s alias) to %s\n", aliasName, strings.Join(synced, ", "))
	}
	return nil
}

// aliasName is the short convenience command made to resolve to codehelper in
// every install dir, so `ch` works anywhere `codehelper` does. codehelper stays
// the canonical name (MCP configs, scripts, and docs all spawn `codehelper`);
// `ch` is purely a shorter entrypoint to the same binary.
const aliasName = "ch"

// installAlias makes `ch` resolve to codehelper in dir. On Unix it's a relative
// symlink (so the link stays valid if the dir is moved); on Windows, where
// symlinks need privilege, it's a copy of the exe. Best-effort — a failure is
// returned for the caller to log, never fatal to an update.
func installAlias(dir string) error {
	if runtime.GOOS == "windows" {
		content, err := os.ReadFile(filepath.Join(dir, "codehelper.exe"))
		if err != nil {
			return err
		}
		return installBinaryBytes(content, filepath.Join(dir, aliasName+".exe"))
	}
	link := filepath.Join(dir, aliasName)
	// Replace any existing ch (old symlink or stale copy) so it always points at
	// the just-synced codehelper. Lstat so a dangling link is still detected.
	if _, err := os.Lstat(link); err == nil {
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	return os.Symlink("codehelper", link) // relative target within the same dir
}

// installBinaryBytes writes content to dst via a staging file + rename, which on
// Linux/macOS replaces even a *running* binary (the rename swaps the dir entry;
// live processes keep the old inode). Windows is handled by replaceRunningBinary.
func installBinaryBytes(content []byte, dst string) error {
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, content, 0o755); err != nil {
		return err
	}
	return replaceRunningBinary(tmp, dst)
}

// replaceRunningBinary installs tmp onto target.
//
// Windows: NTFS usually allows renaming the *running* executable to a side name, then renaming
// the new build into place (Chrome/updater pattern). In-place overwrite of a mapped image is what fails.
// If rename-aside fails, we fall back to a deferred PowerShell helper.
func replaceRunningBinary(tmp, target string) error {
	if err := os.Chmod(tmp, 0o755); err != nil && runtime.GOOS != "windows" {
		_ = os.Remove(tmp)
		return err
	}
	if runtime.GOOS == "windows" {
		return replaceRunningBinaryWindows(tmp, target)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("failed to replace %q: %w", target, err)
	}
	return nil
}

func replaceRunningBinaryWindows(tmp, target string) error {
	bak := target + ".bak"
	_ = os.Remove(bak)

	// Self-update while running: rename live exe aside, then promote tmp → target.
	if _, err := os.Stat(target); err == nil {
		errRen := os.Rename(target, bak)
		if errRen == nil {
			if err := os.Rename(tmp, target); err != nil {
				_ = os.Rename(bak, target)
				_ = os.Remove(tmp)
				return fmt.Errorf("promote new exe onto %q: %w", target, err)
			}
			fmt.Printf("binary updated (%s holds the previous build; delete it later if no process still uses it)\n", filepath.Base(bak))
			go func(name string) {
				time.Sleep(4 * time.Second)
				_ = os.Remove(name)
			}(bak)
			return nil
		}
		fmt.Fprintf(os.Stderr, "codehelper: Windows rename-aside failed (%s → %s): %v\n", filepath.Base(target), filepath.Base(bak), errRen)
	} else if os.IsNotExist(err) {
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("failed to install %q: %w", target, err)
		}
		return nil
	}

	// Fresh path or rename-aside unsupported: try direct swap (works when target is not locked).
	if err := os.Rename(tmp, target); err == nil {
		return nil
	}

	if logPath, derr := scheduleWindowsDeferredReplace(tmp, target); derr == nil {
		fmt.Println("scheduled deferred binary replacement (fallback — Windows blocked rename-aside).")
		if logPath != "" {
			fmt.Println("replace log:", logPath)
		}
		fmt.Println("then run: codehelper version")
		return nil
	}
	_ = os.Remove(tmp)
	return fmt.Errorf("failed to replace %q (rename-aside, direct rename, and deferred helper all failed)", target)
}

// ensureVendorCCompiler guarantees CGO can find a C compiler before `go build`.
// Windows: downloads portable MinGW into .vendor/winlibs-mingw64 when gcc is absent (see scripts/bootstrap-winlibs.ps1).
// Linux/macOS: expects gcc/cc on PATH (standard dev packages); bundling a compiler is not practical here.
func ensureVendorCCompiler(repoRoot string) error {
	if runtime.GOOS == "windows" {
		return ensureWindowsWinLibs(repoRoot)
	}
	if cc := os.Getenv("CC"); cc != "" {
		if _, err := exec.LookPath(cc); err == nil {
			return nil
		}
	}
	if _, err := exec.LookPath("gcc"); err == nil {
		return nil
	}
	if _, err := exec.LookPath("cc"); err == nil {
		return nil
	}
	return fmt.Errorf("CGO requires gcc or cc on PATH (e.g. Debian/Ubuntu: sudo apt install build-essential; Fedora: sudo dnf install gcc make)")
}

func ensureWindowsWinLibs(repoRoot string) error {
	if _, err := exec.LookPath("gcc"); err == nil {
		return nil
	}
	vendorGCC := filepath.Join(repoRoot, ".vendor", "winlibs-mingw64", "bin", "gcc.exe")
	if _, err := os.Stat(vendorGCC); err == nil {
		return nil
	}
	if runtime.GOARCH != "amd64" {
		return fmt.Errorf("automatic WinLibs bootstrap supports Windows x86_64 only; install gcc (e.g. MSYS2) for this architecture")
	}
	script := filepath.Join(repoRoot, "scripts", "bootstrap-winlibs.ps1")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("missing %s: %w", script, err)
	}
	ps := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-RepoRoot", repoRoot)
	ps.Dir = repoRoot
	ps.Env = os.Environ()
	ps.Stdout = os.Stdout
	ps.Stderr = os.Stderr
	if err := ps.Run(); err != nil {
		return fmt.Errorf("automatic WinLibs gcc bootstrap failed: %w", err)
	}
	if _, err := os.Stat(vendorGCC); err != nil {
		return fmt.Errorf("after bootstrap, expected gcc at %s", vendorGCC)
	}
	return nil
}

func buildEnvForUpdate(repoRoot string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, "CGO_ENABLED=1")
	if runtime.GOOS != "windows" {
		return env
	}
	if _, err := exec.LookPath("gcc"); err == nil {
		return env
	}
	vendorBin := filepath.Join(repoRoot, ".vendor", "winlibs-mingw64", "bin")
	gccPath := filepath.Join(vendorBin, "gcc.exe")
	if _, err := os.Stat(gccPath); err != nil {
		return env
	}
	sep := string(os.PathListSeparator)
	foundPath := false
	for i := range env {
		if !strings.HasPrefix(env[i], "PATH=") {
			continue
		}
		foundPath = true
		env[i] = "PATH=" + vendorBin + sep + strings.TrimPrefix(env[i], "PATH=")
		break
	}
	if !foundPath {
		env = append(env, "PATH="+vendorBin)
	}
	return env
}

// scheduleWindowsDeferredReplace spawns a background PowerShell job that waits for this process
// to exit, then retries Move-Item (the running .exe is locked until then). Returns a log file path.
func scheduleWindowsDeferredReplace(tmp, target string) (string, error) {
	waitPID := os.Getpid()
	logPath := filepath.Join(os.TempDir(), fmt.Sprintf("codehelper-replace-%d.log", waitPID))
	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("codehelper-replace-%d.ps1", waitPID))
	script := windowsDeferredReplaceScript()
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		return "", err
	}
	cmd := exec.Command(
		"powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-File", scriptPath,
		"-WaitPid", strconv.Itoa(waitPID),
		"-Src", tmp,
		"-Dst", target,
		"-Log", logPath,
	)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	return logPath, nil
}

func windowsDeferredReplaceScript() string {
	// Keep compatible with Windows PowerShell 5.1
	return `param(
  [Parameter(Mandatory = $true)][int]$WaitPid,
  [Parameter(Mandatory = $true)][string]$Src,
  [Parameter(Mandatory = $true)][string]$Dst,
  [Parameter(Mandatory = $true)][string]$Log
)
$ErrorActionPreference = 'Continue'
function WriteLog([string]$m) {
  try {
    $line = ('[{0}] {1}' -f (Get-Date -Format o), $m)
    Add-Content -LiteralPath $Log -Value $line -Encoding utf8
  } catch { }
}
try {
  WriteLog "waiting for PID $WaitPid to exit (this is the codehelper that ran update)"
  $deadline = (Get-Date).AddMinutes(3)
  while ((Get-Date) -lt $deadline) {
    $proc = Get-Process -Id $WaitPid -ErrorAction SilentlyContinue
    if ($null -eq $proc) { break }
    Start-Sleep -Milliseconds 120
  }
  if ((Get-Date) -ge $deadline) {
    WriteLog "timeout waiting for PID; attempting move anyway"
  }
  Start-Sleep -Milliseconds 500
  for ($i = 0; $i -lt 100; $i++) {
    try {
      Move-Item -LiteralPath $Src -Destination $Dst -Force -ErrorAction Stop
      WriteLog "replace OK -> $Dst"
      exit 0
    } catch {
      WriteLog ("attempt {0}: {1}" -f $i, $_.Exception.Message)
      Start-Sleep -Milliseconds 200
    }
  }
  WriteLog "replace FAILED after retries"
  exit 1
} catch {
  WriteLog ("fatal: {0}" -f $_.Exception.Message)
  exit 1
}
`
}

func ensureWindowsUserPathPrefix(binDir string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if strings.TrimSpace(binDir) == "" {
		return nil
	}
	psScript := fmt.Sprintf(
		"$bin = '%s'; "+
			"$userPath = [Environment]::GetEnvironmentVariable('Path','User'); "+
			"$parts = @(); "+
			"if ($userPath) { $parts = $userPath -split ';' | Where-Object { $_ -and ($_ -ne $bin) } }; "+
			"$newPath = @($bin) + $parts; "+
			"[Environment]::SetEnvironmentVariable('Path', ($newPath -join ';'), 'User'); "+
			"if (($env:PATH -split ';') -notcontains $bin) { $env:PATH = \"$bin;$env:PATH\" }",
		strings.ReplaceAll(binDir, "'", "''"),
	)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("failed to persist User PATH for %q: %s", binDir, msg)
	}
	return nil
}

func ensureWindowsGoBinLauncher(exePath string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	goBin := filepath.Join(home, "go", "bin")
	if st, err := os.Stat(goBin); err != nil || !st.IsDir() {
		return nil
	}
	launcherPath := filepath.Join(goBin, "codehelper.cmd")
	launcherBody := fmt.Sprintf("@echo off\r\n\"%s\" %%*\r\n", exePath)
	if err := os.WriteFile(launcherPath, []byte(launcherBody), 0o644); err != nil {
		return fmt.Errorf("failed to write launcher %q: %w", launcherPath, err)
	}
	return nil
}

func reanalyzeAfterUpdate(repoRoot string, force bool) error {
	ctx := context.Background()
	opt := indexer.Options{
		Force:        force,
		Invalidation: indexer.InvalidationEager,
	}
	if err := indexer.Run(ctx, repoRoot, opt); err != nil {
		return err
	}

	reg, err := registry.Load()
	if err != nil {
		return err
	}
	commit, _ := gitutil.HeadCommit(repoRoot)
	name := filepath.Base(repoRoot)
	if m, _ := meta.Read(repoRoot); m != nil && strings.TrimSpace(m.RepoName) != "" {
		name = strings.TrimSpace(m.RepoName)
	}
	if err := reg.Upsert(name, repoRoot, commit, meta.SchemaVersion); err != nil {
		return err
	}
	return reg.Save()
}
