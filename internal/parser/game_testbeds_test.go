package parser

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func testbedRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(root, "testdata", "minimal-testbeds")
}

func TestUnityTestbed_GetComponentHealthInbound(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "unity", "Assets", "Scripts")
	playerSrc, err := os.ReadFile(filepath.Join(root, "PlayerController.cs"))
	if err != nil {
		t.Fatal(err)
	}
	healthSrc, err := os.ReadFile(filepath.Join(root, "Health.cs"))
	if err != nil {
		t.Fatal(err)
	}
	player, err := ParseCSharp(context.Background(), "u", "Assets/Scripts/PlayerController.cs", playerSrc)
	if err != nil {
		t.Fatal(err)
	}
	health, err := ParseCSharp(context.Background(), "u", "Assets/Scripts/Health.cs", healthSrc)
	if err != nil {
		t.Fatal(err)
	}
	var healthClass bool
	for _, s := range health.Symbols {
		if s.Name == "Health" && s.Kind == types.SymbolKindClass {
			healthClass = true
		}
	}
	if !healthClass {
		t.Fatal("Health class missing from Health.cs extract")
	}
	reads := map[string]bool{}
	for _, e := range player.Edges {
		if e.Kind != types.RefKindReads {
			continue
		}
		if i := strings.LastIndex(e.TargetID, ":"); i >= 0 {
			reads[e.TargetID[i+1:]] = true
		}
	}
	for _, want := range []string{"Health", "HealthBar"} {
		if !reads[want] {
			t.Fatalf("PlayerController missing Unity type read %q; got %#v", want, reads)
		}
	}
}

func TestGodotTestbed_ReadyParentAndAddonCollision(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "godot")
	playerSrc, err := os.ReadFile(filepath.Join(root, "scripts", "player.gd"))
	if err != nil {
		t.Fatal(err)
	}
	addonSrc, err := os.ReadFile(filepath.Join(root, "addons", "vendor_ui", "plugin.gd"))
	if err != nil {
		t.Fatal(err)
	}
	enemySrc, err := os.ReadFile(filepath.Join(root, "scripts", "enemy.gd"))
	if err != nil {
		t.Fatal(err)
	}

	player, err := parseGDScriptLite(context.Background(), "g", "scripts/player.gd", playerSrc)
	if err != nil {
		t.Fatal(err)
	}
	addon, err := parseGDScriptLite(context.Background(), "g", "addons/vendor_ui/plugin.gd", addonSrc)
	if err != nil {
		t.Fatal(err)
	}
	enemy, err := parseGDScriptLite(context.Background(), "g", "scripts/enemy.gd", enemySrc)
	if err != nil {
		t.Fatal(err)
	}

	findReady := func(res *ParseResult, wantParent string) {
		t.Helper()
		for _, s := range res.Symbols {
			if s.Name == "_ready" {
				if s.ParentID != wantParent {
					t.Fatalf("_ready ParentID=%q want %q path=%s", s.ParentID, wantParent, s.Path)
				}
				return
			}
		}
		t.Fatalf("missing _ready (want parent %q)", wantParent)
	}
	findReady(player, "Player")
	findReady(addon, "plugin") // file-stem fallback (no class_name)
	findReady(enemy, "Enemy")

	var takeHitMethod bool
	for _, s := range enemy.Symbols {
		if s.Name == "take_hit" {
			if s.Kind != types.SymbolKindMethod || s.ParentID != "Enemy" {
				t.Fatalf("take_hit kind=%s parent=%q want method/Enemy", s.Kind, s.ParentID)
			}
			takeHitMethod = true
		}
	}
	if !takeHitMethod {
		t.Fatal("expected Enemy.take_hit as method for recv_type resolve")
	}

	// Player inherits Node from class_name source; calls Enemy.take_hit for impact.
	var sawInheritsNode, sawEnemyNew, sawTakeHit, sawHealthBar, sawSetAmount, sawConnect bool
	for _, e := range player.Edges {
		leaf := e.TargetID
		if i := strings.LastIndex(leaf, ":"); i >= 0 {
			leaf = leaf[i+1:]
		}
		switch e.Kind {
		case types.RefKindInherits:
			if leaf == "Node" {
				sawInheritsNode = true
			}
		case types.RefKindCalls:
			if leaf == "take_hit" || leaf == "Enemy.take_hit" {
				sawTakeHit = true
			}
			if leaf == "Enemy" || leaf == "new" {
				sawEnemyNew = true
			}
			if leaf == "set_amount" || leaf == "HealthBar.set_amount" {
				sawSetAmount = true
			}
			if leaf == "_on_moved" {
				sawConnect = true
			}
		case types.RefKindReads:
			if leaf == "Enemy" {
				sawEnemyNew = true
			}
			if leaf == "HealthBar" {
				sawHealthBar = true
			}
		}
	}
	if !sawInheritsNode {
		t.Fatal("expected Player inherits→Node")
	}
	if !sawEnemyNew || !sawTakeHit {
		t.Fatalf("expected Player→Enemy densify (new/read + take_hit); enemyNew=%v takeHit=%v", sawEnemyNew, sawTakeHit)
	}
	if !sawHealthBar || !sawSetAmount {
		t.Fatalf("expected HealthBar read + set_amount; healthBar=%v setAmount=%v", sawHealthBar, sawSetAmount)
	}
	if !sawConnect {
		t.Fatal("expected moved.connect(_on_moved) call edge")
	}
}

