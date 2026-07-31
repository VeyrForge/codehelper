package mcpsvc

import "testing"

// TestAutoIndexCooldown verifies the loop-prevention: the first auto-index for a
// root is allowed, an immediate retry is denied (so an unindexable repo can't
// trigger a re-index on every tool call), and a different root is independent.
func TestAutoIndexCooldown(t *testing.T) {
	rootA := "/tmp/autoindex-cooldown-a"
	rootB := "/tmp/autoindex-cooldown-b"
	t.Cleanup(func() {
		autoIndexCooldowns.Delete(rootA)
		autoIndexCooldowns.Delete(rootB)
	})

	if !autoIndexAllowed(rootA) {
		t.Fatal("first attempt for rootA should be allowed")
	}
	if autoIndexAllowed(rootA) {
		t.Error("immediate second attempt for rootA should be denied by cooldown")
	}
	if !autoIndexAllowed(rootB) {
		t.Error("a different root must not be affected by rootA's cooldown")
	}
}
