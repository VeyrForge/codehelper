package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseVue_ScriptSetupEventsAndMacros(t *testing.T) {
	t.Parallel()
	src := []byte(`
<script setup lang="ts">
  import Button from './Button.vue'
  import { ref, computed } from 'vue'
  const props = defineProps<{ title: string }>()
  const emit = defineEmits<{ click: [] }>()
  const open = ref(false)
  const label = computed(() => props.title.toUpperCase())
  function toggle() { emit('click') }
  function helper() { toggle() }
</script>

<template>
  <div class="hero" v-if="open">
    <h1>{{ label }}</h1>
    <Button @click="toggle" />
    <Card v-on:submit="helper" />
  </div>
</template>

<style>
  .hero { color: red; }
</style>
`)
	res, err := ParseVue(context.Background(), "r", "src/Greeter.vue", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	sigs := map[string]string{}
	for _, s := range res.Symbols {
		names[s.Name] = true
		sigs[s.Name] = s.Signature
		if s.Language != "vue" {
			t.Errorf("symbol %q language=%q want vue", s.Name, s.Language)
		}
	}
	for _, want := range []string{"Greeter", "toggle", "helper", "defineProps", "defineEmits", ".hero", "open", "label"} {
		if !names[want] {
			t.Errorf("missing %q; got %v", want, names)
		}
	}
	if sigs["open"] != "vue-ref" {
		t.Errorf("open signature=%q want vue-ref", sigs["open"])
	}
	if sigs["label"] != "vue-computed" {
		t.Errorf("label signature=%q want vue-computed", sigs["label"])
	}
	var greeterID, openID string
	for _, s := range res.Symbols {
		if s.Name == "Greeter" {
			greeterID = s.ID
		}
		if s.Name == "open" {
			openID = s.ID
		}
	}
	reads := map[string]bool{}
	calls := map[string]bool{}
	for _, e := range res.Edges {
		if e.SourceID != greeterID {
			continue
		}
		if e.Kind == types.RefKindReads {
			reads[symrefName(e.TargetID)] = true
		}
		if e.Kind == types.RefKindCalls {
			calls[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"Button", "Card", "open", "label"} {
		if !reads[want] {
			t.Errorf("missing markup/reactivity read %q; got %#v", want, reads)
		}
	}
	for _, want := range []string{"toggle", "helper", "defineProps"} {
		if !calls[want] {
			t.Errorf("missing call %q; got %#v", want, calls)
		}
	}
	var refCall bool
	for _, e := range res.Edges {
		if e.SourceID == openID && e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":ref") {
			refCall = true
		}
	}
	if !refCall {
		t.Fatal("expected open→ref call edge")
	}
	var imports int
	for _, e := range res.Edges {
		if e.Kind == types.RefKindImports {
			imports++
		}
	}
	if imports == 0 {
		t.Fatal("expected import edge from script setup")
	}
}

func TestExtract_VueNotGenericText(t *testing.T) {
	t.Parallel()
	src := []byte(`<script setup>
export function foo() { bar() }
function bar() {}
</script>
`)
	res, err := Extract(context.Background(), "r", "X.vue", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Symbols {
		if s.Language == "text" {
			t.Fatalf("still treating as generic text: %+v", s)
		}
	}
}

func TestParseAstro_FrontmatterAndMarkup(t *testing.T) {
	t.Parallel()
	src := []byte(`---
import Card from './Card.astro'
export function getStaticPaths() {
  return [{ params: { id: '1' } }]
}
const title = 'hi'
---
<html>
  <body>
    <Card client:load title={title} />
    <Widget client:idle />
  </body>
</html>
<style>
  .wrap { margin: 0; }
</style>
`)
	res, err := ParseAstro(context.Background(), "r", "src/pages/Index.astro", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	sigs := map[string]string{}
	for _, s := range res.Symbols {
		names[s.Name] = true
		sigs[s.Name] = s.Signature
		if s.Language != "astro" {
			t.Errorf("symbol %q language=%q want astro", s.Name, s.Language)
		}
	}
	for _, want := range []string{"Index", "getStaticPaths", ".wrap", "island:Card", "island:Widget"} {
		if !names[want] {
			t.Errorf("missing %q; got %v", want, names)
		}
	}
	if !strings.Contains(sigs["island:Card"], "role=island") || !strings.Contains(sigs["island:Card"], "client=load") {
		t.Errorf("island:Card signature=%q want role=island;client=load", sigs["island:Card"])
	}
	if !strings.Contains(sigs["island:Widget"], "client=idle") {
		t.Errorf("island:Widget signature=%q want client=idle", sigs["island:Widget"])
	}
	var indexID, islandCardID string
	for _, s := range res.Symbols {
		if s.Name == "Index" {
			indexID = s.ID
		}
		if s.Name == "island:Card" {
			islandCardID = s.ID
		}
	}
	reads := map[string]bool{}
	for _, e := range res.Edges {
		if e.SourceID != indexID || e.Kind != types.RefKindReads {
			continue
		}
		reads[symrefName(e.TargetID)] = true
		for _, s := range res.Symbols {
			if s.ID == e.TargetID {
				reads[s.Name] = true
			}
		}
	}
	for _, want := range []string{"Card", "Widget", "island:Card", "island:Widget"} {
		if !reads[want] {
			t.Errorf("missing markup/island read %q; got %#v", want, reads)
		}
	}
	var islandToCard bool
	for _, e := range res.Edges {
		if e.SourceID == islandCardID && e.Kind == types.RefKindReads && strings.HasSuffix(e.TargetID, ":Card") {
			islandToCard = true
		}
	}
	if !islandToCard {
		t.Fatal("expected island:Card→Card reads edge")
	}
	var imports int
	for _, e := range res.Edges {
		if e.Kind == types.RefKindImports {
			imports++
		}
	}
	if imports == 0 {
		t.Fatal("expected frontmatter import edge")
	}
}

func TestParseMDX_IslandsAndComponents(t *testing.T) {
	t.Parallel()
	src := []byte(`import Callout from './Callout.mdx'
import { Note as Hint } from './Note.mdx'

export function highlight(text) {
  return text.toUpperCase()
}

# Hello

Here is a <Callout /> and a <Hint />.

Also render {Badge} inline.

` + "```ts\nexport function fence() { return 1 }\n```\n")
	res, err := ParseMDX(context.Background(), "r", "docs/Intro.mdx", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	sigs := map[string]string{}
	for _, s := range res.Symbols {
		names[s.Name] = true
		sigs[s.Name] = s.Signature
		if s.Language != "mdx" {
			t.Errorf("symbol %q language=%q want mdx", s.Name, s.Language)
		}
	}
	for _, want := range []string{"Intro", "highlight", "fence", "Callout", "Hint"} {
		if !names[want] {
			t.Errorf("missing %q; got %v", want, names)
		}
	}
	if !strings.Contains(sigs["Callout"], "role=component") {
		t.Errorf("Callout signature=%q want role=component", sigs["Callout"])
	}
	var introID string
	for _, s := range res.Symbols {
		if s.Name == "Intro" {
			introID = s.ID
		}
	}
	reads := map[string]bool{}
	for _, e := range res.Edges {
		if e.SourceID == introID && e.Kind == types.RefKindReads {
			reads[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"Callout", "Hint", "Badge"} {
		if !reads[want] {
			t.Errorf("missing JSX/import/expr read %q; got %#v", want, reads)
		}
	}
}
