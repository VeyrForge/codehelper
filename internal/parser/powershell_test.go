package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParsePowerShellLite_FunctionsAndCalls(t *testing.T) {
	src := []byte(`
function Write-Info {
  param($Msg)
  Write-Host $Msg
  Deploy-App $Msg
}
function Deploy-App {
  param($Target)
  Prepare-Env
  Get-ChildItem .
}
function Prepare-Env {
  $x = 1
}
`)
	res, err := parsePowerShellLite(context.Background(), "repo", "deploy.ps1", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, s := range res.Symbols {
		found[s.Name] = s.ID
		if s.Language != "powershell" {
			t.Errorf("lang %q", s.Language)
		}
	}
	for _, want := range []string{"Write-Info", "Deploy-App", "Prepare-Env"} {
		if found[want] == "" {
			t.Errorf("missing %q; got %v", want, found)
		}
	}
	var callPairs []string
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		srcName, tgtName := "", ""
		for n, id := range found {
			if id == e.SourceID {
				srcName = n
			}
			if id == e.TargetID {
				tgtName = n
			}
		}
		callPairs = append(callPairs, srcName+"→"+tgtName)
	}
	joined := strings.Join(callPairs, ",")
	if !strings.Contains(joined, "Write-Info→Deploy-App") {
		t.Errorf("expected Write-Info→Deploy-App in %v", callPairs)
	}
	if !strings.Contains(joined, "Deploy-App→Prepare-Env") {
		t.Errorf("expected Deploy-App→Prepare-Env in %v", callPairs)
	}
	for _, noise := range []string{"Write-Host", "Get-ChildItem"} {
		if strings.Contains(joined, noise) {
			t.Errorf("did not expect cmdlet call involving %q in %v", noise, callPairs)
		}
	}
}
