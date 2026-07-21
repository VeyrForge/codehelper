package mcpsvc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	defaultMaxReadBytes    = 512 * 1024
	absoluteMaxReadBytes   = 4 * 1024 * 1024
	defaultMaxListEntries  = 200
	absoluteMaxListEntries = 2000
	// defaultReadLineWindow caps a whole-file read when the caller passes no
	// limit, so a blind read of a 2,000-line file doesn't dump the whole thing
	// into context. Files within the window return in full; larger files return
	// the first window of lines plus a paginate-from-here note. ~46% of injected
	// tokens were whole-file reads before this cap.
	defaultReadLineWindow = 500
	maxWriteFileBytes     = 4 * 1024 * 1024

	editsRelDir        = ".codehelper/edits"
	revertTokenVersion = "v1"
	// Diff echo caps. After a write/patch is APPLIED the agent already knows what it
	// sent — it only needs confirmation + a revert token, so we echo a small diff
	// (and elide the rest). A dry_run is a PREVIEW, so it gets a larger cap. The old
	// flat 64KB cap meant every edit could dump 64KB of diff back into context.
	appliedDiffCap = 2 * 1024
	previewDiffCap = 12 * 1024
	maxPatchHunks  = 64
)

// RegisterWorkspaceTools adds MCP tools to read/list/write/patch files under indexed repo roots.
func RegisterWorkspaceTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg
	s.AddTool(mcp.NewTool("read_workspace_file",
		mcp.WithDescription("Read a text file under the repo root. Returns up to a default 500-line window (files within it return in full); larger files return the first window plus a note telling you the next offset to page from. For CODE, prefer query/context/ast_query: they return just the relevant symbols instead of the whole file. Pass offset/limit to read a specific slice."),
		mcp.WithString("path", mcp.Required(), mcp.Description("Path relative to repo root, or absolute if still under that root")),
		mcp.WithString("repo", mcp.Description("Repository name (optional; defaults to current MCP workspace)")),
		mcp.WithNumber("offset", mcp.Description("1-based start line (default 1)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("limit", mcp.Description("Max lines to return from offset (default 0 = the default 500-line window; pass an explicit number for a larger/smaller slice)"), mcp.DefaultNumber(0)),
		mcp.WithNumber("max_bytes", mcp.Description("Max bytes to read (cap 4MiB)"), mcp.DefaultNumber(defaultMaxReadBytes)),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default, token-efficient) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("read_workspace_file", readWorkspaceFileHandler(regRef)))

	s.AddTool(mcp.NewTool("write_workspace_file",
		mcp.WithDescription("Replace a file under an indexed repo root (full content). Prefer apply_patch_workspace_file for edits to existing files. Empty content is refused by default (no 0-byte artifacts); pass allow_empty=true only when intentional. Cannot write under .git/, node_modules/, or .env* files. Returns a revert_token usable with revert_workspace_edit."),
		mcp.WithString("path", mcp.Required(), mcp.Description("Path relative to repo root")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Full new file contents (UTF-8)")),
		mcp.WithString("repo", mcp.Description("Repository name (optional; defaults to current MCP workspace)")),
		mcp.WithBoolean("create_directories", mcp.Description("Create parent directories if missing"), mcp.DefaultBool(true)),
		mcp.WithBoolean("allow_truncate", mcp.Description("Bypass truncation guard (set true only when intentionally shrinking)"), mcp.DefaultBool(false)),
		mcp.WithBoolean("allow_empty", mcp.Description("Allow writing an empty (0-byte) file"), mcp.DefaultBool(false)),
		annotWorkspaceWrite(),
	), timedTool("write_workspace_file", writeWorkspaceFileHandler(regRef)))

	s.AddTool(mcp.NewTool("apply_patch_workspace_file",
		mcp.WithDescription("Apply one or more search/replace hunks to an existing file. Preferred arg: hunks=[{old_string,new_string,replace_all?,exact?}]. Aliases accepted: edits/changes/replacements, or patch as the same array, or a small unified-diff string with context/-/+ lines. Each old_string must match the current file exactly once unless replace_all is true. If an exact match fails purely on whitespace/indentation (tabs vs spaces, trailing space) AND the snippet anchors to exactly one place, the patch is still applied — new_string is reindented to the file's real style and the response notes whitespace_adjusted. Exact matches also restyle new_string indent chars to the file's tab/space convention. Pass exact=true on a hunk to disable tolerant re-anchoring. Preserves untouched content verbatim. Returns a unified diff and revert_token."),
		mcp.WithString("path", mcp.Required(), mcp.Description("Path relative to repo root")),
		mcp.WithString("repo", mcp.Description("Repository name (optional; defaults to current MCP workspace)")),
		mcp.WithArray("hunks", mcp.Description("Array of {old_string, new_string, replace_all?:bool, exact?:bool}. Applied in order. Aliases: edits, changes, replacements."), mcp.Items(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"old_string":  map[string]any{"type": "string", "description": "Exact text to find in the current file"},
				"new_string":  map[string]any{"type": "string", "description": "Replacement text"},
				"replace_all": map[string]any{"type": "boolean", "description": "If true, replace every occurrence; otherwise old_string must appear exactly once"},
				"exact":       map[string]any{"type": "boolean", "description": "If true, disable whitespace-tolerant re-anchoring for this hunk"},
			},
			"required":             []string{"old_string", "new_string"},
			"additionalProperties": false,
		})),
		mcp.WithString("patch", mcp.Description("Optional alias: same as hunks (JSON array) OR a small unified-diff string with context/-/+ lines")),
		mcp.WithBoolean("dry_run", mcp.Description("If true, do not write; just return the diff that would be produced"), mcp.DefaultBool(false)),
		annotWorkspaceWrite(),
	), timedTool("apply_patch_workspace_file", applyPatchWorkspaceFileHandler(regRef)))

	s.AddTool(mcp.NewTool("revert_workspace_edit",
		mcp.WithDescription("Revert a prior workspace edit using the revert_token returned by write_workspace_file or apply_patch_workspace_file. Idempotent."),
		mcp.WithString("revert_token", mcp.Required(), mcp.Description("Token returned by a previous write/patch call")),
		annotWorkspaceRevert(),
	), timedTool("revert_workspace_edit", revertWorkspaceEditHandler(regRef)))

	s.AddTool(mcp.NewTool("list_workspace_directory",
		mcp.WithDescription("List one directory (non-recursive) under the repo root — for LAYOUT only. To FIND code, prefer query/scout, which search the whole indexed graph at once; do not walk the tree directory-by-directory."),
		mcp.WithString("path", mcp.Description("Directory relative to repo root (default \".\")")),
		mcp.WithString("repo", mcp.Description("Repository name (optional; defaults to current MCP workspace)")),
		mcp.WithNumber("max_entries", mcp.Description("Max entries (cap 2000)"), mcp.DefaultNumber(defaultMaxListEntries)),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default, token-efficient) | json")),
		annotReadOnlyClosedWorld(),
	), timedTool("list_workspace_directory", listWorkspaceDirectoryHandler(regRef)))
}

