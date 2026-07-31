package graph_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/meta"
)

func ciTestbed(t *testing.T, name string) (root, repo string, st *graph.Store) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	candidates := []string{
		filepath.Join(repoRoot, ".testbeds", "active", name),
		filepath.Join(repoRoot, ".ci-testbeds-tmp", name), // legacy local OUT
	}
	root = ""
	for _, cand := range candidates {
		if _, err := os.Stat(filepath.Join(cand, ".codehelper", "graph.db")); err == nil {
			root = cand
			break
		}
	}
	if root == "" {
		t.Skipf("no indexed bed at %s (run scripts/testbeds-all.sh prepare)", candidates[0])
	}
	m, err := meta.Read(root)
	if err != nil || m == nil {
		t.Fatalf("meta: %v", err)
	}
	st, err = graph.Open(filepath.Join(root, ".codehelper", "graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return root, m.RepoName, st
}

func TestLiveUnityBed_HealthInbound(t *testing.T) {
	_, repo, st := ciTestbed(t, "unity")
	ctx := context.Background()
	syms, err := st.SymbolsByName(ctx, repo, "Health", 20)
	if err != nil {
		t.Fatal(err)
	}
	var healthID string
	for _, s := range syms {
		if string(s.Kind) == "class" && strings.Contains(s.Path, "Health.cs") {
			healthID = s.ID
			break
		}
	}
	if healthID == "" {
		t.Fatalf("Health class not indexed; hits=%d", len(syms))
	}
	callers, err := st.CallersOf(ctx, repo, healthID)
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) == 0 {
		t.Fatal("Health has 0 inbound — GetComponent/RequireComponent densify missing after analyze")
	}
	var sawAwake bool
	for _, c := range callers {
		if c.Name == "Awake" || c.Name == "PlayerController" {
			sawAwake = true
		}
	}
	if !sawAwake {
		t.Fatalf("expected Awake/PlayerController inbound to Health, got %#v", callers)
	}
	t.Logf("unity Health inbound callers=%d (score densified)", len(callers))
}

func TestLiveGodotBed_ReadyDisambiguation(t *testing.T) {
	_, repo, st := ciTestbed(t, "godot")
	ctx := context.Background()
	syms, err := st.SymbolsByName(ctx, repo, "_ready", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) < 2 {
		t.Fatalf("want >=2 _ready (scripts + addon), got %d", len(syms))
	}
	var game, addon int
	var paths []string
	for _, s := range syms {
		paths = append(paths, s.Path+"#"+s.ParentID)
		p := strings.ToLower(strings.ReplaceAll(s.Path, "\\", "/"))
		switch {
		case strings.Contains(p, "addons/") || strings.HasPrefix(p, "addons/"):
			addon++
			if s.ParentID == "" {
				t.Errorf("addon _ready missing ParentID (file stem): %s", s.Path)
			}
		case strings.Contains(p, "player.gd"):
			game++
			if s.ParentID != "Player" {
				t.Errorf("player _ready ParentID=%q want Player", s.ParentID)
			}
		}
	}
	if game == 0 || addon == 0 {
		t.Fatalf("need both game and addon _ready; game=%d addon=%d paths=%v", game, addon, paths)
	}
	t.Logf("godot _ready hits game=%d addon=%d (ranking demotes addons/ in MCP)", game, addon)
}

func TestLiveGodotBed_TakeHitInbound(t *testing.T) {
	_, repo, st := ciTestbed(t, "godot")
	ctx := context.Background()
	syms, err := st.SymbolsByName(ctx, repo, "take_hit", 10)
	if err != nil {
		t.Fatal(err)
	}
	var takeID string
	for _, s := range syms {
		if s.ParentID == "Enemy" || strings.Contains(s.Path, "enemy.gd") {
			takeID = s.ID
			break
		}
	}
	if takeID == "" {
		t.Fatalf("Enemy.take_hit not indexed; hits=%d", len(syms))
	}
	callers, err := st.CallersOf(ctx, repo, takeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) == 0 {
		t.Fatal("take_hit has 0 inbound — Player._ready Enemy.take_hit densify missing after analyze")
	}
	var sawReady bool
	for _, c := range callers {
		if c.Name == "_ready" {
			sawReady = true
		}
	}
	if !sawReady {
		t.Fatalf("expected Player._ready inbound to take_hit, got %#v", callers)
	}
	t.Logf("godot take_hit inbound callers=%d (score densified)", len(callers))
}

func TestLiveUnrealBed_HealthInbound(t *testing.T) {
	_, repo, st := ciTestbed(t, "unreal")
	ctx := context.Background()
	syms, err := st.SymbolsByName(ctx, repo, "UHealthComponent", 10)
	if err != nil {
		t.Fatal(err)
	}
	var healthID string
	for _, s := range syms {
		if string(s.Kind) == "class" {
			healthID = s.ID
			break
		}
	}
	if healthID == "" {
		t.Fatalf("UHealthComponent class not indexed; hits=%d", len(syms))
	}
	callers, err := st.CallersOf(ctx, repo, healthID)
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) == 0 {
		t.Fatal("UHealthComponent has 0 inbound — Cast/CreateDefaultSubobject densify missing after analyze")
	}
	var sawBeginOrCtor bool
	for _, c := range callers {
		if c.Name == "BeginPlay" || c.Name == "AMyGameCharacter" || c.Name == "TakeDamage" || c.Name == "HealthComp" {
			sawBeginOrCtor = true
		}
	}
	if !sawBeginOrCtor {
		t.Fatalf("expected BeginPlay/ctor/TakeDamage inbound to UHealthComponent, got %#v", callers)
	}
	applySyms, err := st.SymbolsByName(ctx, repo, "ApplyDamage", 10)
	if err != nil {
		t.Fatal(err)
	}
	var applyID string
	for _, s := range applySyms {
		if s.ParentID == "UHealthComponent" {
			applyID = s.ID
			break
		}
	}
	if applyID != "" {
		ac, err := st.CallersOf(ctx, repo, applyID)
		if err != nil {
			t.Fatal(err)
		}
		if len(ac) == 0 {
			t.Fatal("ApplyDamage has 0 inbound — typed UHealthComponent.ApplyDamage missing after analyze")
		}
		t.Logf("unreal ApplyDamage inbound callers=%d", len(ac))
	}
	t.Logf("unreal UHealthComponent inbound callers=%d (score densified)", len(callers))
}
