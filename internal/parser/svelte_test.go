package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

const svelteSrc = `
<script lang="ts">
	import { tick } from "svelte";

	let { open: is_open = $bindable() } = $props();

	export function open() {
		is_open = !is_open;
		helper();
	}

	function helper() {
		tick();
	}

	export const greet = () => helper();
</script>

<button onclick={open}>{is_open}</button>
`

func TestParseSvelte_ScriptSymbolsAndCalls(t *testing.T) {
	t.Parallel()
	res, err := ParseSvelte(context.Background(), "r", "lib/Toggle.svelte", []byte(svelteSrc))
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]types.Symbol{}
	for _, s := range res.Symbols {
		byName[s.Name] = s
		if s.Language != "svelte" {
			t.Errorf("symbol %q language=%q want svelte", s.Name, s.Language)
		}
		if !strings.HasSuffix(s.Path, "Toggle.svelte") {
			t.Errorf("symbol %q path=%q want *.svelte", s.Name, s.Path)
		}
	}
	for _, want := range []string{"Toggle", "open", "helper", "greet"} {
		if _, ok := byName[want]; !ok {
			names := make([]string, 0, len(byName))
			for k := range byName {
				names = append(names, k)
			}
			t.Fatalf("missing symbol %q; got %#v", want, names)
		}
	}
	if byName["Toggle"].Kind != types.SymbolKindClass {
		t.Errorf("Toggle kind=%q want class", byName["Toggle"].Kind)
	}
	if byName["open"].Kind != types.SymbolKindFunction {
		t.Errorf("open kind=%q want function", byName["open"].Kind)
	}
	// Line numbers must land inside the script, not at file start.
	if byName["open"].LineStart < 5 {
		t.Errorf("open LineStart=%d; expected remapped into <script>", byName["open"].LineStart)
	}

	var calls []string
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		calls = append(calls, e.SourceID+"→"+e.TargetID)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "helper") {
		t.Fatalf("expected call edge to helper, got:\n%s", joined)
	}
	if !strings.Contains(joined, "tick") {
		t.Fatalf("expected call edge to tick, got:\n%s", joined)
	}
	var imports int
	for _, e := range res.Edges {
		if e.Kind == types.RefKindImports {
			imports++
		}
	}
	if imports == 0 {
		t.Fatal("expected import edge from script")
	}
}

func TestParseSvelte_PlainJSScript(t *testing.T) {
	t.Parallel()
	src := []byte(`<script>
  export function add(a, b) { return sum(a, b); }
  function sum(a, b) { return a + b; }
</script>
`)
	res, err := ParseSvelte(context.Background(), "r", "Add.svelte", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !names["add"] || !names["sum"] || !names["Add"] {
		t.Fatalf("got names %v", names)
	}
	found := false
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.Contains(e.TargetID, "sum") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected add→sum call edge")
	}
}

func TestExtract_SvelteNotGenericText(t *testing.T) {
	t.Parallel()
	src := []byte(`<script>
export function foo() { bar(); }
function bar() {}
</script>
<p>hi</p>
`)
	res, err := Extract(context.Background(), "r", "X.svelte", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Symbols {
		if s.Language == "text" {
			t.Fatalf("still treating as generic text: %+v", s)
		}
	}
	if len(res.Symbols) < 3 {
		t.Fatalf("expected component+funcs, got %d", len(res.Symbols))
	}
}

func TestParseSvelte_MarkupAndStyle(t *testing.T) {
	t.Parallel()
	src := []byte(`
<script>
  import Button from './Button.svelte';
</script>

<style>
  .hero { color: red; }
  .cta:hover { opacity: 0.8; }
</style>

<div class="hero">
  <Button />
  <Card title="x" />
</div>
`)
	res, err := ParseSvelte(context.Background(), "r", "lib/Page.svelte", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !names["Page"] || !names[".hero"] || !names[".cta"] {
		t.Fatalf("expected Page + css classes, got %v", names)
	}
	var pageID string
	for _, s := range res.Symbols {
		if s.Name == "Page" {
			pageID = s.ID
		}
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.SourceID != pageID {
			continue
		}
		if e.Kind == types.RefKindReads {
			if strings.HasPrefix(e.TargetID, "symref:") {
				targets[symrefName(e.TargetID)] = true
			} else if strings.Contains(e.TargetID, ":.hero") || strings.HasSuffix(e.TargetID, ":.hero") {
				targets[".hero"] = true
			} else if strings.Contains(e.TargetID, ":.cta") {
				targets[".cta"] = true
			}
		}
	}
	for _, want := range []string{"Button", "Card"} {
		if !targets[want] {
			t.Errorf("missing markup read to %q; got %#v", want, targets)
		}
	}
}

func TestParseSvelte_EventsAndRunes(t *testing.T) {
	t.Parallel()
	src := []byte(`
<script>
  export let title = '';
  let { open = $bindable(false) } = $props();
  let count = $state(0);
  function toggle() { open = !open; }
</script>

<button onclick={toggle}>{title}</button>
`)
	res, err := ParseSvelte(context.Background(), "r", "lib/Toggle.svelte", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	for _, want := range []string{"Toggle", "$props", "$state", "$bindable", "title"} {
		if !names[want] {
			t.Errorf("missing %q; got %v", want, names)
		}
	}
	var toggleCall bool
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.Contains(e.TargetID, "toggle") {
			toggleCall = true
		}
	}
	if !toggleCall {
		t.Fatal("expected component→toggle event call edge")
	}
}
