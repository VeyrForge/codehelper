package agent

import "strings"

// EnrichInput carries client metadata merged into the user message before the loop.
type EnrichInput struct {
	Text        string
	Workspace   string
	Attachments []string
	ModelHint   string
}

// EnrichUserText appends workspace scope, @-mention paths, and optional model hints.
// Clients should send raw user text plus metadata instead of shaping prompts locally.
func EnrichUserText(in EnrichInput) string {
	body := strings.TrimSpace(in.Text)
	root := strings.TrimSpace(in.Workspace)
	if root == "" {
		root = strings.TrimSpace(in.Workspace)
	}
	if root != "" {
		safe := strings.ReplaceAll(root, `\`, "/")
		body = body + "\n\n<workspace_folder>" + safe + "</workspace_folder>"
	}
	if len(in.Attachments) > 0 {
		var lines strings.Builder
		for _, p := range in.Attachments {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			lines.WriteString("  <path>")
			lines.WriteString(p)
			lines.WriteString("</path>\n")
		}
		if lines.Len() > 0 {
			body = body + "\n\n<user_attached_paths>\n" +
				"  <!-- The user explicitly @-mentioned or attached these paths. They are the primary\n" +
				"       subject of the question. Call read_workspace_file on each one before answering\n" +
				"       and quote real content; do NOT describe what such a file usually contains. -->\n" +
				lines.String() +
				"</user_attached_paths>"
		}
	}
	if hint := strings.TrimSpace(in.ModelHint); hint != "" {
		body = body + "\n\n<llm_model>" + hint + "</llm_model>"
	}
	return body
}
