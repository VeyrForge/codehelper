package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VeyrForge/codehelper/internal/agents"
	"github.com/VeyrForge/codehelper/internal/cache"
	"github.com/VeyrForge/codehelper/internal/filehash"
	"github.com/VeyrForge/codehelper/internal/freshness"
	"github.com/VeyrForge/codehelper/internal/gitutil"
	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/hubs"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/parser"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/profile"
	"github.com/VeyrForge/codehelper/internal/setup"
	"github.com/VeyrForge/codehelper/internal/vocab"
	"github.com/VeyrForge/codehelper/pkg/types"
)

// Options controls indexing.
type Options struct {
	Force        bool
	RepoName     string
	ShardName    string
	IndexSubdir  string
	Invalidation InvalidationMode
	// ProgressJSON, when set, receives one JSON progress object per line (see WriteProgressJSON).
	ProgressJSON io.Writer
}

// Run indexes repository under indexRoot (see cmd wiring: git root + optional subdir).
func Run(ctx context.Context, pathArg string, opt Options) error {
	gitRoot, indexRoot, err := ResolveIndexPaths(pathArg, opt.IndexSubdir)
	if err != nil {
		return err
	}
	if _, err := gitutil.EnsureCodehelperGitignored(gitRoot); err != nil {
		slog.Warn("ensure .codehelper gitignore", "root", gitRoot, "err", err)
	}
	// Write the per-client tool-first rules up front — BEFORE the up-to-date early
	// return — so every client learns to call the MCP tools even when the index is
	// unchanged (the common case for a running watch daemon). AGENTS.md (Codex) now
	// holds the SINGLE full tool contract that CLAUDE.md and .cursor/rules only
	// point at, so it must be (re)written here too; previously it was written only
	// in the full reindex path, so a deleted AGENTS.md never came back on a fresh
	// index and the pointer-style rules dangled.
	if err := setup.WriteClientRules(indexRoot); err != nil {
		slog.Warn("client rules", "err", err)
	}
	if err := agents.Write(indexRoot); err != nil {
		slog.Warn("agents rules", "err", err)
	}

	repoName := strings.TrimSpace(opt.RepoName)
	if repoName == "" {
		repoName = strings.TrimSpace(opt.ShardName)
	}
	if repoName == "" {
		repoName = filepath.Base(indexRoot)
	}
	repoID := repoName

	curCommit, err := gitutil.HeadCommit(gitRoot)
	if err != nil {
		return err
	}
	prev, _ := meta.Read(indexRoot)

	versionMismatch := prev != nil && (prev.SchemaVersion != meta.SchemaVersion || prev.ParserVersion != parser.Version)
	skipWork := !opt.Force && prev != nil && prev.LastCommit == curCommit && !versionMismatch
	// Plain `analyze` is otherwise git-commit gated and would no-op on UNCOMMITTED
	// edits (HEAD unchanged). Reindex if any source file changed since the last
	// index so the working tree — not just the last commit — is reflected.
	if skipWork && freshness.WorkingTreeChangedSince(indexRoot, prev.IndexedAt) {
		skipWork = false
	}
	if skipWork && prev != nil && prev.SymbolCount > 0 {
		dbSyms := graphSymbolCount(indexRoot, repoID)
		if dbSyms == 0 || dbSyms < prev.SymbolCount/2 {
			slog.Warn("graph symbol count diverges from meta; re-indexing", "meta", prev.SymbolCount, "graph", dbSyms, "root", indexRoot)
			skipWork = false
		}
	}
	if skipWork {
		// Even when the index matches HEAD, derived artifacts may be missing — e.g.
		// after a binary upgrade that introduced a new one (vocab.json,
		// project_profile.json). Backfill them cheaply from the existing graph so an
		// upgrade self-applies to every project on its next watch/tool access,
		// without forcing a full reparse. This is what makes "updates work for all".
		backfillDerivedArtifacts(ctx, indexRoot, repoID)
		WriteProgressJSON(opt.ProgressJSON, "up_to_date", 0, 0, 0, 0, "", "index matches HEAD")
		slog.Info("index already up to date", "commit", curCommit, "root", indexRoot)
		return nil
	}

	WriteProgressJSON(opt.ProgressJSON, "start", 0, 0, 0, 0, "", "scanning sources")

	var giSkip func(string) bool
	giMatcher, gerrGi := LoadLayeredGitIgnoreMatcher(gitRoot)
	if gerrGi != nil {
		slog.Warn("gitignore", "err", gerrGi)
	} else if giMatcher != nil {
		giSkip = GitIgnoreSkipFunc(gitRoot, indexRoot, giMatcher)
	}
	// Prune gitignored directories during the walk so vendored fixture trees
	// (e.g. .testbeds/) are never descended; the per-file filter below still
	// catches gitignored individual files (e.g. *.map).
	var dirSkip func(string) bool
	if giSkip != nil {
		dirSkip = func(rel string) bool { return giSkip(rel) || giSkip(rel+"/") }
	}
	allRaw, err := WalkSourceFiles(indexRoot, dirSkip, nil)
	if err != nil {
		return err
	}
	var allFiles []string
	var skippedGit int
	for _, f := range allRaw {
		if giSkip != nil && giSkip(f) {
			continue
		}
		allFiles = append(allFiles, f)
	}
	skippedGit = len(allRaw) - len(allFiles)
	WriteProgressJSON(opt.ProgressJSON, "walk_done", len(allFiles), len(allFiles), skippedGit, len(allFiles), "",
		fmt.Sprintf("%d eligible source files (%d skipped by .gitignore)", len(allFiles), skippedGit))

	dbPath := paths.DBPath(indexRoot)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	st, err := graph.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	fc, err := cache.OpenFileCache(paths.CachePath(indexRoot))
	if err != nil {
		return err
	}
	defer fc.Close()

	var toParse []string
	full := opt.Force || prev == nil || prev.LastCommit == "" || gitutil.IsUnbornCommit(prev.LastCommit) || versionMismatch
	if !full && prev != nil {
		changed, derr := gitDiffNameOnly(gitRoot, prev.LastCommit)
		if derr != nil || len(changed) == 0 {
			full = true
		} else {
			chSet := filterChangedForShard(gitRoot, indexRoot, changed)
			if _, all := chSet["."]; all {
				full = true
			} else {
				for _, f := range allFiles {
					if _, ok := chSet[f]; ok {
						toParse = append(toParse, f)
					}
				}
				if len(toParse) == 0 {
					full = true
				}
				if !full && float64(len(toParse)) >= float64(len(allFiles))*fullReindexRatio {
					full = true
					toParse = nil
				}
			}
		}
	}
	if !full && len(toParse) > 0 {
		mode := opt.Invalidation
		if mode == "" {
			mode = InvalidationEager
		}
		expanded, ierr := ExpandInvalidationPaths(ctx, st, repoID, toParse, mode, 0)
		if ierr != nil {
			return ierr
		}
		uniq := map[string]struct{}{}
		for _, p := range toParse {
			uniq[p] = struct{}{}
		}
		for _, p := range expanded {
			uniq[p] = struct{}{}
		}
		toParse = orderPathsStable(uniq, allFiles)
	}

	if full {
		if err := st.ClearRepo(ctx, repoID); err != nil {
			return err
		}
		toParse = allFiles
	}

	var toReparse []string
	if full {
		toReparse = toParse
	} else {
		for _, p := range toParse {
			abs := filepath.Join(indexRoot, p)
			h, herr := hashFile(abs)
			if herr != nil {
				toReparse = append(toReparse, p)
				continue
			}
			prevH, _ := fc.GetHash(p)
			if prevH != "" && prevH == h {
				continue
			}
			toReparse = append(toReparse, p)
		}
		if len(toReparse) == 0 {
			m := prev
			if m == nil {
				m = &meta.Data{RepoName: repoName, RootPath: indexRoot}
			}
			m.LastCommit = curCommit
			m.ParserVersion = parser.Version
			m.SchemaVersion = meta.SchemaVersion
			if err := refreshMetaCounts(ctx, st, indexRoot, repoID, m); err != nil {
				return err
			}
			slog.Info("incremental skip: file hashes unchanged", "root", indexRoot)
			WriteProgressJSON(opt.ProgressJSON, "done", 0, 0, skippedGit, len(allFiles), "",
				"incremental skip: workspace unchanged")
			return nil
		}
		// Preserve cross-file callers across the edit: revert resolved edges that
		// point INTO the files we're about to re-parse back to symref placeholders,
		// so the post-index ResolveSymrefs re-binds them to the new symbol IDs.
		// Without this, re-parsing (which renumbers symbol IDs by line) orphans
		// caller edges from unchanged files and the call graph silently degrades.
		if err := st.RevertEdgesIntoPaths(ctx, repoID, toReparse); err != nil {
			return err
		}
		for _, p := range toReparse {
			if err := RemoveSymbolsForPaths(ctx, st, repoID, []string{p}); err != nil {
				return err
			}
		}
	}

	symCount := 0
	edgeCount := 0

	nParse := len(toReparse)
	WriteProgressJSON(opt.ProgressJSON, "parse_start", 0, nParse, skippedGit, len(allFiles), "", "parsing")
	tParse := time.Now()
	if err := parseAndIngest(ctx, st, fc, repoID, indexRoot, toReparse, &symCount, &edgeCount, func(done int, rel string) {
		if done == 1 || done == nParse || done%10 == 0 {
			WriteProgressJSON(opt.ProgressJSON, "parse", done, nParse, skippedGit, len(allFiles), rel, "")
		}
		if done%50 == 1 {
			slog.Info("indexing", "file", rel, "progress", fmt.Sprintf("%d/%d", done, nParse))
		}
	}); err != nil {
		return err
	}
	slog.Info("parse+ingest done", "files", nParse, "symbols", symCount, "edges", edgeCount, "dur", time.Since(tParse).Round(time.Millisecond))

	// Resolve cross-file / forward-reference call & read edges that the per-file
	// parser left as symref placeholders. This is what makes caller (context)
	// and blast-radius (impact) queries accurate.
	tPost := time.Now()
	if symStats, err := st.ResolveSymrefs(ctx, repoID); err != nil {
		slog.Warn("symref resolution", "err", err)
	} else if symStats.Total > 0 {
		slog.Info("symref resolution", "resolved", symStats.Resolved,
			"ambiguous", symStats.Ambiguous, "unresolved", symStats.Unresolved, "total", symStats.Total,
			"dur", time.Since(tPost).Round(time.Millisecond))
	}

	tProc := time.Now()
	if err := buildProcesses(ctx, st, repoID, indexRoot, toReparse); err != nil {
		slog.Warn("process detection", "err", err)
	}
	if err := buildClusters(ctx, st, repoID, toReparse); err != nil {
		slog.Warn("clustering", "err", err)
	}
	slog.Info("post-processing done", "dur", time.Since(tProc).Round(time.Millisecond))

	// Rebuild the trigram full-text index so candidate generation uses an indexed
	// substring search instead of full-table LIKE scans (the 100k+ symbol scale
	// fix). Best-effort: a failure just leaves retrieval on the LIKE fallback.
	tFTS := time.Now()
	if err := st.RebuildSymbolFTS(ctx, repoID); err != nil {
		slog.Warn("fts rebuild", "err", err)
	} else {
		slog.Info("fts index rebuilt", "dur", time.Since(tFTS).Round(time.Millisecond))
	}

	m := &meta.Data{
		RepoName:      repoName,
		RootPath:      indexRoot,
		LastCommit:    curCommit,
		ParserVersion: parser.Version,
	}
	if err := refreshMetaCounts(ctx, st, indexRoot, repoID, m); err != nil {
		return err
	}
	totalSyms, totalEdges, totalFiles, err := st.Counts(ctx, repoID)
	if err != nil {
		return err
	}
	// AGENTS.md is written up front (before the up-to-date early return), so it is
	// not rewritten here.
	if err := writeArchitectureSummary(ctx, st, repoID, indexRoot); err != nil {
		slog.Warn("architecture summary", "err", err)
	}
	// Build the project vocabulary seed from the full symbol set: the most frequent
	// identifiers and their sub-words across every language, the raw material a
	// later review step promotes into the shared glossary. Best-effort.
	if syms, verr := st.AllSymbolNames(ctx, repoID); verr != nil {
		slog.Warn("vocabulary scan", "err", verr)
	} else if verr := vocab.Write(indexRoot, repoID, syms); verr != nil {
		slog.Warn("vocabulary seed", "err", verr)
	}
	// Write the project profile (project_type, languages, verify commands) at index
	// time. Previously this was produced only by the agent loop, so projects that
	// were merely indexed had no profile and project_context reported
	// project_type:"unknown". Best-effort. See profile.Generate for detection.
	if _, perr := profile.Write(indexRoot); perr != nil {
		slog.Warn("project profile", "err", perr)
	}
	// Precompute the call-graph "what's linked" summary (symbol + package hubs) so
	// project_context reads it instantly instead of scanning the graph per request.
	// Best-effort — the graph is fully built and symref-resolved by here.
	if herr := hubs.Write(ctx, indexRoot, repoID, st); herr != nil {
		slog.Warn("hubs summary", "err", herr)
	}
	// Refresh SQLite statistics so structural queries (callers/callees/impact) use
	// the selective edge index instead of scanning every call edge — ~19× faster,
	// sub-ms, and the difference between usable and unusable on large monorepos.
	if aerr := st.Analyze(ctx); aerr != nil {
		slog.Warn("analyze stats", "err", aerr)
	}
	WriteProgressJSON(opt.ProgressJSON, "done", nParse, nParse, skippedGit, len(allFiles), "",
		fmt.Sprintf("%d symbols, %d edges, %d files in graph", totalSyms, totalEdges, totalFiles))
	slog.Info("index complete", "symbols", symCount, "edges", edgeCount, "totals", fmt.Sprintf("%d syms %d edges %d files", totalSyms, totalEdges, totalFiles))
	return nil
}

