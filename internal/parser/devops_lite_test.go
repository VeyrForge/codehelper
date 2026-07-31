package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestDevopsKind(t *testing.T) {
	cases := map[string]string{
		"Dockerfile":                "dockerfile",
		"dockerfile":                "dockerfile",
		"Dockerfile.dev":            "dockerfile",
		"Containerfile":             "dockerfile",
		"Makefile":                  "makefile",
		"GNUmakefile":               "makefile",
		"docker-compose.yml":        "compose",
		"compose.yaml":              "compose",
		"src/Dockerfile":            "dockerfile",
		"deploy/docker-compose.yml": "compose",
		"main.go":                   "",
		"app.yml":                   "",
	}
	for path, want := range cases {
		if got := devopsKind(path); got != want {
			t.Errorf("%s: got %q want %q", path, got, want)
		}
	}
}

func TestParseDockerfileLite_StagesAndCopyFrom(t *testing.T) {
	src := []byte(`
FROM golang:1.22 AS builder
WORKDIR /src
COPY . .
RUN go build -o app

FROM alpine:3.20 AS runtime
COPY --from=builder /src/app /usr/local/bin/app
CMD ["app"]
`)
	res, err := parseDockerfileLite(context.Background(), "repo", "Dockerfile", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, s := range res.Symbols {
		found[s.Name] = s.ID
		if s.Language != "dockerfile" {
			t.Errorf("lang %q", s.Language)
		}
	}
	if found["builder"] == "" || found["runtime"] == "" {
		t.Fatalf("missing stages: %v", found)
	}
	saw := false
	for _, e := range res.Edges {
		if e.SourceID == found["runtime"] && e.Kind == types.RefKindReads &&
			(e.TargetID == found["builder"] || strings.HasSuffix(e.TargetID, ":builder")) {
			saw = true
		}
	}
	if !saw {
		t.Fatal("expected runtime READS builder via COPY --from")
	}
}

func TestParseComposeLite_ServicesDependsOn(t *testing.T) {
	src := []byte(`
services:
  db:
    image: postgres:16
  api:
    build: ./api
    depends_on:
      - db
  web:
    image: nginx:alpine
    depends_on: [api, db]

networks:
  front:

volumes:
  pgdata:
`)
	res, err := parseComposeLite(context.Background(), "repo", "docker-compose.yml", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, s := range res.Symbols {
		found[s.Name] = s.ID
	}
	for _, want := range []string{"db", "api", "web", "network:front", "volume:pgdata"} {
		if found[want] == "" {
			t.Errorf("missing %q; got %v", want, found)
		}
	}
	apiReadsDB := false
	webReadsAPI := false
	for _, e := range res.Edges {
		if e.Kind != types.RefKindReads {
			continue
		}
		if e.SourceID == found["api"] && (e.TargetID == found["db"] || strings.HasSuffix(e.TargetID, ":db")) {
			apiReadsDB = true
		}
		if e.SourceID == found["web"] && (e.TargetID == found["api"] || strings.HasSuffix(e.TargetID, ":api")) {
			webReadsAPI = true
		}
	}
	if !apiReadsDB {
		t.Fatal("expected api READS db")
	}
	if !webReadsAPI {
		t.Fatal("expected web READS api")
	}
}

func TestParseMakefileLite_TargetsAndPrereqs(t *testing.T) {
	src := []byte(`
.PHONY: all test build
FOO=1
build: deps
	go build ./...
test: build
	go test ./...
deps:
	go mod download
`)
	res, err := parseMakefileLite(context.Background(), "repo", "Makefile", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, s := range res.Symbols {
		found[s.Name] = s.ID
		if s.Kind != types.SymbolKindFunction {
			t.Errorf("%s kind %s", s.Name, s.Kind)
		}
	}
	for _, want := range []string{"build", "test", "deps"} {
		if found[want] == "" {
			t.Errorf("missing target %q; got %v", want, found)
		}
	}
	if found[".PHONY"] != "" || found["FOO"] != "" {
		t.Fatalf("should skip special/vars: %v", found)
	}
	buildReadsDeps := false
	testReadsBuild := false
	for _, e := range res.Edges {
		if e.Kind != types.RefKindReads {
			continue
		}
		if e.SourceID == found["build"] && (e.TargetID == found["deps"] || strings.HasSuffix(e.TargetID, ":deps")) {
			buildReadsDeps = true
		}
		if e.SourceID == found["test"] && (e.TargetID == found["build"] || strings.HasSuffix(e.TargetID, ":build")) {
			testReadsBuild = true
		}
	}
	if !buildReadsDeps || !testReadsBuild {
		t.Fatalf("prereq reads missing build→deps=%v test→build=%v", buildReadsDeps, testReadsBuild)
	}
}

func TestExtract_RoutesDevOpsBasenames(t *testing.T) {
	df, err := Extract(context.Background(), "r", "Dockerfile", []byte("FROM alpine AS runtime\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(df.Symbols) != 1 || df.Symbols[0].Name != "runtime" {
		t.Fatalf("dockerfile extract: %+v", df.Symbols)
	}
	mf, err := Extract(context.Background(), "r", "Makefile", []byte("build:\n\techo ok\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Symbols) != 1 || mf.Symbols[0].Name != "build" {
		t.Fatalf("makefile extract: %+v", mf.Symbols)
	}
	cf, err := Extract(context.Background(), "r", "docker-compose.yml", []byte("services:\n  web:\n    image: nginx\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cf.Symbols) != 1 || cf.Symbols[0].Name != "web" {
		t.Fatalf("compose extract: %+v", cf.Symbols)
	}
}
