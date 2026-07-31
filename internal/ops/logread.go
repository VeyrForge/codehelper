package ops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/connections"
)

// LogReadResult is bounded log output.
type LogReadResult struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Lines  int    `json:"lines"`
	Output string `json:"output"`
	Trunc  bool   `json:"truncated,omitempty"`
}

// ReadLog tails a configured log source. Local files are read directly; sources
// with ssh_host delegate to a tail recipe on that host (tail-log, tail-{name}, or tail).
func ReadLog(ctx context.Context, repoRoot, sourceName string, lines int) (*LogReadResult, error) {
	if lines <= 0 {
		lines = 200
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}
	cfg, err := connections.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	var src *connections.LogSource
	for i := range cfg.LogSources {
		if strings.EqualFold(cfg.LogSources[i].Name, sourceName) {
			src = &cfg.LogSources[i]
			break
		}
	}
	if src == nil {
		return nil, fmt.Errorf("log source %q not configured", sourceName)
	}
	if src.Disabled {
		return nil, fmt.Errorf("log source %q is disabled", sourceName)
	}
	if strings.TrimSpace(src.SSHHost) != "" {
		return readRemoteLog(ctx, repoRoot, cfg, src, lines)
	}
	p := src.Path
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoRoot, p)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(30 * time.Second)
	}
	_ = f.SetDeadline(deadline)

	all, err := tailFile(f, lines, maxLogBytes)
	if err != nil {
		return nil, err
	}
	trunc := len(all) >= maxLogBytes
	return &LogReadResult{Source: src.Name, Path: src.Path, Lines: lines, Output: all, Trunc: trunc}, nil
}

// readRemoteLog tails a remote log via a tail recipe on the linked SSH host.
func readRemoteLog(ctx context.Context, repoRoot string, cfg connections.Config, src *connections.LogSource, lines int) (*LogReadResult, error) {
	hostName := strings.TrimSpace(src.SSHHost)
	recipeNames := []string{"tail-" + src.Name, "tail-log", "tail"}
	var recipe *connections.Recipe
	for _, rn := range recipeNames {
		if _, r := cfg.FindRecipe(hostName, rn); r != nil {
			recipe = r
			break
		}
	}
	if recipe == nil {
		return nil, fmt.Errorf("log source %q is remote on %q but no tail recipe found — add one: codehelper connections add-recipe --host %s --name tail-log --argv tail,-n,{lines},{path} --params lines,path", src.Name, hostName, hostName)
	}
	pathParam := strings.TrimSpace(src.Path)
	if pathParam == "" {
		return nil, fmt.Errorf("log source %q has no path", src.Name)
	}
	res, err := ExecRecipe(ctx, repoRoot, hostName, recipe.Name, map[string]string{
		"lines": strconv.Itoa(lines),
		"path":  pathParam,
	}, 60*time.Second)
	if res == nil {
		return nil, err
	}
	out := &LogReadResult{
		Source: src.Name, Path: src.Path, Lines: lines, Output: res.Output,
		Trunc: len(res.Output) >= maxLogBytes,
	}
	if err != nil {
		return out, fmt.Errorf("remote tail: %w", err)
	}
	return out, nil
}

func tailFile(f *os.File, maxLines int, maxBytes int) (string, error) {
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	chunk := int64(8192)
	if size < chunk {
		chunk = size
	}
	if chunk == 0 {
		return "", nil
	}
	var buf []byte
	off := size
	// Count newlines directly instead of re-splitting the whole buffer into a
	// []string every iteration (that was O(n²) in the tail size). len(split) ==
	// newlines+1, so the old `len(split) <= maxLines+1` is exactly this.
	newline := []byte{'\n'}
	for off > 0 && bytes.Count(buf, newline) <= maxLines && len(buf) < maxBytes {
		if off-chunk < 0 {
			chunk = off
		}
		off -= chunk
		part := make([]byte, chunk)
		if _, err := f.ReadAt(part, off); err != nil {
			return "", err
		}
		buf = append(part, buf...)
	}
	lines := strings.Split(string(buf), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
	}
	return out, nil
}
