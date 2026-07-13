package parser

import (
	"context"
	"testing"
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
	pass

static func load_default():
	return null

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
}
