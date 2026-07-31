// Package questiongate decides whether proposed user questions are worth asking.
package questiongate

import (
	"strings"

	"github.com/VeyrForge/codehelper/internal/profile"
)

// Input matches MCP question_gate shape.
type Input struct {
	Task              string   `json:"task"`
	KnownContext      []string `json:"known_context"`
	ProposedQuestions []string `json:"proposed_questions"`
}

// Output decides whether to bother the user.
type Output struct {
	AskUser  bool   `json:"ask_user"`
	Reason   string `json:"reason"`
	ToolNext string `json:"tool_next,omitempty"`
}

var criticalTopics = []string{
	"security", "auth", "session", "payment", "order", "pii", "credential",
	"migration", "schema", "destructive", "public api", "contract", "deployment",
	"breaking", "database", "encryption",
}

// Evaluate returns ask_user=false when questions are low-value or inferable.
func Evaluate(in Input, prof *profile.ProjectProfile) Output {
	if len(in.ProposedQuestions) == 0 {
		return Output{AskUser: false, Reason: "no proposed questions", ToolNext: "context"}
	}
	if prof != nil && prof.ProjectType != "" && prof.ProjectType != "unknown" {
		frameworkish := false
		for _, q := range in.ProposedQuestions {
			lq := strings.ToLower(q)
			if strings.Contains(lq, "framework") || strings.Contains(lq, "stack") || strings.Contains(lq, "what language") {
				frameworkish = true
				break
			}
		}
		if frameworkish {
			return Output{
				AskUser:  false,
				Reason:   "framework/stack inferable from project_profile (" + prof.ProjectType + ")",
				ToolNext: "context",
			}
		}
	}

	for _, q := range in.ProposedQuestions {
		lq := strings.ToLower(strings.TrimSpace(q))
		for _, t := range criticalTopics {
			if strings.Contains(lq, t) {
				return Output{
					AskUser:  true,
					Reason:   "question may change security/data/contract posture: " + q,
					ToolNext: "impact",
				}
			}
		}
	}

	return Output{
		AskUser:  false,
		Reason:   "proposed questions are non-blocking; resolve via context/impact first",
		ToolNext: "context",
	}
}
