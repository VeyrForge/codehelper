package patterns

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectPattern_Modal(t *testing.T) {
	packs, err := LoadAll(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) == 0 {
		t.Fatal("expected bundled packs")
	}
	p, score := SelectPattern("make a popup for newsletter", "wordpress", packs)
	if p.ID != "modal_form" || score <= 0 {
		t.Fatalf("got id=%s score=%f", p.ID, score)
	}
}

func TestExpandRequest_Modal(t *testing.T) {
	packs, err := LoadAll(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := ExpandRequest(ExpandInput{
		Request:     "add modal dialog for signup",
		ProjectType: "wordpress",
		ChangedArea: "frontend",
	}, packs)
	if out.PatternID != "modal_form" {
		t.Fatalf("pattern: %s", out.PatternID)
	}
	if len(out.InferredRequirements) < 5 {
		t.Fatalf("expected rich checklist, got %d items", len(out.InferredRequirements))
	}
}

func TestExpandRequest_AuthUsesBundledPack(t *testing.T) {
	packs, err := LoadAll(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := ExpandRequest(ExpandInput{
		Request:     "add authentication and login sessions",
		ProjectType: "go",
		ChangedArea: "backend",
	}, packs)
	if out.PatternID != "auth" {
		t.Fatalf("pattern id = %q, want auth", out.PatternID)
	}
	if out.Intent != "change_auth" {
		t.Fatalf("intent = %q, want change_auth", out.Intent)
	}
	if len(out.InferredRequirements) < 5 {
		t.Fatalf("expected rich auth checklist, got %d", len(out.InferredRequirements))
	}
	if !containsAny(out.InferredRequirements, "authorization check", "KDF", "CSRF") {
		t.Fatalf("auth requirements missing key security items: %#v", out.InferredRequirements)
	}
}

func TestExpandRequest_APIUsesBundledPack(t *testing.T) {
	packs, err := LoadAll(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := ExpandRequest(ExpandInput{
		Request:     "add a REST API endpoint for orders",
		ProjectType: "node",
		ChangedArea: "backend",
	}, packs)
	if out.PatternID != "api_endpoint" {
		t.Fatalf("pattern id = %q, want api_endpoint", out.PatternID)
	}
	if !containsAny(out.InferredRequirements, "DTO", "pagination", "Validate every input") {
		t.Fatalf("api requirements missing key items: %#v", out.InferredRequirements)
	}
}

func TestExpandRequest_DataMigrationUsesBundledPack(t *testing.T) {
	packs, err := LoadAll(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := ExpandRequest(ExpandInput{
		Request:     "create migration to add user.email column",
		ProjectType: "laravel",
		ChangedArea: "backend",
	}, packs)
	if out.PatternID != "data_migration" {
		t.Fatalf("pattern id = %q, want data_migration", out.PatternID)
	}
	if !containsAny(out.InferredRequirements, "rollback", "idempotent", "DROP") {
		t.Fatalf("migration requirements missing key items: %#v", out.InferredRequirements)
	}
}

func TestExpandRequest_PaymentUsesBundledPack(t *testing.T) {
	packs, err := LoadAll(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := ExpandRequest(ExpandInput{
		Request:     "wire up Stripe checkout for subscriptions",
		ProjectType: "node",
		ChangedArea: "backend",
	}, packs)
	if out.PatternID != "payment" {
		t.Fatalf("pattern id = %q, want payment", out.PatternID)
	}
	if !containsAny(out.InferredRequirements, "idempotent", "webhook", "minor units") {
		t.Fatalf("payment requirements missing key items: %#v", out.InferredRequirements)
	}
}

func TestExpandRequest_BackgroundJobUsesBundledPack(t *testing.T) {
	packs, err := LoadAll(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := ExpandRequest(ExpandInput{
		Request:     "add a background job to send daily emails",
		ProjectType: "node",
		ChangedArea: "backend",
	}, packs)
	if out.PatternID != "background_job" {
		t.Fatalf("pattern id = %q, want background_job", out.PatternID)
	}
	if !containsAny(out.InferredRequirements, "idempotent", "retries", "dead-letter") {
		t.Fatalf("background job requirements missing key items: %#v", out.InferredRequirements)
	}
}

func TestExpandRequest_CachingUsesBundledPack(t *testing.T) {
	packs, err := LoadAll(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := ExpandRequest(ExpandInput{
		Request:     "introduce redis caching for product listings",
		ProjectType: "laravel",
		ChangedArea: "backend",
	}, packs)
	if out.PatternID != "caching" {
		t.Fatalf("pattern id = %q, want caching", out.PatternID)
	}
	if !containsAny(out.InferredRequirements, "TTL", "invalidation") {
		t.Fatalf("caching requirements missing key items: %#v", out.InferredRequirements)
	}
}

func TestRepoOverride(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, ".codehelper", "patterns")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"id":"modal_form","triggers":["popup"],"requirements":["custom override"]}`
	if err := os.WriteFile(filepath.Join(sub, "modal_form.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	packs, err := LoadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found Pack
	for _, p := range packs {
		if p.ID == "modal_form" {
			found = p
			break
		}
	}
	if len(found.Requirements) != 1 || found.Requirements[0] != "custom override" {
		t.Fatalf("override not applied: %#v", found.Requirements)
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func containsAny(xs []string, needles ...string) bool {
	for _, x := range xs {
		for _, n := range needles {
			if n != "" && strings.Contains(x, n) {
				return true
			}
		}
	}
	return false
}
