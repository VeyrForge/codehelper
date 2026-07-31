package mcpsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/mark3labs/mcp-go/mcp"
)

// --- #7 param aliases -------------------------------------------------------

func TestContext_AcceptsSymbolAlias(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"target.go": "package x\n\nfunc Helper() int { return 1 }\n",
	})
	req := mcp.CallToolRequest{}
	// Pass `symbol` instead of the canonical `name` — must still resolve.
	req.Params.Arguments = map[string]any{"repo": repo.Name, "symbol": "Helper", "format": "json"}
	res, err := contextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("symbol alias should resolve, got error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), "Helper") {
		t.Fatalf("expected Helper in output: %s", resultText(res))
	}
}

func TestArgFirst(t *testing.T) {
	args := map[string]any{"symbol": "Foo", "name": ""}
	if got := argFirst(args, "name", "symbol"); got != "Foo" {
		t.Fatalf("argFirst should fall through empty name to symbol, got %q", got)
	}
	if got := argFirst(map[string]any{}, "name", "symbol"); got != "" {
		t.Fatalf("argFirst with no keys should be empty, got %q", got)
	}
}

// --- #6 body=full ------------------------------------------------------------

func bigFunc(name string, bodyLines int) string {
	var b strings.Builder
	b.WriteString("package x\n\n")
	b.WriteString("func " + name + "() {\n")
	for i := 0; i < bodyLines; i++ {
		b.WriteString("\t_ = ")
		b.WriteString(itoa(i))
		b.WriteByte('\n')
	}
	b.WriteString("\tprintln(\"END_MARKER\")\n")
	b.WriteString("}\n")
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func TestContext_BodyFull_VsTruncatedDefault(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"big.go": bigFunc("Big", 90), // ~94 lines — exceeds the 40-line default cap and the 80-line auto-full threshold
	})
	call := func(args map[string]any) string {
		req := mcp.CallToolRequest{}
		args["repo"] = repo.Name
		args["name"] = "Big"
		args["format"] = "json"
		req.Params.Arguments = args
		res, err := contextHandler(reg)(ctx, req)
		if err != nil || res.IsError {
			t.Fatalf("context Big: err=%v res=%s", err, resultText(res))
		}
		return resultText(res)
	}
	def := call(map[string]any{})
	if strings.Contains(def, "END_MARKER") {
		t.Fatal("default view of a 90-line func should be truncated (no END_MARKER)")
	}
	if !strings.Contains(def, "body=full") {
		t.Fatalf("truncated view should hint body=full: %s", def)
	}
	full := call(map[string]any{"body": "full"})
	if !strings.Contains(full, "END_MARKER") {
		t.Fatalf("body=full should include the whole function (END_MARKER): %s", full)
	}
}

func TestContext_AutoFull_SmallSymbol(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"small.go": bigFunc("Small", 50), // ~54 lines — under the 80-line auto-full threshold
	})
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "name": "Small", "format": "json"}
	res, err := contextHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("context Small: err=%v res=%s", err, resultText(res))
	}
	if !strings.Contains(resultText(res), "END_MARKER") {
		t.Fatalf("a <80-line symbol should be returned in full by default: %s", resultText(res))
	}
}

// --- #2 disk fallback on symbol miss ----------------------------------------

func TestContext_DiskFallback_OnUnindexedSymbol(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Indexed() {}\n",
	})
	// Write a NEW file AFTER indexing — on disk but not in the symbol graph.
	if err := os.WriteFile(filepath.Join(repo.RootPath, "fresh.go"),
		[]byte("package x\n\nfunc ZanzibarWidget() int { return 7 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "name": "ZanzibarWidget", "format": "json"}
	res, err := contextHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	txt := resultText(res)
	if !strings.Contains(txt, "disk_matches") || !strings.Contains(txt, "fresh.go") {
		t.Fatalf("expected disk_matches pointing at fresh.go for an unindexed symbol: %s", txt)
	}
}

func TestQuery_DiskFallback_OnZeroHits(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc Indexed() {}\n",
	})
	if err := os.WriteFile(filepath.Join(repo.RootPath, "fresh.go"),
		[]byte("package x\n\nfunc ZanzibarWidget() int { return 7 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"repo": repo.Name, "query": "ZanzibarWidget", "format": "json"}
	res, err := queryHandler(reg)(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	txt := resultText(res)
	if !strings.Contains(txt, "disk_matches") || !strings.Contains(txt, "fresh.go") {
		t.Fatalf("query zero-hits should disk-fallback to fresh.go: %s", txt)
	}
}

func TestDistinctiveIdentifier(t *testing.T) {
	if got := distinctiveIdentifier("findUserSession token"); got != "findUserSession" {
		t.Fatalf("expected the camelCase identifier, got %q", got)
	}
	if got := distinctiveIdentifier("the and for"); got != "" {
		t.Fatalf("all-common/short query should yield no identifier, got %q", got)
	}
}

// --- #10 detect_changes untracked source ------------------------------------

func TestDetectChanges_ListsUntrackedSource(t *testing.T) {
	reg, repo, ctx := buildIndexedRepo(t, map[string]string{
		"a.go": "package x\n\nfunc A() {}\n",
	})
	// A brand-new untracked source file + an untracked non-source file.
	if err := os.WriteFile(filepath.Join(repo.RootPath, "brandnew.go"),
		[]byte("package x\n\nfunc Brandnew() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.RootPath, "notes.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := mcp.CallToolRequest{}
	// base_ref=HEAD: buildIndexedRepo makes a single-commit repo, so HEAD~1 doesn't
	// exist. HEAD diffs against the working tree, which is what we want here anyway.
	req.Params.Arguments = map[string]any{"repo": repo.Name, "base_ref": "HEAD", "format": "json"}
	res, err := detectChangesHandler(reg)(ctx, req)
	if err != nil || res.IsError {
		t.Fatalf("detect_changes: err=%v res=%s", err, resultText(res))
	}
	txt := resultText(res)
	if !strings.Contains(txt, "untracked_source_files") || !strings.Contains(txt, "brandnew.go") {
		t.Fatalf("expected untracked_source_files with brandnew.go: %s", txt)
	}
	if strings.Contains(txt, "notes.txt") {
		t.Fatalf("non-source untracked file should be filtered out: %s", txt)
	}
}

func TestFilterSourceFiles(t *testing.T) {
	in := []string{"a.go", "b.txt", "c/d.ts", "e.md", "f.rs"}
	out := filterSourceFiles(in)
	want := map[string]bool{"a.go": true, "c/d.ts": true, "f.rs": true}
	if len(out) != len(want) {
		t.Fatalf("filterSourceFiles=%v", out)
	}
	for _, p := range out {
		if !want[p] {
			t.Fatalf("unexpected path kept: %q", p)
		}
	}
}

// --- #4 query hot cache -----------------------------------------------------

func TestQueryHitsCache_PutGetAndTTL(t *testing.T) {
	c := &queryHitsCache{entries: map[string]queryHitsEntry{}}
	hits := []retrieval.RankedSymbol{{Score: 1}}
	c.put("k", hits)
	if got, ok := c.get("k"); !ok || len(got) != 1 {
		t.Fatalf("expected cache hit, got ok=%v len=%d", ok, len(got))
	}
	if _, ok := c.get("missing"); ok {
		t.Fatal("expected miss for unknown key")
	}
	// Expire by backdating the entry beyond the TTL.
	c.entries["k"] = queryHitsEntry{hits: hits, at: time.Now().Add(-queryCacheTTL - time.Second)}
	if _, ok := c.get("k"); ok {
		t.Fatal("expected expired entry to miss")
	}
}
