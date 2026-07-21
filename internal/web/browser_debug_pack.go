package web

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/paths"
)

// FailureDebugPack is the single structured blob agents need on assert/action
// failure: screenshot path, console errors, failed network, outline/snapshot,
// URL, and action log — one place instead of piecing the report together.
type FailureDebugPack struct {
	Failed         bool             `json:"failed"`
	FinalURL       string           `json:"final_url"`
	Title          string           `json:"title"`
	DocStatus      int              `json:"doc_status"`
	Device         string           `json:"device,omitempty"`
	Viewport       string           `json:"viewport,omitempty"`
	ScreenshotPath string           `json:"screenshot_path,omitempty"`
	ReportPath     string           `json:"report_path,omitempty"`
	PackDir        string           `json:"pack_dir,omitempty"`
	ConsoleErrors  []ConsoleMessage `json:"console_errors"`
	PageErrors     []string         `json:"page_errors"`
	FailedRequests []FailedRequest  `json:"failed_requests"`
	Outline        []OutlineElement `json:"outline,omitempty"`
	Snapshot       string           `json:"snapshot,omitempty"`
	ActionLog      []string         `json:"action_log"`
	Trace          []TraceEvent     `json:"trace,omitempty"`
	FailureShot    bool             `json:"failure_shot,omitempty"`
	GeneratedAt    string           `json:"generated_at"`
}

// ActionsFailed reports whether any interaction step logged FAILED.
func ActionsFailed(log []string) bool {
	for _, l := range log {
		if strings.Contains(l, "FAILED") {
			return true
		}
	}
	return false
}

// ConsoleErrorsOnly filters console messages to error-level entries.
func ConsoleErrorsOnly(in []ConsoleMessage) []ConsoleMessage {
	out := make([]ConsoleMessage, 0, len(in))
	for _, m := range in {
		switch strings.ToLower(strings.TrimSpace(m.Level)) {
		case "error", "assert", "exception":
			out = append(out, m)
		}
	}
	return out
}

// BuildFailureDebugPack assembles the in-memory pack from a capture result
// (paths filled later by WriteFailureDebugPack when writing to disk).
func BuildFailureDebugPack(res *BrowserResult) *FailureDebugPack {
	if res == nil {
		return nil
	}
	failed := ActionsFailed(res.ActionLog)
	return &FailureDebugPack{
		Failed:         failed,
		FinalURL:       res.FinalURL,
		Title:          res.Title,
		DocStatus:      res.DocStatus,
		Device:         res.Device,
		Viewport:       res.Viewport,
		ScreenshotPath: res.ScreenshotPath,
		ReportPath:     res.DebugPackJSON,
		PackDir:        res.DebugPackDir,
		ConsoleErrors:  ConsoleErrorsOnly(res.Console),
		PageErrors:     append([]string(nil), res.PageErrors...),
		FailedRequests: append([]FailedRequest(nil), res.Failed...),
		Outline:        append([]OutlineElement(nil), res.Outline...),
		Snapshot:       res.Snapshot,
		ActionLog:      append([]string(nil), res.ActionLog...),
		Trace:          append([]TraceEvent(nil), res.Trace...),
		FailureShot:    res.FailureShot,
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
	}
}

// DefaultDebugPackRoot is ~/.codehelper/browser/debug-packs.
func DefaultDebugPackRoot() (string, error) {
	base, err := paths.RegistryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "browser", "debug-packs"), nil
}

// ResolveDebugPackDir picks where to write a failure pack.
func ResolveDebugPackDir(opts BrowserOptions) (string, error) {
	if d := strings.TrimSpace(opts.DebugPackDir); d != "" {
		return filepath.Abs(d)
	}
	if d := strings.TrimSpace(os.Getenv("CODEHELPER_BROWSER_DEBUG_PACK_DIR")); d != "" {
		return filepath.Abs(d)
	}
	root, err := DefaultDebugPackRoot()
	if err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	return filepath.Join(root, stamp), nil
}

