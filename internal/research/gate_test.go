package research

import (
	"testing"

	"github.com/VeyrForge/codehelper/internal/patterns"
)

func TestShouldResearch_authTopic(t *testing.T) {
	if !ShouldResearch("add oauth login", nil, patterns.ExpandOutput{}) {
		t.Fatal("expected research for auth topic")
	}
}

func TestNetworkEnabled_missingFile(t *testing.T) {
	if NetworkEnabled("/nonexistent/path") {
		t.Fatal("expected false without learning.json")
	}
}
