package orchestrator

import (
	"encoding/json"
	"strings"

	"github.com/VeyrForge/codehelper/internal/toon"
)

// AgentFacingTokensFormat estimates cloud-facing tokens for the slim orchestrate payload.
func AgentFacingTokensFormat(res *Result, format string) int {
	if res == nil {
		return 0
	}
	payload := res.AgentPayload(false)
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "toon" {
		if t, err := toon.Marshal(payload); err == nil && t != "" {
			return estimateTokens(len(t))
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return estimateTokens(len(res.AgentBrief))
	}
	return estimateTokens(len(b))
}
