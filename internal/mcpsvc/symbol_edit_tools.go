package mcpsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/pkg/types"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Symbolic editing tools — codehelper's heuristic, graph-driven answer to an
// LSP rename/insert. There is no language server and no go/types here: the
// indexer resolves references with conservative heuristics that have a known
// (~3%) miss rate and cannot see comments or string literals. So these tools are
// PREVIEW-FIRST and honest about confidence: every site is classified as
// graph-confirmed (an edge in the call/read graph) or textual-only (a
// word-boundary text match the graph did not confirm — possibly a same-named
// field, a comment, or a string). Applying requires an explicit apply=true, and
// textual-only sites are only touched when include_textual=true.

const symbolEditHeuristicNote = "heuristic, not type-aware (no language server, no go/types); review graph-confirmed sites and treat textual-only sites as unverified before applying."

// RegisterSymbolEditTools wires rename_symbol and insert_at_symbol.
func RegisterSymbolEditTools(s *server.MCPServer, reg *registry.Registry) {
	regRef := reg
	s.AddTool(mcp.NewTool("rename_symbol",
		mcp.WithDescription("Preview-first, graph-driven rename of a symbol and its references (codehelper's heuristic answer to an LSP rename — no language server, no go/types). Resolves the definition via the symbol graph, collects graph-confirmed reference sites, and ALSO runs a word-boundary textual scan to catch references the graph missed, classifying every site as graph-confirmed (high confidence) or textual-only (unverified — may be a same-named field, comment, or string). Returns a per-file plan by default; pass apply=true to write graph-confirmed sites (and include_textual=true to also write textual-only ones). Not type-aware: review before applying."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Current symbol name (or sym: id) to rename")),
		mcp.WithString("to", mcp.Required(), mcp.Description("New name")),
		mcp.WithString("path", mcp.Description("Definition file (relative to repo root) to disambiguate when several symbols share the name")),
		mcp.WithNumber("line", mcp.Description("Definition start line to disambiguate"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("apply", mcp.Description("Write the edits (default false — preview only)"), mcp.DefaultBool(false)),
		mcp.WithBoolean("include_textual", mcp.Description("When applying, also write textual-only (unverified) sites. Default false."), mcp.DefaultBool(false)),
		mcp.WithString("repo", mcp.Description("Repository name (optional; defaults to current MCP workspace)")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotWorkspaceWrite(),
	), timedTool("rename_symbol", renameSymbolHandler(regRef)))

	s.AddTool(mcp.NewTool("insert_at_symbol",
		mcp.WithDescription("Insert a block of code relative to a symbol's definition, located via the symbol graph (no language server). position is before|after|start_of_body|end_of_body. Returns a preview with surrounding context by default; pass apply=true to write. Heuristic line ranges come from the index — review the preview before applying."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Symbol name (or sym: id) to anchor the insertion")),
		mcp.WithString("position", mcp.Required(), mcp.Description("before | after | start_of_body | end_of_body (relative to the symbol's definition)")),
		mcp.WithString("text", mcp.Required(), mcp.Description("Code to insert")),
		mcp.WithString("path", mcp.Description("Definition file (relative to repo root) to disambiguate")),
		mcp.WithNumber("line", mcp.Description("Definition start line to disambiguate"), mcp.DefaultNumber(0)),
		mcp.WithBoolean("apply", mcp.Description("Write the edit (default false — preview only)"), mcp.DefaultBool(false)),
		mcp.WithString("repo", mcp.Description("Repository name (optional; defaults to current MCP workspace)")),
		mcp.WithString("format", mcp.Description("Response text encoding: toon (default) | json")),
		annotWorkspaceWrite(),
	), timedTool("insert_at_symbol", insertAtSymbolHandler(regRef)))
}

// renameSite is one identifier occurrence the rename would touch.
type renameSite struct {
	Line       int     `json:"line"`       // 1-based
	Col        int     `json:"col"`        // 1-based rune column of the identifier start
	Before     string  `json:"before"`     // the line as it is now
	After      string  `json:"after"`      // the line after rename
	Confidence string  `json:"confidence"` // graph-confirmed | definition | textual-only
	Score      float64 `json:"score"`      // numeric confidence for sorting
}

type renameFilePlan struct {
	Path           string       `json:"path"`
	GraphConfirmed []renameSite `json:"graph_confirmed,omitempty"`
	TextualOnly    []renameSite `json:"textual_only,omitempty"`
}

type renameCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Loc  string `json:"loc"` // path:line
	Recv string `json:"recv,omitempty"`
}

type renameSymbolResponse struct {
	Applied              bool              `json:"applied"`
	Name                 string            `json:"name"`
	To                   string            `json:"to"`
	Definition           string            `json:"definition,omitempty"` // path:line of the resolved def
	Ambiguous            bool              `json:"ambiguous,omitempty"`
	Candidates           []renameCandidate `json:"candidates,omitempty"`
	Files                []renameFilePlan  `json:"files,omitempty"`
	GraphConfirmedCount  int               `json:"graph_confirmed_count"`
	TextualOnlyCount     int               `json:"textual_only_count"`
	AppliedSiteCount     int               `json:"applied_site_count,omitempty"`
	RevertTokens         []string          `json:"revert_tokens,omitempty"`
	Note                 string            `json:"note"`
	Warnings             []string          `json:"warnings,omitempty"`
	RecommendedNextTools []string          `json:"recommended_next_tools,omitempty"`
	Confidence           float64           `json:"confidence,omitempty"`
}

type insertAtSymbolResponse struct {
	Applied              bool              `json:"applied"`
	Name                 string            `json:"name"`
	Position             string            `json:"position"`
	Path                 string            `json:"path,omitempty"`
	InsertAtLine         int               `json:"insert_at_line,omitempty"` // 1-based line the block is inserted before
	Ambiguous            bool              `json:"ambiguous,omitempty"`
	Candidates           []renameCandidate `json:"candidates,omitempty"`
	Preview              string            `json:"preview,omitempty"` // inserted block with surrounding context
	RevertToken          string            `json:"revert_token,omitempty"`
	Note                 string            `json:"note"`
	Warnings             []string          `json:"warnings,omitempty"`
	RecommendedNextTools []string          `json:"recommended_next_tools,omitempty"`
	Confidence           float64           `json:"confidence,omitempty"`
}

func renameSymbolHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		name := strings.TrimSpace(argString(args, "name"))
		to := strings.TrimSpace(argString(args, "to"))
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		if to == "" {
			return mcp.NewToolResultError("to (new name) is required"), nil
		}
		if to == name {
			return mcp.NewToolResultError("to must differ from name"), nil
		}
		if !isIdentifier(to) {
			return mcp.NewToolResultError("to must be a plain identifier (letters, digits, underscore; not starting with a digit)"), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		wantPath := strings.TrimSpace(argString(args, "path"))
		wantLine := int(argFloat(args, "line", 0))
		def, cands, err := resolveSymbolForEdit(ctx, st, repo.Name, name, wantPath, wantLine)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if def == nil {
			out := renameSymbolResponse{
				Name: name, To: to, Ambiguous: true, Candidates: cands,
				Note:                 "multiple symbols share this name; pass path and/or line to disambiguate. " + symbolEditHeuristicNote,
				RecommendedNextTools: []string{"query", "context"},
			}
			return mustToolResultStructured(out)
		}
		// The definition's own identifier is on its declaration line; rename only
		// the bare name even for methods (parent_id carries the receiver type).
		oldName := def.Name

		// Per-file collection of sites, keyed by relpath.
		plans := map[string]*renameFilePlan{}
		ensure := func(p string) *renameFilePlan {
			if pl, ok := plans[p]; ok {
				return pl
			}
			pl := &renameFilePlan{Path: p}
			plans[p] = pl
			return pl
		}

		// 1) Definition site: the identifier on the symbol's declaration line.
		defLines, _ := readFileLines(filepath.Join(repo.RootPath, filepath.FromSlash(def.Path)))
		if site, ok := siteOnLine(defLines, def.LineStart, oldName, to); ok {
			site.Confidence = "definition"
			site.Score = 1.0
			ensure(def.Path).GraphConfirmed = append(ensure(def.Path).GraphConfirmed, site)
		}

		// 2) Graph-confirmed reference sites: incoming calls/reads edges name a
		//    caller symbol; locate the identifier occurrence inside that caller's
		//    line range. These are the high-confidence sites.
		confirmedKeys := map[string]bool{} // "path:line" already taken by graph
		confirmedKeys[siteKey(def.Path, def.LineStart)] = true
		edges, _ := st.EdgesTo(ctx, repo.Name, def.ID, "calls", "reads")
		seenCaller := map[string]bool{}
		for _, e := range edges {
			if !strings.HasPrefix(e.SourceID, "sym:") || seenCaller[e.SourceID] {
				continue
			}
			seenCaller[e.SourceID] = true
			caller, _ := st.SymbolByID(ctx, repo.Name, e.SourceID)
			if caller == nil {
				continue
			}
			lines, _ := readFileLines(filepath.Join(repo.RootPath, filepath.FromSlash(caller.Path)))
			for _, site := range sitesInRange(lines, caller.LineStart, caller.LineEnd, oldName, to) {
				if confirmedKeys[siteKey(caller.Path, site.Line)] {
					continue
				}
				confirmedKeys[siteKey(caller.Path, site.Line)] = true
				site.Confidence = "graph-confirmed"
				site.Score = e.Confidence
				if site.Score == 0 {
					site.Score = 0.8
				}
				pl := ensure(caller.Path)
				pl.GraphConfirmed = append(pl.GraphConfirmed, site)
			}
		}

		// 3) Textual-only scan: every word-boundary occurrence of the identifier
		//    across the repo that the graph did NOT already confirm. These may be
		//    false positives (same-named field, comment, string) — flagged as such.
		textualFiles, scanWarn := scanTextualOccurrences(repo.RootPath, oldName, to, confirmedKeys)
		for _, tf := range textualFiles {
			pl := ensure(tf.Path)
			pl.TextualOnly = append(pl.TextualOnly, tf.Sites...)
		}

		out := renameSymbolResponse{
			Name:       oldName,
			To:         to,
			Definition: fmt.Sprintf("%s:%d", def.Path, def.LineStart),
		}
		// Stable, readable ordering.
		for _, pl := range plans {
			sort.Slice(pl.GraphConfirmed, func(i, j int) bool { return pl.GraphConfirmed[i].Line < pl.GraphConfirmed[j].Line })
			sort.Slice(pl.TextualOnly, func(i, j int) bool { return pl.TextualOnly[i].Line < pl.TextualOnly[j].Line })
			out.GraphConfirmedCount += len(pl.GraphConfirmed)
			out.TextualOnlyCount += len(pl.TextualOnly)
			out.Files = append(out.Files, *pl)
		}
		sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
		if scanWarn != "" {
			out.Warnings = append(out.Warnings, scanWarn)
		}

		apply := argBool(args, "apply", false)
		includeTextual := argBool(args, "include_textual", false)
		if !apply {
			out.Note = "preview only — pass apply=true to write graph-confirmed sites (add include_textual=true to also write textual-only sites). " + symbolEditHeuristicNote
			out.RecommendedNextTools = []string{"impact", "context", "rename_symbol"}
			out.Confidence = 0.7
			if out.GraphConfirmedCount > 0 {
				out.Confidence = 0.85
			}
			return mustToolResultStructured(out)
		}

		// Apply path: rewrite the exact identifier occurrences, snapshotting each
		// touched file so revert_workspace_edit can undo it. Graph-confirmed always;
		// textual-only only when include_textual=true.
		applied, tokens, applyWarns := applyRenameSites(repo.RootPath, out.Files, includeTextual)
		out.Applied = true
		out.AppliedSiteCount = applied
		out.RevertTokens = tokens
		out.Warnings = append(out.Warnings, applyWarns...)
		out.Note = fmt.Sprintf("applied %d site(s); textual-only %s. Reverse each file with revert_workspace_edit using the returned tokens, and re-index (codehelper analyze) so the graph reflects the new name. %s",
			applied, textualAppliedPhrase(includeTextual, out.TextualOnlyCount), symbolEditHeuristicNote)
		out.RecommendedNextTools = []string{"verify", "review_diff", "revert_workspace_edit"}
		out.Confidence = 0.8
		return mustToolResultStructured(out)
	}
}

func textualAppliedPhrase(included bool, n int) string {
	if n == 0 {
		return "none found"
	}
	if included {
		return fmt.Sprintf("%d textual-only site(s) were ALSO written (unverified — review the diff)", n)
	}
	return fmt.Sprintf("%d textual-only site(s) were NOT written (pass include_textual=true to write them)", n)
}

func insertAtSymbolHandler(reg *registry.Registry) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		name := strings.TrimSpace(argString(args, "name"))
		position := strings.ToLower(strings.TrimSpace(argString(args, "position")))
		text := argString(args, "text")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		if text == "" {
			return mcp.NewToolResultError("text is required"), nil
		}
		switch position {
		case "before", "after", "start_of_body", "end_of_body":
		default:
			return mcp.NewToolResultError("position must be one of: before, after, start_of_body, end_of_body"), nil
		}
		repo, err := resolveRepoInitialized(ctx, reg, argString(args, "repo"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		st, err := openGraph(repo.RootPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer st.Close()

		wantPath := strings.TrimSpace(argString(args, "path"))
		wantLine := int(argFloat(args, "line", 0))
		def, cands, err := resolveSymbolForEdit(ctx, st, repo.Name, name, wantPath, wantLine)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if def == nil {
			out := insertAtSymbolResponse{
				Name: name, Position: position, Ambiguous: true, Candidates: cands,
				Note:                 "multiple symbols share this name; pass path and/or line to disambiguate.",
				RecommendedNextTools: []string{"query", "context"},
			}
			return mustToolResultStructured(out)
		}

		abs := filepath.Join(repo.RootPath, filepath.FromSlash(def.Path))
		lines, err := readFileLines(abs)
		if err != nil {
			return mcp.NewToolResultError("cannot read definition file: " + err.Error()), nil
		}
		// insertBefore is the 1-based line index the inserted block precedes.
		insertBefore := insertionLine(position, def.LineStart, def.LineEnd, len(lines))
		if insertBefore < 1 || insertBefore > len(lines)+1 {
			return mcp.NewToolResultError("computed insertion line is out of range; the index may be stale — run codehelper analyze"), nil
		}

		block := normalizeInsertBlock(text)
		updated := spliceLines(lines, insertBefore, block)
		preview := insertionPreview(lines, insertBefore, block, 3)

		out := insertAtSymbolResponse{
			Name:         def.Name,
			Position:     position,
			Path:         def.Path,
			InsertAtLine: insertBefore,
			Preview:      preview,
		}
		apply := argBool(args, "apply", false)
		if !apply {
			out.Note = fmt.Sprintf("preview only — would insert before line %d in %s. Pass apply=true to write. %s",
				insertBefore, def.Path, symbolEditHeuristicNote)
			out.RecommendedNextTools = []string{"insert_at_symbol", "read_workspace_file"}
			out.Confidence = 0.8
			return mustToolResultStructured(out)
		}

		token, snapErr := snapshotPreEdit(repo.RootPath, def.Path, []byte(strings.Join(lines, "")))
		if snapErr != nil {
			return mcp.NewToolResultError("snapshot failed: " + snapErr.Error()), nil
		}
		if err := atomicWrite(abs, []byte(updated)); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out.Applied = true
		out.RevertToken = token
		out.Note = fmt.Sprintf("inserted before line %d in %s; revert with revert_workspace_edit using revert_token. %s",
			insertBefore, def.Path, symbolEditHeuristicNote)
		out.RecommendedNextTools = []string{"verify", "review_diff", "revert_workspace_edit"}
		out.Confidence = 0.82
		return mustToolResultStructured(out)
	}
}

// resolveSymbolForEdit resolves name to a single definition symbol. name may be a
// full sym: id, in which case it is looked up directly. Otherwise SymbolsByName
// is filtered to EXACT name matches and disambiguated by path/line. Returns
// (def, nil, nil) on a unique match, (nil, candidates, nil) when ambiguous, and
// an error only on a genuine failure (e.g. no symbol at all).
func resolveSymbolForEdit(ctx context.Context, st *graph.Store, repoID, name, wantPath string, wantLine int) (*types.Symbol, []renameCandidate, error) {
	if strings.HasPrefix(name, "sym:") {
		sym, err := st.SymbolByID(ctx, repoID, name)
		if err != nil {
			return nil, nil, err
		}
		if sym == nil {
			return nil, nil, fmt.Errorf("no symbol with id %q", name)
		}
		return sym, nil, nil
	}
	all, err := st.SymbolsByName(ctx, repoID, name, 200)
	if err != nil {
		return nil, nil, err
	}
	var exact []types.Symbol
	for _, s := range all {
		if s.Name == name {
			exact = append(exact, s)
		}
	}
	if len(exact) == 0 {
		return nil, nil, fmt.Errorf("no symbol named %q in the index (try query to find the right name, or pass a sym: id)", name)
	}
	// Disambiguate by path (suffix match) and/or exact line.
	if wantPath != "" || wantLine > 0 {
		var filtered []types.Symbol
		for _, s := range exact {
			if wantPath != "" && !pathMatches(s.Path, wantPath) {
				continue
			}
			if wantLine > 0 && s.LineStart != wantLine {
				continue
			}
			filtered = append(filtered, s)
		}
		exact = filtered
	}
	if len(exact) == 1 {
		return &exact[0], nil, nil
	}
	if len(exact) == 0 {
		return nil, nil, fmt.Errorf("no symbol named %q matched path=%q line=%d", name, wantPath, wantLine)
	}
	// Still ambiguous → return candidate list, no def.
	sort.Slice(exact, func(i, j int) bool {
		if exact[i].Path != exact[j].Path {
			return exact[i].Path < exact[j].Path
		}
		return exact[i].LineStart < exact[j].LineStart
	})
	cands := make([]renameCandidate, 0, len(exact))
	for _, s := range exact {
		cands = append(cands, renameCandidate{
			ID:   s.ID,
			Name: s.Name,
			Kind: string(s.Kind),
			Loc:  fmt.Sprintf("%s:%d", s.Path, s.LineStart),
			Recv: s.ParentID,
		})
	}
	return nil, cands, nil
}

// pathMatches reports whether symPath equals or ends with the user-supplied path
// hint (so "store.go" or "internal/graph/store.go" both work).
func pathMatches(symPath, want string) bool {
	symPath = filepath.ToSlash(symPath)
	want = filepath.ToSlash(strings.TrimPrefix(want, "./"))
	return symPath == want || strings.HasSuffix(symPath, "/"+want)
}

func siteKey(path string, line int) string { return fmt.Sprintf("%s:%d", path, line) }

// siteOnLine builds a rename site for the first occurrence of oldName on the
// given 1-based line, if any.
func siteOnLine(lines []string, line int, oldName, newName string) (renameSite, bool) {
	if line < 1 || line > len(lines) {
		return renameSite{}, false
	}
	raw := strings.TrimRight(lines[line-1], "\n")
	col, ok := firstIdentCol(raw, oldName, 0)
	if !ok {
		return renameSite{}, false
	}
	return renameSite{
		Line:   line,
		Col:    col + 1,
		Before: raw,
		After:  replaceIdentOnce(raw, oldName, newName, col),
	}, true
}

// sitesInRange returns one site per line in [start,end] that contains a
// word-boundary occurrence of oldName (first occurrence per line).
func sitesInRange(lines []string, start, end int, oldName, newName string) []renameSite {
	var out []renameSite
	if start < 1 {
		start = 1
	}
	if end > len(lines) || end == 0 {
		end = len(lines)
	}
	for ln := start; ln <= end; ln++ {
		raw := strings.TrimRight(lines[ln-1], "\n")
		col, ok := firstIdentCol(raw, oldName, 0)
		if !ok {
			continue
		}
		out = append(out, renameSite{
			Line:   ln,
			Col:    col + 1,
			Before: raw,
			After:  replaceIdentOnce(raw, oldName, newName, col),
		})
	}
	return out
}

type textualFile struct {
	Path  string
	Sites []renameSite
}

// scanTextualOccurrences walks tracked source files under root looking for
// word-boundary matches of oldName that are NOT already claimed by the graph
// (confirmed map keyed by "path:line"). It is intentionally simple: it skips
// obvious non-source/vendored directories and binary files. Returns any scan
// error as a warning string rather than failing the whole tool.
func scanTextualOccurrences(root, oldName, newName string, confirmed map[string]bool) ([]textualFile, string) {
	byPath := map[string]*textualFile{}
	var walkErr error
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if skipScanDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !looksLikeSource(d.Name()) {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		lines, lerr := readFileLines(p)
		if lerr != nil {
			return nil
		}
		for i, raw := range lines {
			ln := i + 1
			if confirmed[siteKey(rel, ln)] {
				continue
			}
			text := strings.TrimRight(raw, "\n")
			col, ok := firstIdentCol(text, oldName, 0)
			if !ok {
				continue
			}
			tf := byPath[rel]
			if tf == nil {
				tf = &textualFile{Path: rel}
				byPath[rel] = tf
			}
			tf.Sites = append(tf.Sites, renameSite{
				Line:       ln,
				Col:        col + 1,
				Before:     text,
				After:      replaceIdentOnce(text, oldName, newName, col),
				Confidence: "textual-only",
				Score:      0.4,
			})
		}
		return nil
	})
	out := make([]textualFile, 0, len(byPath))
	for _, tf := range byPath {
		out = append(out, *tf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	warn := ""
	if walkErr != nil {
		warn = "textual scan incomplete: " + walkErr.Error()
	}
	return out, warn
}

// applyRenameSites rewrites the planned occurrences file-by-file. For each file
// it snapshots the pre-edit content (so revert_workspace_edit works) and applies
// graph-confirmed sites, plus textual-only sites when includeTextual is true.
// It re-reads each file and replaces the exact identifier at the recorded column
// to stay robust if line content shifted slightly. Returns the total sites
// written, the revert tokens, and any per-file warnings.
func applyRenameSites(root string, files []renameFilePlan, includeTextual bool) (int, []string, []string) {
	applied := 0
	var tokens []string
	var warns []string
	for _, pl := range files {
		sites := append([]renameSite{}, pl.GraphConfirmed...)
		if includeTextual {
			sites = append(sites, pl.TextualOnly...)
		}
		if len(sites) == 0 {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(pl.Path))
		orig, err := os.ReadFile(abs)
		if err != nil {
			warns = append(warns, pl.Path+": read failed, skipped ("+err.Error()+")")
			continue
		}
		lines := splitLinesKeepNL(string(orig))
		fileApplied := 0
		// Apply highest line first so earlier edits don't shift later indices
		// (column-based replace within a single line is unaffected by ordering,
		// but keeping it deterministic is cleaner).
		sort.Slice(sites, func(i, j int) bool { return sites[i].Line > sites[j].Line })
		for _, s := range sites {
			if s.Line < 1 || s.Line > len(lines) {
				continue
			}
			raw := strings.TrimRight(lines[s.Line-1], "\n")
			suffix := lineEnding(lines[s.Line-1])
			if raw == s.Before && s.After != "" {
				lines[s.Line-1] = s.After + suffix
				fileApplied++
			}
		}
		if fileApplied == 0 {
			warns = append(warns, pl.Path+": no sites applied (file changed since preview); re-run rename_symbol")
			continue
		}
		token, snapErr := snapshotPreEdit(root, pl.Path, orig)
		if snapErr != nil {
			warns = append(warns, pl.Path+": snapshot failed, NOT written ("+snapErr.Error()+")")
			continue
		}
		if err := atomicWrite(abs, []byte(strings.Join(lines, ""))); err != nil {
			warns = append(warns, pl.Path+": write failed ("+err.Error()+")")
			continue
		}
		tokens = append(tokens, token)
		applied += fileApplied
	}
	return applied, tokens, warns
}

// ---- text helpers ----

// firstIdentCol returns the 0-based rune column of the first word-boundary
// occurrence of ident in line at or after fromByte, or ok=false. Word boundary:
// the char before/after is not an identifier char.
func firstIdentCol(line, ident string, fromByte int) (int, bool) {
	if ident == "" {
		return 0, false
	}
	for i := fromByte; i+len(ident) <= len(line); i++ {
		if line[i:i+len(ident)] != ident {
			continue
		}
		if i > 0 && isIdentByte(line[i-1]) {
			continue
		}
		if i+len(ident) < len(line) && isIdentByte(line[i+len(ident)]) {
			continue
		}
		// Convert byte offset to rune column.
		return len([]rune(line[:i])), true
	}
	return 0, false
}

// replaceIdentOnce replaces the word-boundary identifier occurrence at runeCol
// with newName, returning the rewritten line. Falls back to the original line if
// the column can't be re-located (defensive).
func replaceIdentOnce(line, oldName, newName string, runeCol int) string {
	runes := []rune(line)
	if runeCol < 0 || runeCol+len([]rune(oldName)) > len(runes) {
		return line
	}
	if string(runes[runeCol:runeCol+len([]rune(oldName))]) != oldName {
		return line
	}
	return string(runes[:runeCol]) + newName + string(runes[runeCol+len([]rune(oldName)):])
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if i == 0 && b >= '0' && b <= '9' {
			return false
		}
		if !isIdentByte(b) {
			return false
		}
	}
	return true
}

// readFileLines returns the file split into lines WITH their trailing newlines
// preserved, so joining reproduces the file byte-for-byte.
func readFileLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return splitLinesKeepNL(string(b)), nil
}

func splitLinesKeepNL(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func lineEnding(line string) string {
	if strings.HasSuffix(line, "\n") {
		return "\n"
	}
	return ""
}

// insertionLine maps a position keyword to the 1-based line the block precedes.
//   - before        → the definition's start line
//   - after         → the line after the definition's end
//   - start_of_body → the line after the start line (first body line)
//   - end_of_body   → the definition's end line (insert before the closing line)
func insertionLine(position string, lineStart, lineEnd, total int) int {
	if lineEnd < lineStart {
		lineEnd = lineStart
	}
	switch position {
	case "before":
		return lineStart
	case "after":
		return lineEnd + 1
	case "start_of_body":
		return lineStart + 1
	case "end_of_body":
		return lineEnd
	}
	return lineStart
}

// normalizeInsertBlock ensures the inserted text is whole lines ending in \n.
func normalizeInsertBlock(text string) string {
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

// spliceLines inserts block (already newline-terminated) before the 1-based
// insertBefore line and returns the reassembled file content.
func spliceLines(lines []string, insertBefore int, block string) string {
	if insertBefore < 1 {
		insertBefore = 1
	}
	if insertBefore > len(lines)+1 {
		insertBefore = len(lines) + 1
	}
	var sb strings.Builder
	for i := 0; i < insertBefore-1 && i < len(lines); i++ {
		sb.WriteString(lines[i])
	}
	// If inserting at EOF and the last existing line lacks a trailing newline,
	// add one so the new block starts on its own line.
	if insertBefore == len(lines)+1 && len(lines) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString(block)
	for i := insertBefore - 1; i < len(lines); i++ {
		sb.WriteString(lines[i])
	}
	return sb.String()
}

// insertionPreview renders the inserted block with up to ctx lines of context on
// each side, marking inserted lines with '+'.
func insertionPreview(lines []string, insertBefore int, block string, ctx int) string {
	var sb strings.Builder
	from := insertBefore - 1 - ctx
	if from < 0 {
		from = 0
	}
	for i := from; i < insertBefore-1 && i < len(lines); i++ {
		fmt.Fprintf(&sb, "  %d: %s\n", i+1, strings.TrimRight(lines[i], "\n"))
	}
	for _, bl := range splitLinesKeepNL(block) {
		fmt.Fprintf(&sb, "+ %s\n", strings.TrimRight(bl, "\n"))
	}
	to := insertBefore - 1 + ctx
	if to > len(lines) {
		to = len(lines)
	}
	for i := insertBefore - 1; i < to; i++ {
		fmt.Fprintf(&sb, "  %d: %s\n", i+1, strings.TrimRight(lines[i], "\n"))
	}
	return sb.String()
}

// skipScanDir skips vendored / VCS / build directories during the textual scan.
func skipScanDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build",
		".codehelper", "target", "__pycache__", ".venv", "venv", ".idea", ".vscode":
		return true
	}
	return false
}

// looksLikeSource keeps common text/source extensions for the textual scan and
// drops obvious binaries.
func looksLikeSource(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rb", ".rs", ".java",
		".kt", ".c", ".h", ".cc", ".cpp", ".hpp", ".cs", ".php", ".swift",
		".scala", ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".sql",
		".sh", ".proto", ".graphql", ".vue", ".svelte":
		return true
	}
	return false
}
