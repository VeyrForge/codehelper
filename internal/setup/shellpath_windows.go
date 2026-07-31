//go:build windows

package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func ensureUnixUserPath(binDir string) (bool, error) {
	return false, nil
}

func ensurePlatformUserPath(binDir string) (bool, error) {
	return ensureWindowsUserPath(binDir)
}

func ensureWindowsUserPath(binDir string) (bool, error) {
	binDir = filepath.Clean(strings.TrimSpace(binDir))
	if binDir == "" {
		return false, nil
	}
	if inPathList(os.Getenv("PATH"), binDir) {
		return false, nil
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
		return false, fmt.Errorf("persist User PATH for %q: %s", binDir, msg)
	}
	return true, nil
}
