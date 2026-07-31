package contracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverProtobuf_ServicesMessagesRPCs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	protoDir := filepath.Join(dir, "proto")
	if err := os.MkdirAll(filepath.Join(protoDir, "common"), 0o755); err != nil {
		t.Fatal(err)
	}
	typesProto := `syntax = "proto3";
package demo.common;
message Timestamp {
  int64 seconds = 1;
}
`
	userProto := `syntax = "proto3";
package demo.v1;
import "common/types.proto";

enum Status {
  STATUS_UNSPECIFIED = 0;
}

message User {
  string id = 1;
}

message GetUserRequest {
  string id = 1;
}

service UserService {
  rpc GetUser (GetUserRequest) returns (User);
  rpc ListUsers (ListUsersRequest) returns (stream User);
}
`
	if err := os.WriteFile(filepath.Join(protoDir, "common", "types.proto"), []byte(typesProto), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "user.proto"), []byte(userProto), 0o644); err != nil {
		t.Fatal(err)
	}
	got := DiscoverProtobuf(dir)
	if len(got) < 2 {
		t.Fatalf("DiscoverProtobuf=%#v", got)
	}
	var user *ProtobufContract
	for i := range got {
		if strings.HasSuffix(filepath.ToSlash(got[i].Path), "proto/user.proto") {
			user = &got[i]
			break
		}
	}
	if user == nil {
		t.Fatalf("missing user.proto in %#v", got)
	}
	if user.Package != "demo.v1" {
		t.Fatalf("package=%q", user.Package)
	}
	svc := map[string]struct{}{}
	for _, s := range user.Services {
		svc[s] = struct{}{}
	}
	if _, ok := svc["UserService"]; !ok {
		t.Fatalf("services=%v", user.Services)
	}
	rpcs := map[string]struct{}{}
	for _, r := range user.RPCs {
		rpcs[r] = struct{}{}
	}
	for _, want := range []string{"UserService.GetUser", "UserService.ListUsers"} {
		if _, ok := rpcs[want]; !ok {
			t.Fatalf("missing rpc %s in %v", want, user.RPCs)
		}
	}
	msgs := map[string]struct{}{}
	for _, m := range user.Messages {
		msgs[m] = struct{}{}
	}
	if _, ok := msgs["User"]; !ok {
		t.Fatalf("messages=%v", user.Messages)
	}
	foundImport := false
	for _, imp := range user.Imports {
		if imp == "common/types.proto" {
			foundImport = true
		}
	}
	if !foundImport {
		t.Fatalf("imports=%v", user.Imports)
	}
}

func TestDiscoverAll_ProtobufLinks(t *testing.T) {
	t.Parallel()
	a := t.TempDir()
	b := t.TempDir()
	protoA := `syntax = "proto3";
package demo.v1;
message User { string id = 1; }
service UserService {
  rpc GetUser (GetUserRequest) returns (User);
}
`
	protoB := `syntax = "proto3";
package demo.v1;
message User { string id = 1; string name = 2; }
service UserService {
  rpc GetUser (GetUserRequest) returns (User);
  rpc CreateUser (CreateUserRequest) returns (User);
}
`
	if err := os.MkdirAll(filepath.Join(a, "proto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(b, "proto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "proto", "user.proto"), []byte(protoA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "proto", "user.proto"), []byte(protoB), 0o644); err != nil {
		t.Fatal(err)
	}
	ba := DiscoverAll(a).AnnotateRepo("api")
	bb := DiscoverAll(b).AnnotateRepo("web")
	if len(ba.Protobuf) < 1 || len(bb.Protobuf) < 1 {
		t.Fatalf("protobuf a=%#v b=%#v", ba.Protobuf, bb.Protobuf)
	}
	links := LinkAcrossBundles([]Bundle{ba, bb}, LinkOptions{
		SameGroupRepos: map[string]struct{}{"api": {}, "web": {}},
	})
	kinds := map[string][]string{}
	for _, l := range links {
		kinds[l.Kind] = append(kinds[l.Kind], l.Key)
	}
	has := func(kind, key string) bool {
		for _, k := range kinds[kind] {
			if k == key {
				return true
			}
		}
		return false
	}
	if !has("protobuf_service", "UserService") {
		t.Fatalf("missing protobuf_service in %#v", kinds)
	}
	if !has("protobuf_message", "User") {
		t.Fatalf("missing protobuf_message in %#v", kinds)
	}
	if !has("protobuf_rpc", "UserService.GetUser") {
		t.Fatalf("missing protobuf_rpc in %#v", kinds)
	}
}