// backfillDerivedArtifacts regenerates the cheap, graph-derived index artifacts
// (project profile, vocabulary seed) when they are missing — the case after a
// binary upgrade adds a new artifact but the index already matches HEAD. It does
// no reparsing: the profile is a bounded file scan and the vocab seed is one pass
// over the existing symbols table. Best-effort.
func backfillDerivedArtifacts(ctx context.Context, indexRoot, repoID string) {
	if _, err := os.Stat(profile.Path(indexRoot)); os.IsNotExist(err) {
		if _, werr := profile.Write(indexRoot); werr != nil {
			slog.Warn("profile backfill", "err", werr)
		}
	}
	_, vocabErr := os.Stat(paths.VocabPath(indexRoot))
	_, hubsErr := os.Stat(paths.HubsPath(indexRoot))
	needVocab, needHubs := os.IsNotExist(vocabErr), os.IsNotExist(hubsErr)
	// Open the graph only when a graph-derived artifact is missing — true exactly
	// once per project after an upgrade, so up-to-date watch ticks stay cheap. The
	// same open also backfills query statistics (ANALYZE) for indexes built before
	// it ran at index time, so their structural queries get the fast index too.
	if !needVocab && !needHubs {
		return
	}
	st, gerr := graph.Open(paths.DBPath(indexRoot))
	if gerr != nil {
		slog.Warn("backfill: open graph", "err", gerr)
		return
	}
	defer st.Close()
	if needVocab {
		if syms, serr := st.AllSymbolNames(ctx, repoID); serr == nil {
			if werr := vocab.Write(indexRoot, repoID, syms); werr != nil {
				slog.Warn("vocab backfill", "err", werr)
			}
		}
	}
	if needHubs {
		if werr := hubs.Write(ctx, indexRoot, repoID, st); werr != nil {
			slog.Warn("hubs backfill", "err", werr)
		}
	}
	if !st.HasStats(ctx) {
		if aerr := st.Analyze(ctx); aerr != nil {
			slog.Warn("analyze backfill", "err", aerr)
		}
	}
}

