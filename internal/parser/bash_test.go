package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseBashFunctionsAndCalls(t *testing.T) {
	src := []byte(`#!/usr/bin/env bash
log_info() {
  echo "hi"
  deploy_app "$1"
  git status
}
deploy_app() {
  curl -fsS "$1"
  prepare_env
}
prepare_env() {
  :
}
`)
	res, err := ParseBash(context.Background(), "repo", "deploy.sh", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, s := range res.Symbols {
		found[s.Name] = true
	}
	for _, want := range []string{"log_info", "deploy_app", "prepare_env"} {
		if !found[want] {
			t.Errorf("missing function %q; got %v", want, found)
		}
	}
	var callNames []string
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		if i := strings.LastIndex(e.TargetID, ":"); i >= 0 {
			callNames = append(callNames, e.TargetID[i+1:])
		}
	}
	joined := strings.Join(callNames, ",")
	for _, want := range []string{"deploy_app", "prepare_env"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected call to %q in %v", want, callNames)
		}
	}
	for _, noise := range []string{"echo", "git", "curl"} {
		if strings.Contains(joined, noise) {
			t.Errorf("did not expect builtin/CLI call %q in %v", noise, callNames)
		}
	}
}
