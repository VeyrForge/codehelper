package main

import (
	"strings"
	"testing"
)

func TestRootLongHelp_containsCriticalGuidance(t *testing.T) {
	t.Parallel()
	for _, sub := range []string{
		"codehelper help",
		"codehelper help tools",
		"codehelper help lookup",
		"codehelper update --force-analyze",
		"codehelper init",
		"project_context",
	} {
		if !strings.Contains(rootLongHelp, sub) {
			t.Errorf("rootLongHelp missing %q", sub)
		}
	}
}

func TestSubcommandLongHelp_nonEmpty(t *testing.T) {
	t.Parallel()
	for name, text := range map[string]string{
		"eval":       evalLongHelp,
		"model-eval": modelEvalLongHelp,
		"doctor":     doctorLongHelp,
		"agent":      agentLongHelp,
		"status":     statusLongHelp,
		"mcp":        mcpLongHelp,
	} {
		if strings.TrimSpace(text) == "" {
			t.Errorf("%s Long help is empty", name)
		}
	}
}
