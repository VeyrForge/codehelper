package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestSvelteTestbed_ToggleAndPage(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "svelte", "lib")
	toggleSrc, err := os.ReadFile(filepath.Join(root, "Toggle.svelte"))
	if err != nil {
		t.Fatal(err)
	}
	pageSrc, err := os.ReadFile(filepath.Join(root, "Page.svelte"))
	if err != nil {
		t.Fatal(err)
	}
	toggle, err := ParseSvelte(context.Background(), "s", "lib/Toggle.svelte", toggleSrc)
	if err != nil {
		t.Fatal(err)
	}
	page, err := ParseSvelte(context.Background(), "s", "lib/Page.svelte", pageSrc)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range toggle.Symbols {
		names[s.Name] = true
	}
	for _, want := range []string{"Toggle", "toggle", "greet", "format"} {
		if !names[want] {
			t.Fatalf("Toggle.svelte missing %q; got %v", want, names)
		}
	}
	var pageID string
	for _, s := range page.Symbols {
		if s.Name == "Page" {
			pageID = s.ID
		}
	}
	reads := map[string]bool{}
	for _, e := range page.Edges {
		if e.SourceID == pageID && e.Kind == types.RefKindReads && strings.HasPrefix(e.TargetID, "symref:") {
			reads[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"Toggle", "Card"} {
		if !reads[want] {
			t.Fatalf("Page missing markup read %q; got %#v", want, reads)
		}
	}
}

func TestVueTestbed_Greeter(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "vue", "src")
	src, err := os.ReadFile(filepath.Join(root, "Greeter.vue"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseVue(context.Background(), "v", "src/Greeter.vue", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	sigs := map[string]string{}
	for _, s := range res.Symbols {
		names[s.Name] = true
		sigs[s.Name] = s.Signature
	}
	for _, want := range []string{"Greeter", "greet", "helper", "defineProps", "defineEmits", "open", "label"} {
		if !names[want] {
			t.Fatalf("missing %q; got %v", want, names)
		}
	}
	if sigs["open"] != "vue-ref" || sigs["label"] != "vue-computed" {
		t.Fatalf("ref/computed sigs open=%q label=%q", sigs["open"], sigs["label"])
	}
	var greetCall, labelRead bool
	var greeterID string
	for _, s := range res.Symbols {
		if s.Name == "Greeter" {
			greeterID = s.ID
		}
	}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.Contains(e.TargetID, "greet") {
			greetCall = true
		}
		if e.SourceID == greeterID && e.Kind == types.RefKindReads && strings.HasSuffix(e.TargetID, ":label") {
			labelRead = true
		}
	}
	if !greetCall {
		t.Fatal("expected @click→greet call edge")
	}
	if !labelRead {
		t.Fatal("expected template→label reads edge")
	}
}

func TestAstroTestbed_Index(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "astro", "src", "pages")
	src, err := os.ReadFile(filepath.Join(root, "Index.astro"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseAstro(context.Background(), "a", "src/pages/Index.astro", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	sigs := map[string]string{}
	for _, s := range res.Symbols {
		names[s.Name] = true
		sigs[s.Name] = s.Signature
	}
	if !names["Index"] || !names["getStaticPaths"] || !names["island:Card"] {
		t.Fatalf("missing Index/getStaticPaths/island:Card; got %v", names)
	}
	if !strings.Contains(sigs["island:Card"], "role=island") || !strings.Contains(sigs["island:Card"], "client=load") {
		t.Fatalf("island:Card signature=%q", sigs["island:Card"])
	}
	var indexID string
	for _, s := range res.Symbols {
		if s.Name == "Index" {
			indexID = s.ID
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
	if !reads["Card"] || !reads["Widget"] || !reads["island:Card"] {
		t.Fatalf("missing Card/Widget/island:Card reads; got %#v", reads)
	}
}

func TestMDXTestbed_Intro(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "mdx", "docs")
	src, err := os.ReadFile(filepath.Join(root, "Intro.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseMDX(context.Background(), "m", "docs/Intro.mdx", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	sigs := map[string]string{}
	for _, s := range res.Symbols {
		names[s.Name] = true
		sigs[s.Name] = s.Signature
	}
	for _, want := range []string{"Intro", "highlight", "fence", "Callout", "Hint"} {
		if !names[want] {
			t.Fatalf("missing %q; got %v", want, names)
		}
	}
	if !strings.Contains(sigs["Callout"], "role=component") {
		t.Fatalf("Callout signature=%q", sigs["Callout"])
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
			t.Fatalf("missing read %q; got %#v", want, reads)
		}
	}
}
