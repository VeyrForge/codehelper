package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseGDScriptLite(t *testing.T) {
	src := []byte(`class_name CustomObstacleLibrary
extends Resource

signal library_changed
const MAX_ITEMS = 64
enum Direction { UP, DOWN }
@export var folder_name: String
@onready var _cache := {}

func add_obstacle(def):
	_validate(def)
	MatchConfig.reset_lobby()
	NetworkManager.host_game(NetworkManager.DEFAULT_PORT)
	library_changed.connect(_on_changed)
	library_changed.emit()
	var packed = preload("res://addons/thing.gd")
	pass

static func load_default():
	return null

func _on_changed():
	pass

class InnerThing:
	var value
`)
	res, err := parseGDScriptLite(context.Background(), "repo", "custom_obstacle_library.gd", src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"CustomObstacleLibrary": false, "library_changed": false, "MAX_ITEMS": false,
		"Direction": false, "folder_name": false, "_cache": false,
		"add_obstacle": false, "load_default": false, "InnerThing": false, "value": false,
		"_on_changed": false,
	}
	for _, s := range res.Symbols {
		if _, ok := want[s.Name]; ok {
			want[s.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected GDScript symbol %q to be extracted", name)
		}
	}
	calls := 0
	var callTargets []string
	imports := 0
	for _, e := range res.Edges {
		switch e.Kind {
		case types.RefKindCalls:
			calls++
			callTargets = append(callTargets, e.TargetID)
		case types.RefKindImports:
			imports++
		}
	}
	if calls < 3 {
		t.Fatalf("expected GDScript call edges (>=3), got %d targets=%v", calls, callTargets)
	}
	joined := strings.Join(callTargets, ",")
	for _, wantCall := range []string{"_validate", "reset_lobby", "host_game", "_on_changed", "library_changed"} {
		if !strings.Contains(joined, wantCall) {
			t.Errorf("expected call target containing %q in %v", wantCall, callTargets)
		}
	}
	if imports < 1 {
		t.Fatalf("expected preload/load import edge, got %d", imports)
	}
	var sawExtends bool
	for _, e := range res.Edges {
		if e.Kind == types.RefKindReads && strings.HasSuffix(e.TargetID, ":Resource") {
			sawExtends = true
		}
	}
	if !sawExtends {
		t.Fatal("expected extends Resource read edge")
	}
}
