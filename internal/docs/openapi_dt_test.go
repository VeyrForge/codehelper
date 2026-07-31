package docs

import (
	"strings"
	"testing"
)

func TestResolveDefinitelyTypedAndOpenAPI(t *testing.T) {
	dt := Resolve("@types/node", "")
	if dt.Origin != "derived" || dt.Ecosystem != "npm" {
		t.Fatalf("origin=%q eco=%q want derived/npm", dt.Origin, dt.Ecosystem)
	}
	if !strings.Contains(dt.DocBase, "DefinitelyTyped") || !strings.Contains(dt.DocBase, "types/node") {
		t.Errorf("DocBase=%q want DefinitelyTyped types/node", dt.DocBase)
	}
	if len(dt.Sources) != 1 || dt.Sources[0].Kind != "html" {
		t.Errorf("sources=%+v want single html", dt.Sources)
	}

	oa := Resolve("https://api.example.com/openapi.json", "")
	if oa.Origin != "direct-url" {
		t.Fatalf("origin=%q want direct-url", oa.Origin)
	}
	if !strings.Contains(oa.Note, "OpenAPI") {
		t.Errorf("note=%q want OpenAPI hint", oa.Note)
	}
	if len(oa.Sources) != 1 || oa.Sources[0].URL != "https://api.example.com/openapi.json" {
		t.Errorf("sources=%+v", oa.Sources)
	}
}
