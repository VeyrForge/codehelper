package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/internal/llm"
)

// userQuestionCore strips host-appended XML so orchestration heuristics only
// see the user's words.
func userQuestionCore(enrichedUserText string) string {
	s := enrichedUserText
	for _, tag := range []string{"<workspace_folder>", "<user_attached_paths>", "<workflow_mode>", "<llm_model>"} {
		if i := strings.Index(s, tag); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

func hasUserAttachedPaths(enrichedUserText string) bool {
	return strings.Contains(enrichedUserText, "<user_attached_paths>")
}

var whitespaceRe = regexp.MustCompile(`\s+`)
var socialGreetingRe = regexp.MustCompile(`(?i)^(hi|hello|hey|hiya|yo|thanks|thank you|thx|ty|ok+|okay|bye|goodbye|cya)\b`)
var socialPingRe = regexp.MustCompile(`(?i)^(are you there|ping|pong|testing|test)\b`)

// isPlainSocialOrOffTopic detects greetings / tiny messages — never force
// MCP exploration for those.
func isPlainSocialOrOffTopic(core string) bool {
	t := strings.TrimSpace(whitespaceRe.ReplaceAllString(core, " "))
	if len(t) == 0 || len(t) <= 2 {
		return true
	}
	if socialGreetingRe.MatchString(t) && len(t) <= 48 {
		return true
	}
	if socialPingRe.MatchString(t) && len(t) <= 40 {
		return true
	}
	return false
}

func ollamaErrorTextReply(msg llm.Message) bool {
	t := strings.TrimSpace(msg.Text())
	if t == "" {
		return false
	}
	lower := strings.ToLower(t)
	return strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "error ")
}

var codeExtRe = regexp.MustCompile(`(?i)\.(go|ts|js|tsx|jsx|py|rs|java|cs|cpp|c|h)\b`)
var atMentionRe = regexp.MustCompile(`@\S`)
var workspaceNounRes = []*regexp.Regexp{
	regexp.MustCompile(`\b(this|the|my)\s+(repo|codebase|project|workspace|code|module|package)\b`),
	regexp.MustCompile(`\b(our|this)\s+codebase\b`),
	regexp.MustCompile(`\b(in|into|from|throughout)\s+(the|this)\s+(repo|codebase|project|code)\b`),
	regexp.MustCompile(`\bhow\s+(does|do|is|are|can|should|would|will)\b`),
	regexp.MustCompile(`\b(explain|describe|walk|debug|fix|implement|refactor|review|trace|overview|architecture|structure)\b`),
	regexp.MustCompile(`\b(symbol|function|class|method|file|handler|endpoint|mcp|indexer|graph|lint|build|test)\b`),
}
var whereWhyRe = regexp.MustCompile(`\b(where|why)\b`)
var isAreDoRe = regexp.MustCompile(`\b(is|are|do|does|can)\b`)

// mentionsCodeOrWorkspaceIntent decides when the host may inject
// tool-continuation nudges.
func mentionsCodeOrWorkspaceIntent(core string) bool {
	if strings.TrimSpace(core) == "" {
		return false
	}
	c := strings.ToLower(core)
	if strings.Contains(core, "`") || atMentionRe.MatchString(core) {
		return true
	}
	if codeExtRe.MatchString(core) {
		return true
	}
	for _, re := range workspaceNounRes {
		if re.MatchString(c) {
			return true
		}
	}
	if whereWhyRe.MatchString(c) && isAreDoRe.MatchString(c) {
		return true
	}
	return false
}

var broadAskRes = []*regexp.Regexp{
	regexp.MustCompile(`\b(overview|architecture|structure|tech\s*stack|big\s+picture|high[\s-]?level|landscape|layout)\b`),
	regexp.MustCompile(`\b(whole\s+repo|entire\s+codebase|this\s+codebase|this\s+project|this\s+repo)\b`),
	regexp.MustCompile(`\bwhat(?:'s|\s+is|\s+does)\s+(this|the)\s+(project|repo|codebase)\b`),
	regexp.MustCompile(`\btell\s+me\s+about\b`),
	regexp.MustCompile(`\b(project|repo|codebase)\s+(about|for|do|does)\b`),
	regexp.MustCompile(`\b(purpose|goal|scope)\s+of\s+(this|the|our)\s+(project|repo|codebase)\b`),
	regexp.MustCompile(`(?s)\b(summarize|sum\s+up|sketch|map\s+out)\b.*\b(project|repo|codebase|code)\b`),
}

// isProbablyBroadExplorationAsk keeps expensive breadth nudges off narrow asks.
func isProbablyBroadExplorationAsk(core string) bool {
	c := strings.ToLower(core)
	for _, re := range broadAskRes {
		if re.MatchString(c) {
			return true
		}
	}
	return false
}

// groundingDepthTools prove the model looked past registry metadata.
var groundingDepthTools = map[string]bool{
	"query":                    true,
	"context":                  true,
	"read_workspace_file":      true,
	"list_workspace_directory": true,
}

func hasGroundingDepth(executedToolNames map[string]bool) bool {
	for n := range executedToolNames {
		if groundingDepthTools[n] {
			return true
		}
	}
	return false
}

var readmeBaseRe = regexp.MustCompile(`^readme`)
var proseExtRe = regexp.MustCompile(`\.(md|rst|txt)$`)
var implementationExtRe = regexp.MustCompile(`\.(go|rs|java|py|tsx?|jsx?|vue|cs|swift|kt|zig|c|h|cpp|hpp)$`)
var codeDirShapeRe = regexp.MustCompile(`(^|/)internal/|(^|/)cmd/|(^|/)src/|(^|/)pkg/|(^|/)lib/`)
var dataExtRe = regexp.MustCompile(`(?i)\.(graphql|yaml|yml)$`)
var licenseLikeRe = regexp.MustCompile(`(?i)^(license|contributing|changelog)`)

// countsAsImplementationRead reports whether a read_workspace_file looks like
// implementation code, not prose-only docs or manifests.
func countsAsImplementationRead(pathRaw string) bool {
	norm := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(pathRaw, `\`, "/")))
	if norm == "" || norm == "." || norm == ".." {
		return false
	}
	base := norm
	if i := strings.LastIndex(norm, "/"); i >= 0 {
		base = norm[i+1:]
	}
	if readmeBaseRe.MatchString(base) {
		return false
	}
	if proseExtRe.MatchString(base) {
		return false
	}
	switch base {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", ".gitignore":
		return false
	}
	if licenseLikeRe.MatchString(base) {
		return false
	}
	if implementationExtRe.MatchString(base) {
		return true
	}
	if codeDirShapeRe.MatchString(norm) {
		return !dataExtRe.MatchString(base)
	}
	return false
}

// peekWorkspaceReadPath extracts the path arg before normalization aliases.
func peekWorkspaceReadPath(args map[string]any) string {
	for _, k := range []string{"path", "file_path", "filepath", "relative_path", "file"} {
		if s, isStr := args[k].(string); isStr && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// retrievalDedupeKey signatures successful query/context_pack calls to block
// identical repeats.
func retrievalDedupeKey(name string, norm map[string]any) string {
	switch name {
	case "query":
		limit := 0.0
		if f, isF := norm["limit"].(float64); isF {
			limit = f
		}
		pack := false
		if b, isB := norm["include_context_pack"].(bool); isB {
			pack = b
		}
		return fmt.Sprintf("query::%s::%s::%v::%v",
			strings.ToLower(stringish(norm["query"])),
			strings.ToLower(stringish(norm["repo"])),
			pack, limit)
	}
	return ""
}

// listWorkspaceDedupeKey blocks useless repeat directory listings.
func listWorkspaceDedupeKey(args map[string]any) string {
	n := normalizeToolArguments("list_workspace_directory", args)
	repo := strings.ToLower(stringish(n["repo"]))
	p := strings.ReplaceAll(stringish(n["path"]), `\`, "/")
	if p == "" || repoRootPathRe.MatchString(p) {
		p = "."
	}
	return repo + "::" + p
}

// maxBreadthNudges stops breadth injection before models that never call
// read_workspace_file spin until max rounds.
const maxBreadthNudges = 4

// overviewBreadthMet decides when a broad-overview answer has enough evidence.
func overviewBreadthMet(counts map[string]int, workspaceToolsAvailable bool, implementationReads, breadthNudgesDone int) bool {
	if breadthNudgesDone >= maxBreadthNudges {
		return true
	}

	q := counts["query"]
	cp := counts["context_pack"]
	rf := counts["read_workspace_file"]
	ls := counts["list_workspace_directory"]
	ctx := counts["context"]

	if !workspaceToolsAvailable {
		return q >= 4 && cp >= 2 && (ctx >= 1 || cp >= 3 || q >= 5)
	}

	hasCodeEvidence := implementationReads >= 2 || breadthNudgesDone >= 8
	// Require real source reads so answers are not README copy-paste.
	strict := q >= 3 && cp >= 1 && ls >= 1 && rf >= 2 && hasCodeEvidence &&
		(cp >= 2 || ctx >= 1 || q >= 4 || rf >= 3 || implementationReads >= 3)
	if strict {
		return true
	}

	// Escape hatch: enough index + layout + at least one code read +
	// second-line search — avoid infinite list_workspace_directory loops.
	loose := breadthNudgesDone >= 2 && cp >= 1 && ls >= 1 && q >= 2 && rf >= 1 &&
		implementationReads >= 1 && (ctx >= 1 || q >= 3 || cp >= 2)
	return loose
}

var qwenModelRe = regexp.MustCompile(`(?i)\bqwen\b`)

func isLikelyQwenModel(model string) bool {
	return qwenModelRe.MatchString(model)
}

// WorkspaceEditEvent describes a successful write/patch so clients can render
// a Keep/Undo proposal.
type WorkspaceEditEvent struct {
	Tool         string `json:"tool"`
	Path         string `json:"path"`
	RepoRoot     string `json:"repo_root"`
	Diff         string `json:"diff"`
	DiffElided   bool   `json:"diff_elided"`
	RevertToken  string `json:"revert_token"`
	Created      bool   `json:"created"`
	BytesBefore  *int64 `json:"bytes_before,omitempty"`
	BytesAfter   *int64 `json:"bytes_after,omitempty"`
	HunksApplied *int64 `json:"hunks_applied,omitempty"`
}

// parseWorkspaceEditResult decodes the JSON envelope returned by write/patch
// tools so clients can render Keep/Undo bubbles.
func parseWorkspaceEditResult(tool, raw string) *WorkspaceEditEvent {
	if raw == "" {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
		return nil
	}
	path, _ := obj["path"].(string)
	if path == "" {
		return nil
	}
	repoRoot, _ := obj["repo_root"].(string)
	diff, _ := obj["diff"].(string)
	revertToken, _ := obj["revert_token"].(string)
	created, _ := obj["created"].(bool)
	diffElided, _ := obj["diff_elided"].(bool)
	if diff == "" && revertToken == "" && tool == "apply_patch_workspace_file" {
		return nil
	}
	ev := &WorkspaceEditEvent{
		Tool:        tool,
		Path:        path,
		RepoRoot:    repoRoot,
		Diff:        diff,
		DiffElided:  diffElided,
		RevertToken: revertToken,
		Created:     created,
	}
	if f, isF := obj["bytes_before"].(float64); isF {
		v := int64(f)
		ev.BytesBefore = &v
	}
	if f, isF := obj["bytes_after"].(float64); isF {
		v := int64(f)
		ev.BytesAfter = &v
	} else if f, isF := obj["bytes_written"].(float64); isF {
		v := int64(f)
		ev.BytesAfter = &v
	}
	if f, isF := obj["hunks_applied"].(float64); isF {
		v := int64(f)
		ev.HunksApplied = &v
	}
	return ev
}
