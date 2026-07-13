package hints

import "testing"

func TestEnsureBuiltin_IsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	EnsureBuiltin()
	list, err := List(ScopeGlobal, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 2 {
		t.Fatalf("expected at least 2 builtin global hints, got %d", len(list))
	}
	EnsureBuiltin()
	list2, _ := List(ScopeGlobal, "")
	if len(list2) != len(list) {
		t.Fatalf("EnsureBuiltin should be idempotent: %d vs %d", len(list), len(list2))
	}
	m, _ := MatchingFor("", "go", []string{"go"}, nil)
	if len(m) < 2 {
		t.Fatalf("expected builtin hints in MatchingFor, got %v", m)
	}
}

func TestHintsLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.codehelper

	h, err := Add(ScopeFramework, "wordpress", "Bump asset version with filemtime().", "translate-plugin")
	if err != nil {
		t.Fatal(err)
	}
	if h.ID == "" || h.ScopeType != "framework" || h.Scope != "wordpress" {
		t.Fatalf("unexpected stored hint: %+v", h)
	}
	// Dedup: adding the same text again returns the same id, no duplicate.
	again, _ := Add(ScopeFramework, "wordpress", "Bump asset version with filemtime().", "x")
	if again.ID != h.ID {
		t.Errorf("expected dedup to same id, got %s vs %s", again.ID, h.ID)
	}
	if list, _ := List("", ""); len(list) != 1 {
		t.Fatalf("expected 1 hint after dedup, got %d", len(list))
	}

	// A global hint + a language hint.
	_, _ = Add(ScopeGlobal, "", "Always run the project's verify before claiming done.", "")
	_, _ = Add(ScopeLanguage, "go", "Wrap errors with %w.", "")

	// Matching: a wordpress/php project gets the wordpress + global hints, not go.
	m, _ := MatchingFor("wordpress", "wordpress_plugin", []string{"php"}, nil)
	if len(m) != 2 {
		t.Fatalf("expected 2 matching hints (wordpress+global), got %d: %v", len(m), m)
	}
	// A go project gets the go + global hints, not wordpress.
	mg, _ := MatchingFor("", "go", []string{"go"}, nil)
	if len(mg) != 2 {
		t.Fatalf("expected 2 matching hints (go+global), got %d: %v", len(mg), mg)
	}

	// Export/import round-trips into a fresh HOME.
	data, err := Export()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	added, err := ImportMerge(data)
	if err != nil {
		t.Fatal(err)
	}
	if added != 3 {
		t.Errorf("expected 3 imported hints, got %d", added)
	}

	// Remove by id.
	ok, _ := Remove(h.ID)
	if !ok {
		t.Errorf("expected removal of %s to succeed", h.ID)
	}
}
