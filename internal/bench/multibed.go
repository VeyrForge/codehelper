package bench

// MultiBedCoverage documents which methodology-lite paired probes map to which
// indexed OSS beds. Kept in-package so `go doc` / BENCHMARK.md stay aligned.
//
// Run: CODEHELPER_TESTBEDS=… scripts/mcp-paired-eval.sh
// CI: optional job when CODEHELPER_TESTBEDS is set (see .github/workflows/ci.yml).

// BedTier is a coarse graph-quality expectation for eval sampling
// (mcp-eval-methodology §1.1 hold-out stacks).
type BedTier string

const (
	BedTierStrong BedTier = "strong" // dense call graph (Go/Rust-ish)
	BedTierMedium BedTier = "medium" // framework apps with DI/routes
	BedTierWeak   BedTier = "weak"   // library / sparse inbound
)

// BedProbe describes one multi-bed coverage slot.
type BedProbe struct {
	Bed   string
	Tier  BedTier
	Kinds []string // architecture_qa, fix_bug_orient, feature_orient, …
}

// DefaultMultiBedCoverage is the 12-bed lite suite used by
// internal/mcpsvc.TestPairedMCPLiteTestbeds.
func DefaultMultiBedCoverage() []BedProbe {
	return []BedProbe{
		{Bed: "axum", Tier: BedTierStrong, Kinds: []string{"architecture_qa"}},
		{Bed: "gin", Tier: BedTierStrong, Kinds: []string{"architecture_qa"}},
		{Bed: "fiber", Tier: BedTierStrong, Kinds: []string{"feature_orient"}},
		{Bed: "fastapi", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}},
		{Bed: "flask", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}},
		{Bed: "djangorest", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}},
		{Bed: "nest", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}},
		{Bed: "laravel", Tier: BedTierMedium, Kinds: []string{"feature_orient"}},
		{Bed: "sinatra", Tier: BedTierMedium, Kinds: []string{"feature_orient"}},
		{Bed: "spring-petclinic", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}},
		{Bed: "svelte", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}},
		{Bed: "express", Tier: BedTierWeak, Kinds: []string{"fix_bug_orient"}},
	}
}