func TestDiscoverGraphQL_TypesAndOps(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	schema := `# demo schema
type User {
  id: ID!
  name: String!
}

type Query {
  user(id: ID!): User
  users: [User!]!
}

type Mutation {
  createUser(name: String!): User
}
`
	if err := os.WriteFile(filepath.Join(dir, "schema.graphql"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	got := DiscoverGraphQL(dir)
	if len(got) != 1 {
		t.Fatalf("DiscoverGraphQL=%#v", got)
	}
	c := got[0]
	if len(c.Types) < 3 {
		t.Fatalf("types=%v", c.Types)
	}
	ops := map[string]struct{}{}
	for _, op := range c.Operations {
		ops[op] = struct{}{}
	}
	for _, want := range []string{"Query.user", "Query.users", "Mutation.createUser"} {
		if _, ok := ops[want]; !ok {
			t.Fatalf("missing op %s in %v", want, c.Operations)
		}
	}
}

func TestDiscoverEvents_AsyncAPIAndCloudEvents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	asyncJSON := `{
  "asyncapi": "2.6.0",
  "info": {"title": "Orders", "version": "1.0.0"},
  "channels": {
    "order/created": {},
    "order/shipped": {}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "asyncapi.json"), []byte(asyncJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	ce := `{"specversion":"1.0","type":"com.acme.order.created","source":"/orders"}`
	if err := os.MkdirAll(filepath.Join(dir, "events"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events", "cloudevents.json"), []byte(ce), 0o644); err != nil {
		t.Fatal(err)
	}
	yamlEvents := "events:\n  - com.acme.order.shipped\n  - com.acme.order.cancelled\n"
	if err := os.WriteFile(filepath.Join(dir, "events.yaml"), []byte(yamlEvents), 0o644); err != nil {
		t.Fatal(err)
	}
	got := DiscoverEvents(dir)
	if len(got) < 3 {
		t.Fatalf("DiscoverEvents=%#v", got)
	}
	bySource := map[string]EventContract{}
	for _, c := range got {
		bySource[c.Source] = c
	}
	if len(bySource["asyncapi"].Channels) != 2 {
		t.Fatalf("asyncapi=%#v", bySource["asyncapi"])
	}
	if len(bySource["cloudevents"].EventNames) < 1 || bySource["cloudevents"].EventNames[0] != "com.acme.order.created" {
		t.Fatalf("cloudevents=%#v", bySource["cloudevents"])
	}
	if len(bySource["event_pattern"].EventNames) < 2 {
		t.Fatalf("event_pattern=%#v", bySource["event_pattern"])
	}
}

func TestDiscoverAll_AndLinkAcrossBundles(t *testing.T) {
	t.Parallel()
	a := t.TempDir()
	b := t.TempDir()
	schemaA := "type Query {\n  order(id: ID!): Order\n}\ntype Order { id: ID! }\n"
	schemaB := "type Query {\n  order(id: ID!): Order\n}\ntype Order { id: ID! }\ntype User { id: ID! }\n"
	if err := os.WriteFile(filepath.Join(a, "schema.graphql"), []byte(schemaA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "schema.graphql"), []byte(schemaB), 0o644); err != nil {
		t.Fatal(err)
	}
	evA := `{"asyncapi":"2.6.0","channels":{"order/created":{}}}`
	evB := `{"asyncapi":"2.6.0","channels":{"order/created":{},"order/shipped":{}}}`
	ceA := `{"specversion":"1.0","type":"com.acme.order.created"}`
	ceB := `{"specversion":"1.0","type":"com.acme.order.created"}`
	if err := os.WriteFile(filepath.Join(a, "asyncapi.json"), []byte(evA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "asyncapi.json"), []byte(evB), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a, "events"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(b, "events"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "events", "cloudevents.json"), []byte(ceA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "events", "cloudevents.json"), []byte(ceB), 0o644); err != nil {
		t.Fatal(err)
	}
	openA := `{"openapi":"3.0.0","info":{"title":"A","version":"1"},"paths":{"/orders":{}}}`
	openB := `{"openapi":"3.0.0","info":{"title":"B","version":"1"},"paths":{"/orders":{},"/users":{}}}`
	if err := os.WriteFile(filepath.Join(a, "openapi.json"), []byte(openA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "openapi.json"), []byte(openB), 0o644); err != nil {
		t.Fatal(err)
	}

	ba := DiscoverAll(a).AnnotateRepo("api")
	bb := DiscoverAll(b).AnnotateRepo("web")
	if ba.Count() < 3 || bb.Count() < 3 {
		t.Fatalf("counts a=%d b=%d ba=%#v bb=%#v", ba.Count(), bb.Count(), ba, bb)
	}
	links := LinkAcrossBundles([]Bundle{ba, bb}, LinkOptions{
		SameGroupRepos: map[string]struct{}{"api": {}, "web": {}},
	})
	kinds := map[string][]string{}
	for _, l := range links {
		kinds[l.Kind] = append(kinds[l.Kind], l.Key)
		if len(l.Occurrences) < 2 {
			t.Fatalf("link %#v should have ≥2 occurrences", l)
		}
		for _, o := range l.Occurrences {
			if !o.SameGroup {
				t.Fatalf("expected same_group on %#v", o)
			}
		}
	}
	has := func(kind, key string) bool {
		for _, k := range kinds[kind] {
			if k == key {
				return true
			}
		}
		return false
	}
	if !has("api_path", "/orders") {
		t.Fatalf("missing api_path /orders in %#v", kinds)
	}
	if !has("graphql_type", "Order") || !has("graphql_op", "Query.order") {
		t.Fatalf("missing graphql links in %#v", kinds)
	}
	if !has("channel", "order/created") {
		t.Fatalf("missing channel order/created in %#v", kinds)
	}
	if !has("event", "com.acme.order.created") {
		t.Fatalf("missing event com.acme.order.created in %#v", kinds)
	}
}

func TestDiscoverOpenAPI_JSONAndYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jsonSpec := `{
  "openapi": "3.0.0",
  "info": {"title": "Demo API", "version": "1.2.0"},
  "paths": {
    "/users": {},
    "/users/{id}": {}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "openapi.json"), []byte(jsonSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	yamlSpec := "openapi: 3.0.3\ninfo:\n  title: YAML API\n  version: 0.1.0\npaths:\n  /health:\n    get: {}\n  /v1/items:\n    get: {}\n"
	if err := os.MkdirAll(filepath.Join(dir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api", "openapi.yaml"), []byte(yamlSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	got := DiscoverOpenAPI(dir)
	if len(got) != 2 {
		t.Fatalf("DiscoverOpenAPI=%#v", got)
	}
	byFmt := map[string]OpenAPIContract{}
	for _, c := range got {
		byFmt[c.Format] = c
	}
	if byFmt["json"].Title != "Demo API" || len(byFmt["json"].APIPaths) != 2 {
		t.Fatalf("json contract=%#v", byFmt["json"])
	}
	if byFmt["yaml"].Title != "YAML API" || len(byFmt["yaml"].APIPaths) < 2 {
		t.Fatalf("yaml contract=%#v", byFmt["yaml"])
	}
}

func TestDiscoverOpenAPI_ShallowDirScan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "openapi"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := `{"openapi":"3.0.0","info":{"title":"Dir","version":"1"},"paths":{"/ping":{}}}`
	if err := os.WriteFile(filepath.Join(dir, "openapi", "service.openapi.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-matching name in the same dir must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "openapi", "notes.yaml"), []byte("title: not a spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := DiscoverOpenAPI(dir)
	if len(got) != 1 || len(got[0].APIPaths) != 1 || got[0].APIPaths[0] != "/ping" {
		t.Fatalf("DiscoverOpenAPI=%#v", got)
	}
}

func TestDiscoverGraphQL_ShallowDirAndRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	schema := "type Query {\n  ping: String!\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "schemas", "ops.graphql"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.gql"), []byte("type Mutation {\n  noop: Boolean\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := DiscoverGraphQL(dir)
	if len(got) != 2 {
		t.Fatalf("DiscoverGraphQL=%#v", got)
	}
	ops := map[string]struct{}{}
	for _, c := range got {
		for _, op := range c.Operations {
			ops[op] = struct{}{}
		}
	}
	if _, ok := ops["Query.ping"]; !ok {
		t.Fatalf("missing Query.ping in %#v", ops)
	}
	if _, ok := ops["Mutation.noop"]; !ok {
		t.Fatalf("missing Mutation.noop in %#v", ops)
	}
}

func TestDiscover_StubSchemasTracked(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "testdata", "contracts")
	if _, err := os.Stat(root); err != nil {
		t.Skip("testdata/contracts stubs missing")
	}
	api := DiscoverAll(filepath.Join(root, "api")).AnnotateRepo("api")
	web := DiscoverAll(filepath.Join(root, "web")).AnnotateRepo("web")
	if len(api.OpenAPI) < 1 || len(api.GraphQL) < 1 {
		t.Fatalf("api stubs thin: %#v", api)
	}
	if len(web.OpenAPI) < 1 || len(web.GraphQL) < 1 {
		t.Fatalf("web stubs thin: %#v", web)
	}
	dirOpen := DiscoverOpenAPI(filepath.Join(root, "openapi"))
	if len(dirOpen) < 1 || dirOpen[0].Title == "" {
		t.Fatalf("openapi/ dir stub=%#v", dirOpen)
	}
	dirGQL := DiscoverGraphQL(filepath.Join(root, "graphql"))
	if len(dirGQL) < 1 {
		t.Fatalf("graphql/ dir stub=%#v", dirGQL)
	}
	links := LinkAcrossBundles([]Bundle{api, web}, LinkOptions{
		SameGroupRepos: map[string]struct{}{"api": {}, "web": {}},
	})
	has := map[string]bool{}
	for _, l := range links {
		has[l.Kind+"|"+l.Key] = true
	}
	for _, want := range []string{"api_path|/orders", "graphql_type|Order", "graphql_op|Query.order"} {
		if !has[want] {
			t.Fatalf("missing link %s in %#v", want, links)
		}
	}
}

func TestDiscover_RealOSSBedsIfPresent(t *testing.T) {
	t.Parallel()
	base, err := filepath.Abs(filepath.Join("..", "..", ".testbeds", "real-oss"))
	if err != nil {
		t.Skip(".testbeds/real-oss not present")
	}
	if _, err := os.Stat(base); err != nil {
		t.Skip(".testbeds/real-oss not present")
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	var samples []string
	seen := map[string]struct{}{}
	scanBed := func(name string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		bed := filepath.Join(base, name)
		info, err := os.Stat(bed)
		if err != nil || !info.IsDir() {
			return
		}
		b := DiscoverAll(bed)
		n := b.Count()
		if n == 0 {
			return
		}
		found += n
		samples = append(samples, name)
	}
	// Prefer known beds (Stat follows junctions; DirEntry.IsDir can miss mount points).
	for _, prefer := range []string{"multi-repo-a", "multi-repo-b", "fastapi", "nest", "express", "gin"} {
		scanBed(prefer)
		if len(samples) >= 8 {
			break
		}
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		scanBed(e.Name())
		if len(samples) >= 8 {
			break
		}
	}
	if found == 0 {
		// Honest: most OSS clones generate OpenAPI at runtime and ship no SDL.
		t.Log("no on-disk OpenAPI/GraphQL/event contracts in real-oss beds (expected for FastAPI/etc.); stubs cover discovery")
		return
	}
	t.Logf("discovered %d contract file(s) across beds: %s", found, strings.Join(samples, ", "))
}
