package graph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// TestScaleStructuralQueries is a large-graph scalability harness: it builds a
// synthetic ~100k-symbol / ~300k-edge repo and measures CallersOf latency
// (percentiles) WITHOUT and WITH ANALYZE, proving the v2.46.0 fix holds at scale —
// the index seek stays sub-ms while a scan grows with the whole edge table.
//
// Gated (it builds a large DB) — run with:
//
//	CODEHELPER_SCALE_BENCH=1 go test ./internal/graph -run TestScaleStructuralQueries -v
func TestScaleStructuralQueries(t *testing.T) {
	if os.Getenv("CODEHELPER_SCALE_BENCH") == "" {
		t.Skip("set CODEHELPER_SCALE_BENCH=1 to run the large-graph scalability benchmark")
	}
	const (
		files       = 2000 // packages
		perFile     = 50   // symbols/package  -> 100k symbols
		fanout      = 3    // calls/symbol     -> 300k edges
		iterations  = 300
		latencyGate = 3 * time.Millisecond // with stats, even at scale
	)
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "scale.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"

	symID := func(f, s int) string {
		return fmt.Sprintf("sym:r:pkg%d/file.go:%d:Fn%d_%d", f, s+1, f, s)
	}
	tBuild := time.Now()
	var batch []FileIngest
	edgeCount := 0
	for f := 0; f < files; f++ {
		path := fmt.Sprintf("pkg%d/file.go", f)
		fi := FileIngest{File: types.FileMeta{ID: "file:r:" + path, RepoID: repo, Path: path, Language: "go"}}
		for s := 0; s < perFile; s++ {
			fi.Symbols = append(fi.Symbols, types.Symbol{ID: symID(f, s), RepoID: repo, Name: fmt.Sprintf("Fn%d_%d", f, s), Kind: types.SymbolKindFunction, Path: path, LineStart: s + 1})
		}
		// Each symbol calls the "main" (symbol 0) of `fanout` OTHER packages, so
		// every main ends up with a moderate, realistic caller count.
		for s := 0; s < perFile; s++ {
			src := symID(f, s)
			for k := 1; k <= fanout; k++ {
				dst := symID((f+k)%files, 0)
				fi.Edges = append(fi.Edges, types.Reference{ID: fmt.Sprintf("e:%d:%d:%d", f, s, k), RepoID: repo, Kind: types.RefKindCalls, SourceID: src, TargetID: dst, Confidence: 1})
				edgeCount++
			}
		}
		batch = append(batch, fi)
		if len(batch) >= 500 {
			if _, _, err := st.IngestFiles(ctx, batch); err != nil {
				t.Fatalf("ingest: %v", err)
			}
			batch = nil
		}
	}
	if len(batch) > 0 {
		if _, _, err := st.IngestFiles(ctx, batch); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	t.Logf("built %d symbols, %d edges in %v", files*perFile, edgeCount, time.Since(tBuild).Round(time.Millisecond))

	// A "main" targeted by callers from `fanout` other packages.
	target := symID(0, 0)
	measure := func() (p50, p99 time.Duration, n int) {
		ds := make([]time.Duration, iterations)
		for i := range ds {
			t0 := time.Now()
			cs, _ := st.CallersOf(ctx, repo, target)
			n = len(cs)
			ds[i] = time.Since(t0)
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)/2], ds[len(ds)*99/100], n
	}

	if st.HasStats(ctx) {
		t.Fatal("expected no stats before ANALYZE")
	}
	p50Cold, p99Cold, callers := measure()
	t.Logf("NO stats : CallersOf(%d callers) p50=%v p99=%v", callers, p50Cold.Round(time.Microsecond), p99Cold.Round(time.Microsecond))

	if err := st.Analyze(ctx); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	p50Hot, p99Hot, _ := measure()
	t.Logf("WITH stats: CallersOf p50=%v p99=%v", p50Hot.Round(time.Microsecond), p99Hot.Round(time.Microsecond))
	if p99Cold > 0 {
		t.Logf("speedup p99: %.1fx", float64(p99Cold)/float64(p99Hot))
	}

	// The scalability guarantee: with stats, a structural lookup stays fast even at
	// 300k edges. (Without stats it scans them all and grows with the table.)
	if p99Hot > latencyGate {
		t.Errorf("CallersOf p99=%v exceeds %v at 300k edges — structural queries not scaling", p99Hot, latencyGate)
	}
}

// TestScaleResolveSymrefs measures symref resolution on a large synthetic graph.
// The write path uses temp-table bulk INSERT/DELETE (not per-edge ExecContext).
//
// Gated — run with:
//
//	CODEHELPER_SCALE_BENCH=1 go test ./internal/graph -run TestScaleResolveSymrefs -v
func TestScaleResolveSymrefs(t *testing.T) {
	if os.Getenv("CODEHELPER_SCALE_BENCH") == "" {
		t.Skip("set CODEHELPER_SCALE_BENCH=1 to run the symref-resolution scalability benchmark")
	}
	const (
		files      = 2000
		perFile    = 50
		symrefsPer = 2 // symref edges per symbol -> ~100k symrefs
	)
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "scale_resolve.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const repo = "r"

	symID := func(f, s int) string {
		return fmt.Sprintf("sym:r:pkg%d/file.go:%d:Fn%d_%d", f, s+1, f, s)
	}
	tBuild := time.Now()
	var batch []FileIngest
	for f := 0; f < files; f++ {
		path := fmt.Sprintf("pkg%d/file.go", f)
		fi := FileIngest{File: types.FileMeta{ID: "file:r:" + path, RepoID: repo, Path: path, Language: "go"}}
		for s := 0; s < perFile; s++ {
			id := symID(f, s)
			fi.Symbols = append(fi.Symbols, types.Symbol{ID: id, RepoID: repo, Name: fmt.Sprintf("Fn%d_%d", f, s), Kind: types.SymbolKindFunction, Path: path, LineStart: s + 1})
			// Each symbol has symrefsPer outgoing symref calls to unique targets elsewhere.
			for k := 0; k < symrefsPer; k++ {
				dstPkg := (f + k + 1) % files
				name := fmt.Sprintf("Fn%d_0", dstPkg)
				symref := fmt.Sprintf("symref:r:%s:%s", path, name)
				fi.Edges = append(fi.Edges, types.Reference{
					ID: fmt.Sprintf("e:r:%s:calls:%s", id, symref), RepoID: repo,
					Kind: types.RefKindCalls, SourceID: id, TargetID: symref, Confidence: 0.5,
				})
			}
		}
		batch = append(batch, fi)
		if len(batch) >= 500 {
			if _, _, err := st.IngestFiles(ctx, batch); err != nil {
				t.Fatalf("ingest: %v", err)
			}
			batch = nil
		}
	}
	if len(batch) > 0 {
		if _, _, err := st.IngestFiles(ctx, batch); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	t.Logf("built %d symbols with symrefs in %v", files*perFile, time.Since(tBuild).Round(time.Millisecond))

	t0 := time.Now()
	stats, err := st.ResolveSymrefs(ctx, repo)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	elapsed := time.Since(t0)
	t.Logf("ResolveSymrefs: resolved=%d total=%d in %v (%.0f edges/s)",
		stats.Resolved, stats.Total, elapsed.Round(time.Millisecond),
		float64(stats.Resolved)/elapsed.Seconds())

	if stats.Resolved == 0 {
		t.Fatal("expected symrefs to resolve")
	}
	// Sanity: a second pass is a no-op.
	stats2, err := st.ResolveSymrefs(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Resolved != 0 {
		t.Errorf("second pass should resolve nothing, got %+v", stats2)
	}
}