// maxParseWorkers bounds tree-sitter parsing concurrency so codehelper is a good
// citizen on a shared machine. Using GOMAXPROCS (all cores) made a single analyze
// — or a watch-daemon reindex — spike to ~all-core CPU; with several IDE windows
// each running their own daemon, that saturated the box and caused lag/crashes.
// Default: a quarter of the cores, clamped to [1,8], leaving most cores for the
// editor. Override with CODEHELPER_MAX_WORKERS (e.g. =2 on a low-end laptop, or a
// high number on a dedicated CI box).
func maxParseWorkers() int {
	if v := strings.TrimSpace(os.Getenv("CODEHELPER_MAX_WORKERS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	n := runtime.GOMAXPROCS(0) / 4
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	return n
}

// parseOutput is one file parsed by a worker, handed to the serial batched writer.
type parseOutput struct {
	ingest graph.FileIngest
	rel    string
	hash   string
	hashOK bool
}

// parseAndIngest parses files concurrently across GOMAXPROCS workers (tree-sitter
// parsing is CPU-bound and each file is independent) and persists them through a
// SINGLE writer goroutine that batches rows into transactions — the cold-build
// pattern from Codebase-Memory (arXiv:2603.27277): parallel parse → serial
// batched flush. SQLite has one writer, so every st.* call stays on this
// goroutine; workers only touch the filesystem and the per-call (lock-free)
// tree-sitter parser. On 100k+ file repos this turns a serial, per-row-autocommit
// loop into a parallel parse + transaction-batched write.
func parseAndIngest(ctx context.Context, st *graph.Store, fc *cache.FileCache, repoID, indexRoot string, toReparse []string, symCount, edgeCount *int, progress func(done int, rel string)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	nWorkers := maxParseWorkers()
	jobs := make(chan string, nWorkers*2)
	results := make(chan parseOutput, nWorkers*2)

	go func() {
		defer close(jobs)
		for _, rel := range toReparse {
			select {
			case jobs <- rel:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Nice this worker's OS thread so CPU-bound parsing yields to the
			// editor and interactive tool calls — keeps the box responsive even
			// when several daemons reindex at once (see lowerParseThreadPriority).
			lowerParseThreadPriority()
			for rel := range jobs {
				abs := filepath.Join(indexRoot, rel)
				buf, rerr := os.ReadFile(abs)
				if rerr != nil {
					continue
				}
				// Hash and size come from the buffer we just read — no second
				// open+read (hashFile) or stat per file, which halves the file
				// I/O of a cold build on large repos.
				h := filehash.OfBytes(buf)
				fmeta := types.FileMeta{
					ID:       parser.FileNodeID(repoID, rel),
					RepoID:   repoID,
					Path:     rel,
					Language: languageFromExt(rel),
					Size:     int64(len(buf)),
					Hash:     h,
				}
				res, perr := parser.Extract(ctx, repoID, rel, buf)
				if perr != nil {
					slog.Warn("parse failed", "path", rel, "err", perr)
					continue
				}
				for i := range res.Symbols {
					res.Symbols[i].RepoID = repoID
				}
				for i := range res.Edges {
					res.Edges[i].RepoID = repoID
				}
				select {
				case results <- parseOutput{
					ingest: graph.FileIngest{File: fmeta, Symbols: res.Symbols, Edges: res.Edges},
					rel:    rel, hash: h, hashOK: true,
				}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()

	// Single writer: accumulate a bounded batch, flush as one transaction.
	const batchSize = 400
	batch := make([]graph.FileIngest, 0, batchSize)
	pending := make([]parseOutput, 0, batchSize) // rel+hash to record after commit
	var writeErr error
	flush := func() {
		if len(batch) == 0 {
			return
		}
		sc, ec, err := st.IngestFiles(ctx, batch)
		if err != nil {
			writeErr = err
			cancel()
			return
		}
		*symCount += sc
		*edgeCount += ec
		for _, p := range pending {
			if p.hashOK {
				_ = fc.SetHash(p.rel, p.hash)
			}
		}
		batch = batch[:0]
		pending = pending[:0]
	}

	done := 0
	for out := range results {
		done++
		progress(done, out.rel)
		if writeErr != nil {
			continue // drain remaining results so workers never block on send
		}
		batch = append(batch, out.ingest)
		pending = append(pending, out)
		if len(batch) >= batchSize {
			flush()
		}
	}
	if writeErr == nil {
		flush()
	}
	return writeErr
}

func filterChangedForShard(gitRoot, indexRoot string, changed []string) map[string]struct{} {
	out := map[string]struct{}{}
	prefix := ""
	if filepath.Clean(indexRoot) != filepath.Clean(gitRoot) {
		var err error
		prefix, err = filepath.Rel(gitRoot, indexRoot)
		if err != nil {
			return out
		}
		prefix = filepath.ToSlash(prefix)
	}
	for _, c := range changed {
		c = filepath.ToSlash(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if prefix == "" {
			out[c] = struct{}{}
			continue
		}
		if !strings.HasPrefix(c, prefix) {
			continue
		}
		rest := strings.TrimPrefix(c, prefix)
		rest = strings.TrimPrefix(rest, "/")
		if rest == "" {
			out["."] = struct{}{}
			continue
		}
		out[rest] = struct{}{}
	}
	return out
}

func gitDiffNameOnly(root, oldCommit string) ([]string, error) {
	if oldCommit == "" || gitutil.IsUnbornCommit(oldCommit) {
		return nil, nil
	}
	return gitutil.DiffAgainst(root, oldCommit)
}

func hashFile(p string) (string, error) {
	return filehash.OfFile(p)
}

func orderPathsStable(set map[string]struct{}, allFiles []string) []string {
	var out []string
	for _, f := range allFiles {
		if _, ok := set[f]; ok {
			out = append(out, f)
		}
	}
	return out
}

func languageFromExt(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".cs":
		return "csharp"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx":
		return "cpp"
	case ".php":
		return "php"
	case ".rb":
		return "ruby"
	case ".kt", ".kts":
		return "kotlin"
	case ".swift":
		return "swift"
	case ".scala", ".sc":
		return "scala"
	case ".sh", ".bash":
		return "bash"
	case ".lua":
		return "lua"
	case ".gd":
		return "gdscript"
	case ".shader":
		return "shaderlab"
	case ".gdshader", ".gdshaderinc":
		return "gdshader"
	case ".hlsl", ".hlsli", ".fx", ".fxh", ".cginc", ".compute", ".usf", ".ush":
		return "hlsl"
	case ".glsl", ".vert", ".frag", ".geom", ".comp", ".tesc", ".tese", ".rgen", ".rchit", ".rmiss", ".rahit", ".rint", ".rcall":
		return "glsl"
	case ".metal":
		return "metal"
	case ".wgsl":
		return "wgsl"
	case ".ex", ".exs":
		return "elixir"
	case ".tf", ".tfvars", ".hcl":
		return "hcl"
	case ".proto":
		return "protobuf"
	case ".sql":
		return "sql"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".dart":
		return "dart"
	case ".vue":
		return "vue"
	case ".svelte":
		return "svelte"
	case ".astro":
		return "astro"
	case ".mdx":
		return "mdx"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return "typescript"
	default:
		return "unknown"
	}
}

func buildProcesses(ctx context.Context, st *graph.Store, repoID, root string, files []string) error {
	for _, rel := range files {
		syms, err := st.SymbolsForPath(ctx, repoID, rel)
		if err != nil || len(syms) == 0 {
			continue
		}
		var steps []string
		for _, s := range syms {
			if s.Kind == types.SymbolKindFunction || s.Kind == types.SymbolKindMethod {
				steps = append(steps, s.ID)
			}
		}
		if len(steps) == 0 {
			continue
		}
		sum := sha256.Sum256([]byte(rel))
		pid := hex.EncodeToString(sum[:8])
		p := types.Process{
			ID:          fmt.Sprintf("proc:%s:%s:%s", repoID, pid, rel),
			RepoID:      repoID,
			Name:        fmt.Sprintf("flow:%s", rel),
			EntrySymbol: steps[0],
			StepSymbols: steps,
		}
		if err := st.UpsertProcess(ctx, p); err != nil {
			return err
		}
		_ = root
	}
	return nil
}

func buildClusters(ctx context.Context, st *graph.Store, repoID string, files []string) error {
	byDir := map[string][]string{}
	for _, rel := range files {
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = "/"
		}
		syms, err := st.SymbolsForPath(ctx, repoID, rel)
		if err != nil {
			return err
		}
		for _, s := range syms {
			byDir[dir] = append(byDir[dir], s.ID)
		}
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	// Remove stale cluster rows for touched directories before upserting fresh rows.
	if err := st.DeleteClustersByNames(ctx, repoID, dirs); err != nil {
		return err
	}

	for _, dir := range dirs {
		mem := byDir[dir]
		if len(mem) < 2 {
			continue
		}
		sum := sha256.Sum256([]byte(dir))
		id := hex.EncodeToString(sum[:8])
		c := types.Cluster{
			ID:       fmt.Sprintf("clu:%s:%s", repoID, id),
			RepoID:   repoID,
			Name:     dir,
			Members:  dedupeStrings(mem),
			Cohesion: 0.5,
		}
		if err := st.UpsertCluster(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

type architectureSummary struct {
	RepoID      string          `json:"repo_id"`
	GeneratedAt string          `json:"generated_at"`
	Processes   []types.Process `json:"processes"`
	Clusters    []types.Cluster `json:"clusters"`
}

func writeArchitectureSummary(ctx context.Context, st *graph.Store, repoID, root string) error {
	procs, err := st.ListProcesses(ctx, repoID)
	if err != nil {
		return err
	}
	clusters, err := st.ListClusters(ctx, repoID)
	if err != nil {
		return err
	}
	out := architectureSummary{
		RepoID:      repoID,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Processes:   procs,
		Clusters:    clusters,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(paths.RepoIndexDir(root), "architecture_summary.json")
	return os.WriteFile(p, b, 0o644)
}
