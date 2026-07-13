package mcpsvc

import (
	"reflect"
	"testing"
)

// jsonFieldTags returns the `json:"..."` tag values of every field of a struct
// type, in declaration order, so two structs can be compared for JSON parity.
func jsonFieldTags(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Tag.Get("json"))
	}
	return out
}

// TestQueryToolResponseSchemaParity guards the schema-mirror invariant: the
// OUTPUT-SCHEMA struct (queryToolResponseSchema) must expose exactly the same JSON
// fields, in the same order, as the real response (queryToolResponse). Only the Go
// type of `hits` differs (any vs []map[string]any) — see the type doc comment.
// If they drift, the advertised outputSchema stops matching what the handler
// returns, which is how strict MCP clients silently lose tools.
func TestQueryToolResponseSchemaParity(t *testing.T) {
	real := jsonFieldTags(reflect.TypeOf(queryToolResponse{}))
	schema := jsonFieldTags(reflect.TypeOf(queryToolResponseSchema{}))
	if !reflect.DeepEqual(real, schema) {
		t.Fatalf("queryToolResponse and queryToolResponseSchema JSON fields diverged:\n real:   %v\n schema: %v", real, schema)
	}
}
