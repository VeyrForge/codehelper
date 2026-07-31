package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseProtobuf_ServicesRPCsImports(t *testing.T) {
	src := []byte(`syntax = "proto3";
package demo.v1;

import "google/protobuf/timestamp.proto";
import public "common/types.proto";

enum Status {
  STATUS_UNSPECIFIED = 0;
  STATUS_ACTIVE = 1;
}

message User {
  string id = 1;
  string name = 2;
  Status status = 3;
}

message GetUserRequest {
  string id = 1;
}

service UserService {
  rpc GetUser (GetUserRequest) returns (User);
  rpc ListUsers (ListUsersRequest) returns (stream User);
}
`)
	res, err := ParseProtobuf(context.Background(), "repo", "proto/user.proto", src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]types.SymbolKind{
		"Status":         types.SymbolKindEnum,
		"User":           types.SymbolKindClass,
		"GetUserRequest": types.SymbolKindClass,
		"UserService":    types.SymbolKindInterface,
		"GetUser":        types.SymbolKindMethod,
		"ListUsers":      types.SymbolKindMethod,
	}
	found := map[string]types.SymbolKind{}
	for _, s := range res.Symbols {
		found[s.Name] = s.Kind
		if s.Name == "GetUser" {
			if s.ParentID != "UserService" {
				t.Errorf("GetUser ParentID=%q want UserService", s.ParentID)
			}
			if !strings.Contains(s.Signature, "GetUser") || !strings.Contains(s.Signature, "returns") {
				t.Errorf("GetUser Signature=%q", s.Signature)
			}
		}
		if s.Name == "ListUsers" && s.ParentID != "UserService" {
			t.Errorf("ListUsers ParentID=%q want UserService", s.ParentID)
		}
	}
	for name, kind := range want {
		if found[name] != kind {
			t.Errorf("%s: got %v want %v (found=%v)", name, found[name], kind, found)
		}
	}
	importWant := map[string]bool{
		"google/protobuf/timestamp.proto": false,
		"common/types.proto":              false,
	}
	for _, imp := range res.Imports {
		if _, ok := importWant[imp]; ok {
			importWant[imp] = true
		}
	}
	for path, ok := range importWant {
		if !ok {
			t.Errorf("missing import %q in %v", path, res.Imports)
		}
	}
	importEdges := 0
	for _, e := range res.Edges {
		if e.Kind != types.RefKindImports {
			continue
		}
		importEdges++
		if !strings.Contains(e.TargetID, "timestamp.proto") && !strings.Contains(e.TargetID, "types.proto") {
			t.Errorf("unexpected import target %s", e.TargetID)
		}
	}
	if importEdges < 2 {
		t.Fatalf("import edges=%d want ≥2", importEdges)
	}
}