func TestUnrealTestbed_UEMacrosAndHealthReads(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "unreal", "Source", "MyGame")
	charH, err := os.ReadFile(filepath.Join(root, "MyGameCharacter.h"))
	if err != nil {
		t.Fatal(err)
	}
	charCpp, err := os.ReadFile(filepath.Join(root, "MyGameCharacter.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	healthH, err := os.ReadFile(filepath.Join(root, "HealthComponent.h"))
	if err != nil {
		t.Fatal(err)
	}

	hRes, err := ParseCpp(context.Background(), "ue", "Source/MyGame/MyGameCharacter.h", charH)
	if err != nil {
		t.Fatal(err)
	}
	cRes, err := ParseCpp(context.Background(), "ue", "Source/MyGame/MyGameCharacter.cpp", charCpp)
	if err != nil {
		t.Fatal(err)
	}
	healthRes, err := ParseCpp(context.Background(), "ue", "Source/MyGame/HealthComponent.h", healthH)
	if err != nil {
		t.Fatal(err)
	}

	var sawChar, sawHealthCompClass, sawTake, sawApply bool
	for _, s := range hRes.Symbols {
		if s.Name == "AMyGameCharacter" && s.Kind == types.SymbolKindClass {
			sawChar = true
		}
		if s.Name == "TakeDamage" && s.Kind == types.SymbolKindMethod && s.ParentID == "AMyGameCharacter" {
			sawTake = true
		}
	}
	for _, s := range healthRes.Symbols {
		if s.Name == "UHealthComponent" && s.Kind == types.SymbolKindClass {
			sawHealthCompClass = true
		}
		if s.Name == "ApplyDamage" && s.Kind == types.SymbolKindMethod && s.ParentID == "UHealthComponent" {
			sawApply = true
		}
	}
	if !sawChar || !sawHealthCompClass || !sawTake || !sawApply {
		t.Fatalf("unreal bed symbols incomplete char=%v health=%v take=%v apply=%v",
			sawChar, sawHealthCompClass, sawTake, sawApply)
	}

	reads := map[string]bool{}
	for _, e := range append(hRes.Edges, cRes.Edges...) {
		if e.Kind != types.RefKindReads {
			continue
		}
		if i := strings.LastIndex(e.TargetID, ":"); i >= 0 {
			reads[e.TargetID[i+1:]] = true
		}
	}
	if !reads["UHealthComponent"] {
		t.Fatalf("missing UHealthComponent reads from character; got %#v", reads)
	}

	// Measurable densify vs pre-UE stub shape (class + 2 out-of-line methods, no macros).
	oldH := []byte(`#pragma once
#include "CoreMinimal.h"
class AMyGameCharacter {
public:
	void BeginPlay();
	void TakeDamage(float Amount);
private:
	float Health = 100.f;
};
`)
	oldC := []byte(`#include "MyGameCharacter.h"
void AMyGameCharacter::BeginPlay() {}
void AMyGameCharacter::TakeDamage(float Amount) { Health -= Amount; }
`)
	oldHdr, err := ParseCpp(context.Background(), "ue", "Source/MyGame/MyGameCharacter.h", oldH)
	if err != nil {
		t.Fatal(err)
	}
	oldCpp, err := ParseCpp(context.Background(), "ue", "Source/MyGame/MyGameCharacter.cpp", oldC)
	if err != nil {
		t.Fatal(err)
	}
	beforeSym := len(oldHdr.Symbols) + len(oldCpp.Symbols)
	beforeEdge := len(oldHdr.Edges) + len(oldCpp.Edges)
	afterSym := len(hRes.Symbols) + len(cRes.Symbols) + len(healthRes.Symbols)
	afterEdge := len(hRes.Edges) + len(cRes.Edges) + len(healthRes.Edges)
	if afterSym <= beforeSym || afterEdge <= beforeEdge {
		t.Fatalf("expected densify after>before; before syms=%d edges=%d after syms=%d edges=%d",
			beforeSym, beforeEdge, afterSym, afterEdge)
	}
	t.Logf("unreal locate densify: before symbols=%d edges=%d → after symbols=%d edges=%d (header+cpp+HealthComponent.h)",
		beforeSym, beforeEdge, afterSym, afterEdge)
}

func TestLuaTestbed_GreetFormatRequire(t *testing.T) {
	path := filepath.Join(testbedRoot(t), "lua", "greeter.lua")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseLua(context.Background(), "lua", "greeter.lua", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Symbols) < 1 {
		t.Fatalf("lua stub must index ≥1 symbols, got 0 (ParseLua missing function_statement?)")
	}
	found := map[string]bool{}
	for _, s := range res.Symbols {
		found[s.Name] = true
	}
	if !found["format"] || !found["greet"] {
		t.Fatalf("expected format+greet from greeter.lua, got %v", found)
	}
	if len(res.Imports) == 0 || !strings.Contains(strings.Join(res.Imports, ","), "helpers") {
		t.Fatalf("expected require(\"helpers\"), got %v", res.Imports)
	}
	calls := 0
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls++
		}
	}
	if calls == 0 {
		t.Fatal("expected call edges from greet→format / string.upper")
	}
}