// WriteFailureDebugPack writes screenshot + report.json under dir and updates
// res paths + FailurePack. Prefer the failure-step shot when present.
func WriteFailureDebugPack(dir string, res *BrowserResult) (*FailureDebugPack, error) {
	if res == nil {
		return nil, fmt.Errorf("nil BrowserResult")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	format := NormalizeFormat(res.Format)
	shotName := "failure." + format
	shotPath := filepath.Join(dir, shotName)
	img := failureShotBytes(res)
	if len(img) == 0 {
		img = res.Image
	}
	if len(img) > 0 {
		if err := os.WriteFile(shotPath, img, 0o644); err != nil {
			return nil, err
		}
		res.ScreenshotPath = shotPath
	}
	reportPath := filepath.Join(dir, "report.json")
	pack := BuildFailureDebugPack(res)
	pack.PackDir = dir
	pack.ScreenshotPath = res.ScreenshotPath
	pack.ReportPath = reportPath
	data, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(reportPath, data, 0o644); err != nil {
		return nil, err
	}
	res.DebugPackDir = dir
	res.DebugPackJSON = reportPath
	res.FailurePack = pack
	return pack, nil
}

// WriteCaptureReport writes a CLI/MCP-adjacent JSON report next to a screenshot
// (success or failure). Includes FailurePack when actions failed.
func WriteCaptureReport(reportPath string, res *BrowserResult) error {
	if res == nil {
		return fmt.Errorf("nil BrowserResult")
	}
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
		return err
	}
	pack := BuildFailureDebugPack(res)
	if pack != nil {
		pack.ScreenshotPath = res.ScreenshotPath
		pack.ReportPath = reportPath
		pack.PackDir = res.DebugPackDir
	}
	payload := map[string]any{
		"final_url":       res.FinalURL,
		"title":           res.Title,
		"doc_status":      res.DocStatus,
		"device":          res.Device,
		"viewport":        res.Viewport,
		"format":          res.Format,
		"load_ms":         res.LoadMS,
		"screenshot_path": res.ScreenshotPath,
		"debug_pack_dir":  res.DebugPackDir,
		"debug_pack_json": res.DebugPackJSON,
		"action_log":      res.ActionLog,
		"console_errors":  ConsoleErrorsOnly(res.Console),
		"page_errors":     res.PageErrors,
		"failed_requests": res.Failed,
		"outline":         res.Outline,
		"snapshot":        res.Snapshot,
		"trace":           res.Trace,
		"actions_failed":  ActionsFailed(res.ActionLog),
		"failure_pack":    pack,
		"generated_at":    time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(reportPath, data, 0o644)
}

func failureShotBytes(res *BrowserResult) []byte {
	if res == nil {
		return nil
	}
	if res.FailureShot {
		for i := len(res.ActionPreviews) - 1; i >= 0; i-- {
			p := res.ActionPreviews[i]
			if strings.Contains(p.Label, "FAILED") && len(p.Image) > 0 {
				return p.Image
			}
		}
		if len(res.ActionPreviews) > 0 {
			return res.ActionPreviews[len(res.ActionPreviews)-1].Image
		}
	}
	return nil
}

// shouldWriteDebugPack decides whether to persist a failure pack to disk.
// CLI/MCP set WriteDebugPack=true. Env CODEHELPER_BROWSER_WRITE_DEBUG_PACK=0 disables;
// =1 forces on. A non-empty DebugPackDir always enables writing.
func shouldWriteDebugPack(opts BrowserOptions) bool {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("CODEHELPER_BROWSER_WRITE_DEBUG_PACK")))
	switch env {
	case "0", "false", "off", "no":
		return false
	case "1", "true", "on", "yes":
		return true
	}
	if strings.TrimSpace(opts.DebugPackDir) != "" {
		return true
	}
	return opts.WriteDebugPack
}

// AttachFailureArtifacts fills FailurePack and optionally writes it to disk when
// actions failed. On success, only the in-memory pack is attached (Failed=false).
func AttachFailureArtifacts(res *BrowserResult, opts BrowserOptions) error {
	if res == nil {
		return nil
	}
	failed := ActionsFailed(res.ActionLog)
	res.FailurePack = BuildFailureDebugPack(res)
	if !failed || !shouldWriteDebugPack(opts) {
		return nil
	}
	dir, err := ResolveDebugPackDir(opts)
	if err != nil {
		return err
	}
	_, err = WriteFailureDebugPack(dir, res)
	return err
}