type readWorkspaceFileResponse struct {
	Path                 string   `json:"path"`
	RepoRoot             string   `json:"repo_root"`
	Truncated            bool     `json:"truncated"`
	SizeBytes            int64    `json:"size_bytes"`
	ReadBytes            int      `json:"read_bytes"`
	TotalLines           int      `json:"total_lines,omitempty"`
	LineStart            int      `json:"line_start,omitempty"`
	LineEnd              int      `json:"line_end,omitempty"`
	Content              string   `json:"content"`
	NotUTF8              bool     `json:"not_utf8,omitempty"`
	Note                 string   `json:"note,omitempty"`
	RecommendedNextTools []string `json:"recommended_next_tools,omitempty"`
	Warnings             []string `json:"warnings,omitempty"`
	EvidencePaths        []string `json:"evidence_paths,omitempty"`
	Confidence           float64  `json:"confidence,omitempty"`
}

type writeWorkspaceFileResponse struct {
	Note         string `json:"note,omitempty"`
	Path         string `json:"path"`
	RepoRoot     string `json:"repo_root"`
	BytesBefore  int    `json:"bytes_before"`
	BytesWritten int    `json:"bytes_written"`
	Created      bool   `json:"created"`
	NoOp         bool   `json:"no_op"`
	RevertToken  string `json:"revert_token,omitempty"`
	Diff         string `json:"diff"`
	DiffElided   bool   `json:"diff_elided,omitempty"`
}

type applyPatchResponse struct {
	Note               string `json:"note,omitempty"`
	Path               string `json:"path"`
	RepoRoot           string `json:"repo_root"`
	BytesBefore        int    `json:"bytes_before"`
	BytesAfter         int    `json:"bytes_after"`
	HunksApplied       int    `json:"hunks_applied"`
	WhitespaceAdjusted int    `json:"whitespace_adjusted,omitempty"`
	RevertToken        string `json:"revert_token,omitempty"`
	Diff               string `json:"diff"`
	DiffElided         bool   `json:"diff_elided,omitempty"`
	DryRun             bool   `json:"dry_run,omitempty"`
}

type revertResponse struct {
	Path        string `json:"path"`
	RepoRoot    string `json:"repo_root"`
	Restored    bool   `json:"restored"`
	AlreadyDone bool   `json:"already_done,omitempty"`
}

