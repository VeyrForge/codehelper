package bench

import "testing"

func TestDefaultMultiBedCoverage(t *testing.T) {
	cov := DefaultMultiBedCoverage()
	if len(cov) < 12 {
		t.Fatalf("expected ≥12 beds, got %d", len(cov))
	}
	tiers := map[BedTier]int{}
	seen := map[string]bool{}
	for _, b := range cov {
		if b.Bed == "" || len(b.Kinds) == 0 {
			t.Fatalf("invalid probe: %+v", b)
		}
		if seen[b.Bed] {
			t.Fatalf("duplicate bed %s", b.Bed)
		}
		seen[b.Bed] = true
		tiers[b.Tier]++
	}
	if tiers[BedTierStrong] < 2 || tiers[BedTierMedium] < 2 || tiers[BedTierWeak] < 1 {
		t.Fatalf("tier balance weak: %+v", tiers)
	}
}
