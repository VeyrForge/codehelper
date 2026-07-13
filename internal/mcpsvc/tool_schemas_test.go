package mcpsvc

import (
	"encoding/json"
	"testing"

	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/server"
)

// TestAllToolSchemasValid registers every MCP tool exactly as production does and
// checks that each advertised inputSchema/outputSchema is well-formed JSON Schema.
//
// This guards a real production failure mode: a single malformed schema (e.g. a
// property that serializes to a bare `true` instead of a schema object, which is
// how an `any`-typed Go field reflects) makes strict MCP clients reject the WHOLE
// tools/list — silently disabling every codehelper tool at once. Only `query` was
// guarded before (TestQueryToolResponseSchemaParity); this covers all ~60 tools.
func TestAllToolSchemasValid(t *testing.T) {
	reg, err := registry.Load()
	if err != nil {
		t.Skipf("registry load failed: %v", err)
	}
	// Tool registration is static — schemas do not depend on an indexed repo.
	srv := server.NewMCPServer("codehelper-schema-test", "0")
	RegisterAll(srv, reg)

	tools := srv.ListTools()
	if len(tools) == 0 {
		t.Fatal("no tools registered")
	}

	for name, st := range tools {
		// MarshalJSON is what the server sends in tools/list; it errors if a tool
		// sets both InputSchema and RawInputSchema, etc.
		raw, err := json.Marshal(st.Tool)
		if err != nil {
			t.Errorf("%s: tool marshal failed: %v", name, err)
			continue
		}
		var envelope struct {
			InputSchema  json.RawMessage `json:"inputSchema"`
			OutputSchema json.RawMessage `json:"outputSchema"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Errorf("%s: tool JSON unparseable: %v", name, err)
			continue
		}
		if len(envelope.InputSchema) == 0 {
			t.Errorf("%s: missing inputSchema", name)
			continue
		}
		for _, problem := range schemaProblems(name+".inputSchema", envelope.InputSchema) {
			t.Errorf("%s", problem)
		}
		if len(envelope.OutputSchema) > 0 {
			for _, problem := range schemaProblems(name+".outputSchema", envelope.OutputSchema) {
				t.Errorf("%s", problem)
			}
		}
	}
}

// TestSchemaProblemsCatchesBadSchemas proves the validator is not vacuous: it must
// reject the exact shapes that break strict MCP clients.
func TestSchemaProblemsCatchesBadSchemas(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		want   bool // expect at least one problem
	}{
		{"valid object", `{"type":"object","properties":{"q":{"type":"string"}}}`, false},
		{"valid no props", `{"type":"object"}`, false},
		{"bare boolean schema", `true`, true},
		{"non-object root type", `{"type":"array"}`, true},
		{"boolean property (the any->true bug)", `{"type":"object","properties":{"x":true}}`, true},
		{"nested boolean property", `{"type":"object","properties":{"o":{"type":"object","properties":{"y":false}}}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := schemaProblems("t", json.RawMessage(tc.schema))
			if (len(got) > 0) != tc.want {
				t.Fatalf("schemaProblems(%s) = %v, want problems=%v", tc.schema, got, tc.want)
			}
		})
	}
}

// schemaProblems returns human-readable problems with a top-level JSON Schema:
// it must be an object-typed schema, and every property (recursively) must be a
// JSON Schema OBJECT — never a bare boolean, the shape that poisons tools/list.
// Empty slice means the schema is client-safe.
func schemaProblems(path string, raw json.RawMessage) []string {
	var problems []string
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return []string{path + ": unparseable schema: " + err.Error()}
	}
	obj, ok := node.(map[string]any)
	if !ok {
		return []string{path + ": schema is not a JSON object (a bare boolean/array schema breaks strict clients)"}
	}
	if typ, _ := obj["type"].(string); typ != "object" {
		problems = append(problems, path+`: root schema type is not "object"`)
	}
	problems = append(problems, propProblems(path, obj)...)
	return problems
}

// propProblems recurses through a schema's "properties", flagging any value that
// is not a JSON object. An `any`-typed Go field reflects to a boolean property
// schema, which is what made strict clients reject the entire tools/list.
func propProblems(path string, schema map[string]any) []string {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	var problems []string
	for pname, pval := range props {
		pobj, ok := pval.(map[string]any)
		if !ok {
			problems = append(problems, path+".properties."+pname+": property schema is not a JSON object (bare booleans poison tools/list)")
			continue
		}
		problems = append(problems, propProblems(path+".properties."+pname, pobj)...)
	}
	return problems
}