type dirEntry struct {
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type listWorkspaceDirectoryResponse struct {
	Path                 string     `json:"path"`
	RepoRoot             string     `json:"repo_root"`
	Truncated            bool       `json:"truncated"`
	Entries              []dirEntry `json:"entries"`
	TotalFound           int        `json:"total_found"`
	RecommendedNextTools []string   `json:"recommended_next_tools,omitempty"`
	Warnings             []string   `json:"warnings,omitempty"`
	EvidencePaths        []string   `json:"evidence_paths,omitempty"`
	Confidence           float64    `json:"confidence,omitempty"`
}

func readWorkspaceFileHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		rawPath := argString(args, "path")
		if strings.TrimSpace(rawPath) == "" {
			return mcp.NewToolResultError("path is required"), nil
		}
		rel, err := relativePathUnderRepo(repo.RootPath, rawPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if workspacePathBlocked(rel, false) {
			return mcp.NewToolResultError("reading this path is not allowed (.git or similar)"), nil
		}
		maxBytes := int(argFloat(args, "max_bytes", defaultMaxReadBytes))
		if maxBytes < 1 {
			maxBytes = defaultMaxReadBytes
		}
		if maxBytes > absoluteMaxReadBytes {
			maxBytes = absoluteMaxReadBytes
		}

		abs := filepath.Join(repo.RootPath, filepath.FromSlash(rel))
		st, err := os.Stat(abs)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if st.IsDir() {
			return mcp.NewToolResultError("path is a directory, not a file"), nil
		}

		f, err := os.Open(abs)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer f.Close()

		limited := io.LimitReader(f, int64(maxBytes)+1)
		buf, err := io.ReadAll(limited)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		truncated := len(buf) > maxBytes
		if truncated {
			buf = buf[:maxBytes]
		}

		notUTF8 := !utf8.Valid(buf)
		content := string(buf)

		// Line-range slicing so the agent reads only the part it needs instead of
		// pulling a whole large file into context. When no limit is given we apply
		// a DEFAULT window (defaultReadLineWindow) so a blind read of a big file
		// pages instead of dumping the whole thing — files within the window still
		// return in full.
		offset := int(argFloat(args, "offset", 0))
		limit := int(argFloat(args, "limit", 0))
		explicitLimit := limit > 0
		var lineStart, lineEnd, totalLines int
		content, lineStart, lineEnd, totalLines = windowLines(content, offset, limit)
		moreAfter := lineEnd > 0 && lineEnd < totalLines

		out := readWorkspaceFileResponse{
			Path:                 filepath.ToSlash(rel),
			RepoRoot:             filepath.ToSlash(repo.RootPath),
			Truncated:            truncated,
			SizeBytes:            st.Size(),
			ReadBytes:            len(buf),
			TotalLines:           totalLines,
			LineStart:            lineStart,
			LineEnd:              lineEnd,
			Content:              content,
			NotUTF8:              notUTF8,
			RecommendedNextTools: []string{"query", "context", "ast_query"},
			EvidencePaths:        []string{filepath.ToSlash(rel)},
			Confidence:           0.9,
		}
		if truncated {
			out.Warnings = append(out.Warnings, "response truncated at max_bytes")
		}
		switch {
		case moreAfter && !explicitLimit:
			out.Note = fmt.Sprintf("showing lines %d–%d of %d (default %d-line window) — pass offset=%d to read the next page, or use query/context/ast_query to get just the symbol you need instead of paging the whole file.",
				lineStart, lineEnd, totalLines, defaultReadLineWindow, lineEnd+1)
		case moreAfter && explicitLimit:
			out.Note = fmt.Sprintf("lines %d–%d of %d — pass offset=%d for the next slice.", lineStart, lineEnd, totalLines, lineEnd+1)
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

func writeWorkspaceFileHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		rawPath := argString(args, "path")
		if strings.TrimSpace(rawPath) == "" {
			return mcp.NewToolResultError("path is required"), nil
		}
		content := argString(args, "content")
		if len(content) > maxWriteFileBytes {
			return mcp.NewToolResultError(fmt.Sprintf("content exceeds max %d bytes", maxWriteFileBytes)), nil
		}
		// Never leave 0-byte artifacts: empty content is refused unless the caller
		// explicitly opts in. This covers both create and overwrite paths.
		if len(content) == 0 && !argBool(args, "allow_empty", false) {
			return mcp.NewToolResultError(
				"refusing write: content is empty (would create a 0-byte file). " +
					"Pass allow_empty=true only if intentional, or provide non-empty content.",
			), nil
		}

		rel, err := relativePathUnderRepo(repo.RootPath, rawPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if workspacePathBlocked(rel, true) {
			return mcp.NewToolResultError("writes are blocked for this path (.git, node_modules, or .env*)"), nil
		}

		abs := filepath.Join(repo.RootPath, filepath.FromSlash(rel))
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			return mcp.NewToolResultError("path is a directory, not a file"), nil
		}

		createDirs := argBool(args, "create_directories", true)
		if createDirs {
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}

		prevBytes, prevExists, readErr := readIfExists(abs)
		if readErr != nil {
			return mcp.NewToolResultError(readErr.Error()), nil
		}
		created := !prevExists

		// No-op short circuit: nothing on disk needs touching, so don't
		// create a snapshot, don't touch mtime, and return an empty diff
		// with no_op=true so callers can tell the write was idempotent.
		if prevExists && bytes.Equal(prevBytes, []byte(content)) {
			out := writeWorkspaceFileResponse{
				Path:         filepath.ToSlash(rel),
				RepoRoot:     filepath.ToSlash(repo.RootPath),
				BytesBefore:  len(prevBytes),
				BytesWritten: len(content),
				Created:      false,
				NoOp:         true,
				Diff:         "",
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		}

		if prevExists && !argBool(args, "allow_truncate", false) {
			if reason := truncationLooksWrong(prevBytes, []byte(content)); reason != "" {
				return mcp.NewToolResultError(
					"refusing write: " + reason +
						" — pass allow_truncate=true if intentional, or use apply_patch_workspace_file for surgical edits.",
				), nil
			}
		}

		token := ""
		if prevExists {
			t, snapErr := snapshotPreEdit(repo.RootPath, rel, prevBytes)
			if snapErr != nil {
				return mcp.NewToolResultError("snapshot failed: " + snapErr.Error()), nil
			}
			token = t
		}

		if err := atomicWrite(abs, []byte(content)); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		diff, elided := unifiedDiffWithCap(filepath.ToSlash(rel), string(prevBytes), content, appliedDiffCap)

		out := writeWorkspaceFileResponse{
			Path:         filepath.ToSlash(rel),
			RepoRoot:     filepath.ToSlash(repo.RootPath),
			BytesBefore:  len(prevBytes),
			BytesWritten: len(content),
			Created:      created,
			NoOp:         false,
			RevertToken:  token,
			Diff:         diff,
			DiffElided:   elided,
		}
		if created {
			out.Note = "file did not exist; no revert_token is issued. To undo, delete the file directly."
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return mcp.NewToolResultError("failed to encode response"), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

type patchHunk struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
	// Exact skips whitespace-tolerant re-anchoring and applies new_string only on a
	// byte-exact old_string match (still restyles indent chars to the file's style).
	Exact bool `json:"exact,omitempty"`
}

func applyPatchWorkspaceFileHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		rawPath := argString(args, "path")
		if strings.TrimSpace(rawPath) == "" {
			return mcp.NewToolResultError("path is required"), nil
		}
		rel, err := relativePathUnderRepo(repo.RootPath, rawPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if workspacePathBlocked(rel, true) {
			return mcp.NewToolResultError("writes are blocked for this path (.git, node_modules, or .env*)"), nil
		}

		hunks, err := resolvePatchHunks(args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(hunks) == 0 {
			return mcp.NewToolResultError("hunks must contain at least one entry"), nil
		}
		if len(hunks) > maxPatchHunks {
			return mcp.NewToolResultError(fmt.Sprintf("too many hunks (max %d)", maxPatchHunks)), nil
		}

		abs := filepath.Join(repo.RootPath, filepath.FromSlash(rel))
		st, statErr := os.Stat(abs)
		if statErr != nil {
			return mcp.NewToolResultError("file not found (apply_patch requires existing file): " + statErr.Error()), nil
		}
		if st.IsDir() {
			return mcp.NewToolResultError("path is a directory, not a file"), nil
		}

		prevBytes, err := os.ReadFile(abs)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if !utf8.Valid(prevBytes) {
			return mcp.NewToolResultError("apply_patch only supports UTF-8 text files"), nil
		}

		updated, applied, fuzzy, err := applyHunks(string(prevBytes), hunks)
		if err != nil {
			if phe, ok := err.(*patchHunkError); ok {
				payload := map[string]any{
					"error":              phe.Error(),
					"hunk_index":         phe.HunkIndex,
					"reason":             phe.Reason,
					"match_count":        phe.MatchCount,
					"old_string_snippet": phe.OldSnippet,
					"file_excerpt":       phe.FileExcerpt,
					"hint":               phe.Hint,
					"path":               filepath.ToSlash(rel),
				}
				if b, jerr := json.MarshalIndent(payload, "", "  "); jerr == nil {
					return mcp.NewToolResultError(string(b)), nil
				}
			}
			return mcp.NewToolResultError(err.Error()), nil
		}
		if len(updated) > maxWriteFileBytes {
			return mcp.NewToolResultError(fmt.Sprintf("patched content exceeds max %d bytes", maxWriteFileBytes)), nil
		}

		dryRun := argBool(args, "dry_run", false)
		diffCap := appliedDiffCap
		if dryRun {
			diffCap = previewDiffCap
		}
		diff, elided := unifiedDiffWithCap(filepath.ToSlash(rel), string(prevBytes), updated, diffCap)

		token := ""
		if !dryRun {
			t, snapErr := snapshotPreEdit(repo.RootPath, rel, prevBytes)
			if snapErr != nil {
				return mcp.NewToolResultError("snapshot failed: " + snapErr.Error()), nil
			}
			token = t
			if err := atomicWrite(abs, []byte(updated)); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}

		out := applyPatchResponse{
			Path:         filepath.ToSlash(rel),
			RepoRoot:     filepath.ToSlash(repo.RootPath),
			BytesBefore:  len(prevBytes),
			BytesAfter:   len(updated),
			HunksApplied: applied,
			RevertToken:  token,
			Diff:         diff,
			DiffElided:   elided,
			DryRun:       dryRun,
		}
		if fuzzy > 0 {
			out.WhitespaceAdjusted = fuzzy
			out.Note = fmt.Sprintf("%d hunk(s) matched after normalizing whitespace/indentation; new text was reindented to the file's actual style. Verify the diff.", fuzzy)
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return mcp.NewToolResultError("failed to encode response"), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

func revertWorkspaceEditHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		args := req.GetArguments()
		token := strings.TrimSpace(argString(args, "revert_token"))
		if token == "" {
			return mcp.NewToolResultError("revert_token is required"), nil
		}
		snap, err := loadSnapshot(reg, token)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if snap.Reverted {
			out := revertResponse{
				Path:        snap.RelPath,
				RepoRoot:    filepath.ToSlash(snap.RepoRoot),
				Restored:    true,
				AlreadyDone: true,
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		}
		abs := filepath.Join(snap.RepoRoot, filepath.FromSlash(snap.RelPath))
		if err := atomicWrite(abs, snap.Content); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := markSnapshotReverted(snap); err != nil {
			// best-effort marker; restore already succeeded
			_ = err
		}
		out := revertResponse{
			Path:     snap.RelPath,
			RepoRoot: filepath.ToSlash(snap.RepoRoot),
			Restored: true,
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return mcp.NewToolResultError("failed to encode response"), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

func listWorkspaceDirectoryHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = ctx
		args := req.GetArguments()
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		rawPath := argString(args, "path")
		if strings.TrimSpace(rawPath) == "" {
			rawPath = "."
		}
		rel, err := relativePathUnderRepo(repo.RootPath, rawPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if workspacePathBlocked(rel, false) {
			return mcp.NewToolResultError("listing this path is not allowed"), nil
		}

		maxEnt := int(argFloat(args, "max_entries", defaultMaxListEntries))
		if maxEnt < 1 {
			maxEnt = defaultMaxListEntries
		}
		if maxEnt > absoluteMaxListEntries {
			maxEnt = absoluteMaxListEntries
		}

		abs := filepath.Join(repo.RootPath, filepath.FromSlash(rel))
		st, err := os.Stat(abs)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if !st.IsDir() {
			return mcp.NewToolResultError("path is not a directory"), nil
		}

		entries, err := os.ReadDir(abs)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

		total := len(entries)
		truncated := total > maxEnt
		if truncated {
			entries = entries[:maxEnt]
		}

		outList := make([]dirEntry, 0, len(entries))
		for _, e := range entries {
			isDir := e.IsDir()
			var size int64
			if et, err := e.Info(); err == nil {
				isDir = et.IsDir()
				if !isDir {
					size = et.Size()
				}
			}
			outList = append(outList, dirEntry{Name: e.Name(), IsDir: isDir, SizeBytes: size})
		}

		out := listWorkspaceDirectoryResponse{
			Path:                 filepath.ToSlash(rel),
			RepoRoot:             filepath.ToSlash(repo.RootPath),
			Truncated:            truncated,
			Entries:              outList,
			TotalFound:           total,
			RecommendedNextTools: []string{"query", "scout", "context"},
			Confidence:           0.82,
		}
		for _, e := range outList {
			if !e.IsDir {
				out.EvidencePaths = append(out.EvidencePaths, filepath.ToSlash(filepath.Join(rel, e.Name)))
			}
			if len(out.EvidencePaths) >= 12 {
				break
			}
		}
		if truncated {
			out.Warnings = append(out.Warnings, "directory listing truncated at max_entries")
		}
		return mustToolResultFormatted(out, resolveFormat(args))
	}
}

// patchHunkError carries enough context for the LLM to repair the patch on the
// next round (hunk index, why it failed, current match count, and an optional
// snippet of the file showing what is actually there).
type patchHunkError struct {
	HunkIndex   int    `json:"hunk_index"`
	Reason      string `json:"reason"`
	MatchCount  int    `json:"match_count"`
	OldSnippet  string `json:"old_string_snippet,omitempty"`
	FileExcerpt string `json:"file_excerpt,omitempty"`
	Hint        string `json:"hint"`
}

func (e *patchHunkError) Error() string {
	return fmt.Sprintf("hunk %d: %s", e.HunkIndex, e.Reason)
}

// firstLineOf returns the first line of s, truncated to maxRunes runes.
func firstLineOf(s string, maxRunes int) string {
	idx := strings.IndexByte(s, '\n')
	if idx >= 0 {
		s = s[:idx]
	}
	rs := []rune(s)
	if len(rs) <= maxRunes {
		return string(rs)
	}
	return string(rs[:maxRunes]) + "…"
}

// nearestFileExcerpt finds a short window of the file content closest to the
// first non-empty line of old_string, so the model can see what is actually
// there and rebuild the hunk on the next round.
func nearestFileExcerpt(content, oldString string) string {
	probe := strings.TrimSpace(firstLineOf(oldString, 80))
	if probe == "" {
		return ""
	}
	idx := strings.Index(content, probe)
	if idx < 0 && len(probe) > 12 {
		idx = strings.Index(content, probe[:12])
	}
	if idx < 0 {
		return ""
	}
	start := idx - 80
	if start < 0 {
		start = 0
	}
	end := idx + len(probe) + 80
	if end > len(content) {
		end = len(content)
	}
	return content[start:end]
}

// applyHunks runs ordered search/replace operations. Each old_string must appear
// exactly once in the current buffer unless replace_all=true. Returns the
// rewritten content and the number of hunks applied. On failure it returns a
// *patchHunkError with diagnostic context the agent can use to self-correct.
func applyHunks(content string, hunks []patchHunk) (string, int, int, error) {
	cur := content
	fuzzy := 0
	for i, h := range hunks {
		if h.OldString == "" {
			return "", 0, 0, &patchHunkError{
				HunkIndex: i + 1,
				Reason:    "old_string is empty",
				Hint:      "Provide non-empty `old_string` text copied verbatim from the file.",
			}
		}
		if h.OldString == h.NewString {
			return "", 0, 0, &patchHunkError{
				HunkIndex:  i + 1,
				Reason:     "old_string equals new_string (no-op)",
				OldSnippet: firstLineOf(h.OldString, 120),
				Hint:       "Set `new_string` to the actual replacement text.",
			}
		}
		count := strings.Count(cur, h.OldString)
		if count == 0 {
			// Exact match failed. The #1 cause is whitespace/indentation drift (tabs
			// vs spaces, trailing space). Try a line-anchored, whitespace-tolerant
			// re-anchor: if old_string matches EXACTLY ONE span modulo per-line
			// whitespace, splice new_string in (reindented to the file's real base
			// indentation). Only the unambiguous single-span case is auto-applied —
			// anything else falls through to the precise error below.
			if !h.Exact {
				if res, ok := tolerantReplace(cur, h.OldString, h.NewString); ok {
					cur = res
					fuzzy++
					continue
				}
			}
			hint := "Re-read the file with `read_workspace_file` and copy `old_string` byte-for-byte from the current content " +
				"(same indentation, same trailing newlines). If indentation uses tabs vs spaces, copy what is actually on disk."
			reason := "old_string not found in file (matches must be exact, including whitespace and newlines)"
			if !h.Exact && whitespaceInsensitiveContains(cur, h.OldString) {
				reason = "old_string not found EXACTLY, and a whitespace-only match was NOT unique enough to auto-apply (appears 0 or >1 times after normalizing indentation)"
				hint = "The lines exist but the indentation differs AND the snippet isn't unique. Add 3–5 surrounding lines to old_string so it anchors to exactly one place, then retry."
			}
			if h.Exact {
				reason = "old_string not found (exact=true: whitespace-tolerant match disabled)"
				hint = "Copy `old_string` byte-for-byte from the file, or omit exact=true to allow indent-tolerant re-anchoring."
			}
			return "", 0, 0, &patchHunkError{
				HunkIndex:   i + 1,
				Reason:      reason,
				MatchCount:  0,
				OldSnippet:  firstLineOf(h.OldString, 120),
				FileExcerpt: nearestFileExcerpt(cur, h.OldString),
				Hint:        hint,
			}
		}
		if !h.ReplaceAll && count > 1 {
			return "", 0, 0, &patchHunkError{
				HunkIndex:  i + 1,
				Reason:     "old_string matches multiple times",
				MatchCount: count,
				OldSnippet: firstLineOf(h.OldString, 120),
				Hint: "Expand `old_string` with 3–5 surrounding lines until it matches exactly once, " +
					"or pass `replace_all: true` if you really mean every match.",
			}
		}
		// Preserve the file's tab/space style even on exact matches: agents often
		// paste new_string with the opposite indent character.
		newStr := unifyIndentChars(h.NewString, h.OldString)
		if h.ReplaceAll {
			cur = strings.ReplaceAll(cur, h.OldString, newStr)
		} else {
			cur = strings.Replace(cur, h.OldString, newStr, 1)
		}
	}
	return cur, len(hunks), fuzzy, nil
}

// tolerantReplace re-anchors a failed exact match using line-leading/trailing
// whitespace tolerance. It matches old_string's lines against the file with each
// line's whitespace normalized (leading/trailing stripped, internal runs collapsed),
// requires EXACTLY ONE matching span (so we never guess between candidates), and
// splices new_string in place of the real on-disk lines. new_string is reindented so
// its base indentation matches the file's actual indentation at the match — fixing
// the tabs-vs-spaces / off-by-indent drift that caused the miss, without importing the
// agent's wrong indentation into the file. Returns ("", false) to fall back to a
// precise error whenever the match isn't a single, confident span.
func tolerantReplace(cur, oldStr, newStr string) (string, bool) {
	fileLines := strings.Split(cur, "\n")
	oldLines := strings.Split(strings.TrimRight(oldStr, "\n"), "\n")
	if len(oldLines) == 0 || (len(oldLines) == 1 && strings.TrimSpace(oldLines[0]) == "") {
		return "", false
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	oldNorm := make([]string, len(oldLines))
	for i, l := range oldLines {
		oldNorm[i] = norm(l)
	}
	var starts []int
	for i := 0; i+len(oldLines) <= len(fileLines); i++ {
		match := true
		for j := range oldLines {
			if norm(fileLines[i+j]) != oldNorm[j] {
				match = false
				break
			}
		}
		if match {
			starts = append(starts, i)
		}
	}
	if len(starts) != 1 {
		return "", false // 0 = not present line-aligned; >1 = ambiguous — don't guess
	}
	at := starts[0]
	span := len(oldLines)
	fileIndent := leadingWS(fileLines[at])
	oldIndent := leadingWS(oldLines[0])
	newLines := strings.Split(strings.TrimRight(newStr, "\n"), "\n")
	// Preserve relative indent; never invent a base indent when the agent's
	// old_string had none (that rewrote banner " * express" into "    * express").
	reindented := make([]string, len(newLines))
	for i, l := range newLines {
		if oldIndent != "" && strings.HasPrefix(l, oldIndent) {
			reindented[i] = fileIndent + l[len(oldIndent):]
		} else {
			reindented[i] = l
		}
	}
	// Final pass: ensure indent *characters* match the file line (tabs vs spaces).
	styled := unifyIndentChars(strings.Join(reindented, "\n"), fileLines[at])
	reindented = strings.Split(styled, "\n")
	out := make([]string, 0, len(fileLines)-span+len(reindented))
	out = append(out, fileLines[:at]...)
	out = append(out, reindented...)
	out = append(out, fileLines[at+span:]...)
	return strings.Join(out, "\n"), true
}

// leadingWS returns the leading run of spaces/tabs in s.
func leadingWS(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// windowLines returns the slice of content for a 1-based offset and limit. A
// limit<=0 applies the default window (defaultReadLineWindow), so a blind read of a
// large file pages instead of dumping the whole thing; files within the window return
// in full. Returns the sliced content plus the 1-based start/end actually returned and
// the total line count (so the caller can tell whether more lines follow).
func windowLines(content string, offset, limit int) (sliced string, lineStart, lineEnd, totalLines int) {
	totalLines = strings.Count(content, "\n") + 1
	start := offset
	if start < 1 {
		start = 1
	}
	eff := limit
	if eff <= 0 {
		eff = defaultReadLineWindow
	}
	lines := strings.Split(content, "\n")
	if start <= len(lines) {
		end := start + eff - 1
		if end > len(lines) {
			end = len(lines)
		}
		return strings.Join(lines[start-1:end], "\n"), start, end, totalLines
	}
	return "", start, start - 1, totalLines
}

// whitespaceInsensitiveContains reports whether needle appears in hay once each
// line's leading/trailing whitespace is stripped and internal runs collapsed —
// i.e. the text is present but indentation/spacing drifted (the #1 cause of a
// failed patch). Used only to make the error message precise.
func whitespaceInsensitiveContains(hay, needle string) bool {
	n := normalizeWS(needle)
	return n != "" && strings.Contains(normalizeWS(hay), n)
}

func normalizeWS(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		b.WriteString(strings.Join(strings.Fields(line), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func decodeHunks(raw any) ([]patchHunk, error) {
	if raw == nil {
		return nil, fmt.Errorf("hunks is required")
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("hunks must be an array of {old_string,new_string} objects (not a unified-diff string). Pass hunks=[{old_string,new_string}] — aliases edits/changes/replacements also work")
	}
	out := make([]patchHunk, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("hunks[%d] must be an object", i)
		}
		h := patchHunk{
			OldString:  firstMapString(m, "old_string", "old", "before", "search"),
			NewString:  firstMapString(m, "new_string", "new", "after", "replace"),
			ReplaceAll: argMapBool(m, "replace_all", false) || argMapBool(m, "replaceAll", false),
			Exact:      argMapBool(m, "exact", false),
		}
		out = append(out, h)
	}
	return out, nil
}

// resolvePatchHunks accepts the canonical `hunks` array plus common LLM aliases
// (edits/changes/replacements/patch-as-array) and a minimal unified-diff `patch`
// string (context + -/+ lines without file headers).
func resolvePatchHunks(args map[string]any) ([]patchHunk, error) {
	for _, key := range []string{"hunks", "edits", "changes", "replacements", "patches"} {
		if raw, ok := args[key]; ok && raw != nil {
			return decodeHunks(raw)
		}
	}
	if raw, ok := args["patch"]; ok && raw != nil {
		switch v := raw.(type) {
		case []any:
			return decodeHunks(v)
		case string:
			hunks, err := hunksFromUnifiedDiff(v)
			if err != nil {
				return nil, err
			}
			return hunks, nil
		default:
			return nil, fmt.Errorf("patch must be a hunks array or a unified-diff string")
		}
	}
	return nil, fmt.Errorf("hunks is required (array of {old_string,new_string}); aliases: edits, changes, replacements, or patch")
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := argMapString(m, k); strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// hunksFromUnifiedDiff parses a simplified unified diff (optional @@ header,
// lines prefixed with space/-/+) into a single search/replace hunk.
func hunksFromUnifiedDiff(diff string) ([]patchHunk, error) {
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return nil, fmt.Errorf("patch string is empty; pass hunks=[{old_string,new_string}] instead")
	}
	lines := strings.Split(diff, "\n")
	var oldLines, newLines []string
	sawChange := false
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") {
			continue
		}
		if line == "" {
			// blank lines in diffs are context empties when prefixed; bare blank keep as context
			oldLines = append(oldLines, "")
			newLines = append(newLines, "")
			continue
		}
		switch line[0] {
		case ' ':
			oldLines = append(oldLines, line[1:])
			newLines = append(newLines, line[1:])
		case '-':
			oldLines = append(oldLines, line[1:])
			sawChange = true
		case '+':
			newLines = append(newLines, line[1:])
			sawChange = true
		default:
			// Treat unprefixed lines as context (LLMs often omit the leading space).
			oldLines = append(oldLines, line)
			newLines = append(newLines, line)
		}
	}
	if !sawChange {
		return nil, fmt.Errorf("patch string has no +/- change lines; use hunks=[{old_string,new_string}]")
	}
	oldStr := strings.Join(oldLines, "\n")
	newStr := strings.Join(newLines, "\n")
	if oldStr == newStr {
		return nil, fmt.Errorf("patch produces no change")
	}
	// Source files almost always use newline-terminated snippets; add one when
	// missing so a common LLM unified-diff matches on-disk content.
	if oldStr != "" && !strings.HasSuffix(oldStr, "\n") {
		oldStr += "\n"
	}
	if newStr != "" && !strings.HasSuffix(newStr, "\n") {
		newStr += "\n"
	}
	return []patchHunk{{OldString: oldStr, NewString: newStr}}, nil
}

// truncationLooksWrong returns a non-empty reason if a full-content write
// looks like a truncated/half-baked LLM response. Conservative: only fires on
// the obvious cases.
func truncationLooksWrong(prev, next []byte) string {
	if len(prev) == 0 {
		return ""
	}
	if len(next) == 0 {
		return "new content is empty (previous file was non-empty)"
	}
	// Big shrink that also drops a trailing newline → most likely truncation.
	shrinkRatio := float64(len(prev)-len(next)) / float64(len(prev))
	prevEndsNL := prev[len(prev)-1] == '\n'
	nextEndsNL := next[len(next)-1] == '\n'
	if shrinkRatio > 0.5 && prevEndsNL && !nextEndsNL {
		return fmt.Sprintf(
			"new content shrinks file by %.0f%% and is missing trailing newline (looks truncated)",
			shrinkRatio*100,
		)
	}
	// Ends mid-token (line break + identifier without value). The classic case
	// is `*.` from the user's terminal: the new content's last non-empty line
	// ends with an obviously incomplete glob/symbol.
	lastLine := tailLine(next)
	if isObviouslyTruncatedTail(lastLine) {
		return fmt.Sprintf("new content ends with %q which looks like a truncated line", lastLine)
	}
	return ""
}

func tailLine(b []byte) string {
	s := string(b)
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func isObviouslyTruncatedTail(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	// `*.` `**.` etc. with no extension.
	if strings.HasSuffix(t, ".") && (strings.HasPrefix(t, "*") || strings.HasPrefix(t, "**")) {
		return true
	}
	// Trailing structural openers that never closed on the same line.
	if strings.HasSuffix(t, "{") || strings.HasSuffix(t, "(") || strings.HasSuffix(t, "[") {
		return false // openers alone are usually fine
	}
	// Half-open string/quote heuristic.
	if strings.Count(t, `"`)%2 == 1 || strings.Count(t, "`")%2 == 1 {
		return true
	}
	return false
}

// atomicWrite writes via *.tmp + rename for crash safety. On Linux this also
// gives sane behavior when the target is currently being read. The temp file is
// size-checked before rename so a short write never replaces the target with a
// truncated/empty artifact.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".codehelper-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	n, err := tmp.Write(data)
	if err != nil {
		cleanup()
		return err
	}
	if n != len(data) {
		cleanup()
		return fmt.Errorf("atomic write short write: wrote %d of %d bytes", n, len(data))
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	st, err := os.Stat(tmpName)
	if err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if st.Size() != int64(len(data)) {
		_ = os.Remove(tmpName)
		return fmt.Errorf("atomic write size mismatch: temp has %d bytes, expected %d", st.Size(), len(data))
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// unifyIndentChars restyles leading indentation in newStr to match the tab/space
// convention of styleSample (typically old_string copied from the file). Relative
// indent depth is preserved; only the indent character/unit is adapted. Blank
// lines are left untouched. Decorative one-space prefixes (JSDoc / banner
// " * comment" lines) are NOT restyled — expanding them to 4 spaces corrupted
// license headers in dry-run diffs.
func unifyIndentChars(newStr, styleSample string) string {
	preferTabs, spaceWidth := indentConvention(styleSample)
	if !preferTabs && spaceWidth <= 0 {
		return newStr
	}
	lines := strings.Split(newStr, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		ws := leadingWS(line)
		if ws == "" {
			continue
		}
		// Preserve decorative single-space prefixes (banner / block-comment stars).
		if !preferTabs && ws == " " {
			continue
		}
		body := line[len(ws):]
		depth := indentDepth(ws, spaceWidth)
		if preferTabs {
			lines[i] = strings.Repeat("\t", depth) + body
		} else {
			lines[i] = strings.Repeat(" ", depth*spaceWidth) + body
		}
	}
	return strings.Join(lines, "\n")
}

// indentConvention inspects styleSample and reports whether it prefers tabs and,
// for spaces, the indent width (2 or 4). Returns spaceWidth=0 (do not restyle)
// when the sample only shows decorative 1-space indents or other odd widths that
// are not a real indent unit.
func indentConvention(sample string) (preferTabs bool, spaceWidth int) {
	spaceWidth = 4
	sawTabs, sawSpaces := false, false
	minSpaces := 0
	for _, line := range strings.Split(sample, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		ws := leadingWS(line)
		if ws == "" {
			continue
		}
		if strings.Contains(ws, "\t") {
			sawTabs = true
		}
		spaces := strings.Count(ws, " ")
		if spaces > 0 && !strings.Contains(ws, "\t") {
			sawSpaces = true
			if minSpaces == 0 || spaces < minSpaces {
				minSpaces = spaces
			}
		}
	}
	if sawTabs && !sawSpaces {
		return true, 4
	}
	if sawSpaces {
		// Only restyle when we see a real indent unit (2 or 4 spaces). A lone
		// 1-space prefix (banner comments) must not expand to 4 spaces.
		if minSpaces == 1 {
			return false, 0
		}
		if minSpaces > 0 && minSpaces%2 == 0 && minSpaces <= 8 {
			spaceWidth = minSpaces
			if spaceWidth > 4 {
				spaceWidth = 4
			}
			if spaceWidth == 0 {
				spaceWidth = 4
			}
		} else {
			return false, 0
		}
		return false, spaceWidth
	}
	if sawTabs {
		return true, 4
	}
	return false, 0
}

func indentDepth(ws string, spaceWidth int) int {
	if spaceWidth <= 0 {
		spaceWidth = 4
	}
	depth := 0
	i := 0
	for i < len(ws) {
		if ws[i] == '\t' {
			depth++
			i++
			continue
		}
		if ws[i] == ' ' {
			n := 0
			for i < len(ws) && ws[i] == ' ' {
				n++
				i++
			}
			depth += (n + spaceWidth - 1) / spaceWidth
			continue
		}
		break
	}
	return depth
}

func readIfExists(path string) (content []byte, exists bool, err error) {
	b, readErr := os.ReadFile(path)
	if readErr == nil {
		return b, true, nil
	}
	if os.IsNotExist(readErr) {
		return nil, false, nil
	}
	return nil, false, readErr
}

// snapshotMeta is persisted alongside each snapshot so revert can locate the
// original file and detect double-reverts.
type snapshotMeta struct {
	Version   string `json:"version"`
	Token     string `json:"token"`
	RepoRoot  string `json:"repo_root"`
	RelPath   string `json:"rel_path"`
	Timestamp string `json:"ts"`
	Reverted  bool   `json:"reverted"`
	Content   []byte `json:"-"`
}

func snapshotPreEdit(repoRoot, rel string, prev []byte) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("snapshot: empty rel path")
	}
	token, err := newSnapshotToken()
	if err != nil {
		return "", err
	}
	snapDir := filepath.Join(repoRoot, filepath.FromSlash(editsRelDir), token)
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(snapDir, "content.bin"), prev, 0o644); err != nil {
		return "", err
	}
	meta := snapshotMeta{
		Version:   revertTokenVersion,
		Token:     token,
		RepoRoot:  repoRoot,
		RelPath:   filepath.ToSlash(rel),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Reverted:  false,
	}
	mb, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(snapDir, "meta.json"), mb, 0o644); err != nil {
		return "", err
	}
	return token, nil
}

func loadSnapshot(reg *registry.Registry, token string) (*snapshotMeta, error) {
	if !isPlausibleToken(token) {
		return nil, fmt.Errorf("invalid revert_token")
	}
	for _, entry := range reg.List() {
		dir := filepath.Join(entry.RootPath, filepath.FromSlash(editsRelDir), token)
		metaPath := filepath.Join(dir, "meta.json")
		if _, err := os.Stat(metaPath); err != nil {
			continue
		}
		raw, err := os.ReadFile(metaPath)
		if err != nil {
			return nil, err
		}
		var meta snapshotMeta
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("snapshot meta corrupt: %w", err)
		}
		if meta.Token != token {
			return nil, fmt.Errorf("snapshot token mismatch")
		}
		content, err := os.ReadFile(filepath.Join(dir, "content.bin"))
		if err != nil {
			return nil, err
		}
		meta.Content = content
		return &meta, nil
	}
	return nil, fmt.Errorf("revert_token not found (snapshot may have been cleaned up)")
}

func markSnapshotReverted(snap *snapshotMeta) error {
	if snap == nil {
		return nil
	}
	snap.Reverted = true
	mb, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	dir := filepath.Join(snap.RepoRoot, filepath.FromSlash(editsRelDir), snap.Token)
	return os.WriteFile(filepath.Join(dir, "meta.json"), mb, 0o644)
}

func newSnapshotToken() (string, error) {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(buf[:]), nil
}

func isPlausibleToken(t string) bool {
	if len(t) < 8 || len(t) > 128 {
		return false
	}
	for _, r := range t {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'Z':
		case r == '-' || r == 'T':
		default:
			return false
		}
	}
	return true
}

// unifiedDiffWithCap produces a minimal unified diff. Caps output at maxBytes
// and reports whether it was elided.
func unifiedDiffWithCap(path, before, after string, maxBytes int) (string, bool) {
	if before == after {
		return "", false
	}
	beforeLines := splitKeepNL(before)
	afterLines := splitKeepNL(after)
	hunks := diffHunks(beforeLines, afterLines, 3)
	var sb strings.Builder
	sb.WriteString("--- a/")
	sb.WriteString(path)
	sb.WriteByte('\n')
	sb.WriteString("+++ b/")
	sb.WriteString(path)
	sb.WriteByte('\n')
	for _, h := range hunks {
		sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", h.beforeStart, h.beforeCount, h.afterStart, h.afterCount))
		for _, ln := range h.lines {
			sb.WriteString(ln)
			if !strings.HasSuffix(ln, "\n") {
				sb.WriteByte('\n')
			}
		}
		if sb.Len() > maxBytes {
			return sb.String()[:maxBytes] + "\n...(diff truncated)\n", true
		}
	}
	return sb.String(), false
}

func splitKeepNL(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.SplitAfter(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

type diffHunk struct {
	beforeStart, beforeCount int
	afterStart, afterCount   int
	lines                    []string
}

// diffHunks computes a minimal LCS-based diff and groups changes with `context` lines of surrounding context.
func diffHunks(a, b []string, context int) []diffHunk {
	ops := lcsDiff(a, b)
	hunks := []diffHunk{}
	i := 0
	for i < len(ops) {
		// Skip leading equal runs
		for i < len(ops) && ops[i].kind == opEqual {
			i++
		}
		if i >= len(ops) {
			break
		}
		// Capture context before
		start := i - context
		if start < 0 {
			start = 0
		}
		// Find end of change run (with up to 2*context equal lines tolerated inside)
		end := i
		gap := 0
		for end < len(ops) {
			if ops[end].kind == opEqual {
				gap++
				if gap > context*2 {
					break
				}
			} else {
				gap = 0
			}
			end++
		}
		// Trim trailing equal beyond context lines
		trail := end
		for trail > i && ops[trail-1].kind == opEqual {
			trail--
		}
		stopCtx := trail + context
		if stopCtx > len(ops) {
			stopCtx = len(ops)
		}

		var lines []string
		var beforeStart, afterStart int
		for k := start; k < stopCtx; k++ {
			op := ops[k]
			if k == start {
				beforeStart = op.aIdx + 1
				afterStart = op.bIdx + 1
			}
			switch op.kind {
			case opEqual:
				lines = append(lines, " "+op.text)
			case opDel:
				lines = append(lines, "-"+op.text)
			case opAdd:
				lines = append(lines, "+"+op.text)
			}
		}
		bc, ac := countSides(lines)
		hunks = append(hunks, diffHunk{
			beforeStart: beforeStart,
			beforeCount: bc,
			afterStart:  afterStart,
			afterCount:  ac,
			lines:       lines,
		})
		i = stopCtx
	}
	return hunks
}

func countSides(lines []string) (int, int) {
	b, a := 0, 0
	for _, l := range lines {
		if len(l) == 0 {
			continue
		}
		switch l[0] {
		case ' ':
			b++
			a++
		case '-':
			b++
		case '+':
			a++
		}
	}
	return b, a
}

type diffOpKind int

const (
	opEqual diffOpKind = iota
	opDel
	opAdd
)

type diffOp struct {
	kind diffOpKind
	text string
	aIdx int
	bIdx int
}

// lcsDiff builds an LCS table to derive a minimal edit script. For very large
// inputs we fall back to a coarser line-by-line diff to bound memory.
func lcsDiff(a, b []string) []diffOp {
	const maxLcsLines = 4000
	if len(a) > maxLcsLines || len(b) > maxLcsLines {
		return coarseDiff(a, b)
	}
	n := len(a)
	m := len(b)
	// dp[i][j] = LCS length for a[i:], b[j:]
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			ops = append(ops, diffOp{kind: opEqual, text: trimNL(a[i]), aIdx: i, bIdx: j})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			ops = append(ops, diffOp{kind: opDel, text: trimNL(a[i]), aIdx: i, bIdx: j})
			i++
		} else {
			ops = append(ops, diffOp{kind: opAdd, text: trimNL(b[j]), aIdx: i, bIdx: j})
			j++
		}
	}
	for i < n {
		ops = append(ops, diffOp{kind: opDel, text: trimNL(a[i]), aIdx: i, bIdx: j})
		i++
	}
	for j < m {
		ops = append(ops, diffOp{kind: opAdd, text: trimNL(b[j]), aIdx: i, bIdx: j})
		j++
	}
	return ops
}

func coarseDiff(a, b []string) []diffOp {
	out := make([]diffOp, 0, len(a)+len(b))
	for i, l := range a {
		out = append(out, diffOp{kind: opDel, text: trimNL(l), aIdx: i, bIdx: 0})
	}
	for j, l := range b {
		out = append(out, diffOp{kind: opAdd, text: trimNL(l), aIdx: len(a), bIdx: j})
	}
	return out
}

func trimNL(s string) string {
	return strings.TrimRight(s, "\n")
}

// relativePathUnderRepo returns a slash-separated path relative to repoRoot; rejects escapes.
func relativePathUnderRepo(repoRoot, userPath string) (string, error) {
	rr, err := filepath.Abs(filepath.Clean(repoRoot))
	if err != nil {
		return "", err
	}
	up := strings.TrimSpace(userPath)
	if up == "" {
		return "", fmt.Errorf("path is empty")
	}
	var joined string
	if filepath.IsAbs(up) {
		joined = filepath.Clean(up)
	} else {
		joined = filepath.Join(rr, filepath.FromSlash(up))
		joined = filepath.Clean(joined)
	}
	absFile, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	sep := string(filepath.Separator)
	if absFile != rr && !strings.HasPrefix(absFile, rr+sep) {
		return "", fmt.Errorf("path escapes repository root")
	}
	rel, err := filepath.Rel(rr, absFile)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository root")
	}
	return filepath.ToSlash(rel), nil
}

func workspacePathBlocked(relPath string, forWrite bool) bool {
	norm := filepath.ToSlash(filepath.Clean(relPath))
	parts := strings.Split(norm, "/")
	for _, p := range parts {
		if p == ".git" {
			return true
		}
		if forWrite && p == "node_modules" {
			return true
		}
	}
	base := filepath.Base(norm)
	if forWrite && isBlockedEnvFilename(base) {
		return true
	}
	return false
}

// Blocks secret env files; allows .env.example / .env.sample templates.
func isBlockedEnvFilename(base string) bool {
	if base == ".env" {
		return true
	}
	if strings.HasPrefix(base, ".env.") && base != ".env.example" && base != ".env.sample" {
		return true
	}
	return false
}

func argFloat(args map[string]any, k string, def float64) float64 {
	if args == nil {
		return def
	}
	v, ok := args[k]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return def
		}
		return f
	default:
		return def
	}
}

func argMapString(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	v, ok := m[k]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func argMapBool(m map[string]any, k string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[k]
	if !ok || v == nil {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}
