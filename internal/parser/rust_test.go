package parser

import (
	"context"
	"strings"
	"testing"
)

const rustSrc = `
/// Schedules experts and prefetches their weights.
/// Returns the chosen expert ids.
#[inline]
pub fn schedule_experts(layer: u32, topk: usize) -> Vec<u32> {
    helper();
    Vec::new()
}

fn helper() {}

/// A manifest describing how layer weights are compressed.
pub struct WeightManifest {
    pub bits: u8,
}

/// Strategy used to drift experts between layers.
pub enum ExpertDrift {
    None,
    Linear,
}
`

func findSym(syms []symSlice, name string) (symSlice, bool) {
	for _, s := range syms {
		if s.Name == name {
			return s, true
		}
	}
	return symSlice{}, false
}

type symSlice struct {
	Name string
	Sig  string
}

func TestParseRustCapturesDocsAndSignature(t *testing.T) {
	res, err := ParseRust(context.Background(), "r", "sched.rs", []byte(rustSrc))
	if err != nil {
		t.Fatal(err)
	}
	var syms []symSlice
	for _, s := range res.Symbols {
		syms = append(syms, symSlice{Name: s.Name, Sig: s.Signature})
	}

	fn, ok := findSym(syms, "schedule_experts")
	if !ok {
		t.Fatalf("schedule_experts not parsed; got %+v", syms)
	}
	if !strings.Contains(fn.Sig, "Schedules experts and prefetches their weights") {
		t.Errorf("fn doc comment not captured: %q", fn.Sig)
	}
	if !strings.Contains(fn.Sig, "layer: u32") || !strings.Contains(fn.Sig, "-> Vec<u32>") {
		t.Errorf("fn params/return not captured: %q", fn.Sig)
	}

	st, ok := findSym(syms, "WeightManifest")
	if !ok {
		t.Fatalf("struct WeightManifest not parsed; got %+v", syms)
	}
	if !strings.Contains(st.Sig, "manifest describing how layer weights are compressed") {
		t.Errorf("struct doc not captured: %q", st.Sig)
	}

	en, ok := findSym(syms, "ExpertDrift")
	if !ok {
		t.Fatalf("enum ExpertDrift not parsed; got %+v", syms)
	}
	if !strings.Contains(en.Sig, "drift experts between layers") {
		t.Errorf("enum doc not captured: %q", en.Sig)
	}
}

const axumRouterSrc = `
pub struct Router<S = ()> {}

pub trait Handler {}

impl Clone for Router {}

impl Handler for Router {}

async fn handler() -> &'static str { "ok" }
async fn health() {}

fn app() -> Router {
    Router::new()
        .route("/", get(handler))
        .route("/health", get(health))
}
`

func TestParseRust_RouterTypeUseAndHandlers(t *testing.T) {
	t.Parallel()
	res, err := ParseRust(context.Background(), "r", "main.rs", []byte(axumRouterSrc))
	if err != nil {
		t.Fatal(err)
	}

	var readsToRouter, callsToHandler, implements int
	for _, e := range res.Edges {
		switch e.Kind {
		case "reads":
			if strings.Contains(e.TargetID, ":Router") && strings.Contains(e.SourceID, ":app") {
				readsToRouter++
			}
		case "calls":
			if strings.Contains(e.SourceID, ":app") &&
				(strings.Contains(e.TargetID, ":handler") || strings.Contains(e.TargetID, ":health")) {
				callsToHandler++
			}
		case "implements":
			implements++
		}
	}
	if readsToRouter == 0 {
		t.Fatal("expected app→Router type-use (reads) edge")
	}
	if callsToHandler < 2 {
		t.Fatalf("expected app→handler and app→health call edges, got %d", callsToHandler)
	}
	if implements == 0 {
		t.Fatal("expected implements edges for impl Trait for Router")
	}
}
