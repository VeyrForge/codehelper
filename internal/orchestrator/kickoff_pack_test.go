package orchestrator

import "testing"

func TestEnrichPackFromKickoff(t *testing.T) {
	raw := `{
		"orient":{"project_type":"go","summary":"A Go service","languages":["go"]},
		"reuse_candidates":[{"name":"HandleAuth","loc":"internal/auth/handler.go:12"}],
		"steps":["Extend HandleAuth validation","Add tests"],
		"decision_points":["Use middleware or handler wrapper?"],
		"verification":["go test ./..."]
	}`
	var pack ContextPack
	top := enrichPackFromKickoff(raw, &pack)
	if top != "HandleAuth" {
		t.Fatalf("top=%q", top)
	}
	if pack.OrientLine == "" || len(pack.Symbols) != 1 || len(pack.Steps) != 2 {
		t.Fatalf("pack=%+v", pack)
	}
}
