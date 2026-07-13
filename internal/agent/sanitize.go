package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/llm"
)

var embeddedParamTypePlaceholders = map[string]bool{
	"string": true, "number": true, "boolean": true, "object": true, "array": true, "null": true,
}

// extractBalancedJSONObjects splits top-level `{ ... }` spans (strings may
// contain `{` — heuristic only).
func extractBalancedJSONObjects(text string) []string {
	var results []string
	depth := 0
	start := -1
	for i, c := range []byte(text) {
		switch c {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				results = append(results, text[start:i+1])
				start = -1
			}
		}
	}
	return results
}

func parseJSONObjectCandidate(raw string) map[string]any {
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}
	return obj
}

func normalizeEmbeddedParams(value any) map[string]any {
	v := value
	if s, isStr := v.(string); isStr {
		t := strings.TrimSpace(s)
		if strings.HasPrefix(t, "{") {
			if parsed := parseJSONObjectCandidate(t); parsed != nil {
				v = parsed
			}
		}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	out := map[string]any{}
	for k, raw := range obj {
		if s, isStr := raw.(string); isStr {
			if embeddedParamTypePlaceholders[strings.ToLower(strings.TrimSpace(s))] {
				continue
			}
			out[k] = s
			continue
		}
		if rec, isObj := raw.(map[string]any); isObj {
			typeV := ""
			if tv, isStr := rec["type"].(string); isStr {
				typeV = strings.ToLower(strings.TrimSpace(tv))
			}
			if typeV != "" && embeddedParamTypePlaceholders[typeV] {
				_, hasDefault := rec["default"]
				_, hasValue := rec["value"]
				_, hasExample := rec["example"]
				if !hasDefault && !hasValue && !hasExample {
					continue
				}
			}
		}
		out[k] = raw
	}
	return out
}

type embeddedTool struct {
	name   string
	params map[string]any
}

func embeddedToolFromObject(obj map[string]any, allowed map[string]bool) *embeddedTool {
	var fnRec map[string]any
	if fn, isObj := obj["function"].(map[string]any); isObj {
		fnRec = fn
	}
	rawName := ""
	for _, k := range []string{"name", "function_name", "functionName", "tool", "tool_name"} {
		if s, isStr := obj[k].(string); isStr && s != "" {
			rawName = s
			break
		}
	}
	if rawName == "" && fnRec != nil {
		if s, isStr := fnRec["name"].(string); isStr {
			rawName = s
		}
	}
	if strings.TrimSpace(rawName) == "" {
		return nil
	}
	canonical := rawName
	if !allowed[rawName] {
		canonical = aliasToolName(rawName, allowed)
	}
	if canonical == "" {
		return nil
	}
	var paramsRaw any
	for _, k := range []string{"parameters", "arguments", "args", "input", "params"} {
		if v, ok := obj[k]; ok && v != nil {
			paramsRaw = v
			break
		}
	}
	if paramsRaw == nil && fnRec != nil {
		for _, k := range []string{"arguments", "parameters"} {
			if v, ok := fnRec[k]; ok && v != nil {
				paramsRaw = v
				break
			}
		}
	}
	return &embeddedTool{name: canonical, params: normalizeEmbeddedParams(paramsRaw)}
}

var fencedJSONRe = regexp.MustCompile("(?is)```(?:json)?\\s*(.*?)```")
var wholeFencedJSONRe = regexp.MustCompile("(?is)^```(?:json)?\\s*(.*?)```\\s*$")

func makeEmbeddedToolCallID(index int) string {
	return fmt.Sprintf("embedded_%s_%d", time.Now().UTC().Format("20060102t150405"), index)
}

// parseEmbeddedToolRequests recovers tool calls some OpenAI-compatible
// servers emit as plain `{"name":"…","parameters":{…}}` assistant text.
func parseEmbeddedToolRequests(content string, allowed map[string]bool) []llm.ToolCall {
	var out []llm.ToolCall
	var candidates []string

	for _, m := range fencedJSONRe.FindAllStringSubmatch(content, -1) {
		candidates = append(candidates, strings.TrimSpace(m[1]))
	}
	if trimmed := strings.TrimSpace(content); strings.HasPrefix(trimmed, "{") {
		candidates = append(candidates, trimmed)
	}
	candidates = append(candidates, extractBalancedJSONObjects(content)...)

	dedupe := map[string]bool{}
	ix := 0
	for _, raw := range candidates {
		obj := parseJSONObjectCandidate(raw)
		if obj == nil {
			continue
		}
		embedded := embeddedToolFromObject(obj, allowed)
		if embedded == nil {
			continue
		}
		paramsJSON, err := json.Marshal(embedded.params)
		if err != nil {
			continue
		}
		sig := embedded.name + ":" + string(paramsJSON)
		if dedupe[sig] {
			continue
		}
		dedupe[sig] = true
		out = append(out, llm.ToolCall{
			ID:   makeEmbeddedToolCallID(ix),
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      embedded.name,
				Arguments: string(paramsJSON),
			},
		})
		ix++
	}
	return out
}

// stripEmbeddedToolJSONBlocks removes fenced tool-call JSON from assistant
// text so history stays readable.
func stripEmbeddedToolJSONBlocks(content string, allowed map[string]bool) string {
	return fencedJSONRe.ReplaceAllStringFunc(content, func(full string) string {
		m := fencedJSONRe.FindStringSubmatch(full)
		if m == nil {
			return full
		}
		obj := parseJSONObjectCandidate(strings.TrimSpace(m[1]))
		if obj == nil {
			return full
		}
		if embeddedToolFromObject(obj, allowed) != nil {
			return ""
		}
		return full
	})
}

// unwrapAssistantJSONEnvelope unwraps `{"response":"…"}` style envelopes
// (and optional ```json fences) some local models wrap replies in.
func unwrapAssistantJSONEnvelope(raw string) string {
	s := strings.TrimSpace(raw)
	for i := 0; i < 3; i++ {
		candidate := s
		if m := wholeFencedJSONRe.FindStringSubmatch(candidate); m != nil {
			candidate = strings.TrimSpace(m[1])
		}
		if !strings.HasPrefix(candidate, "{") {
			break
		}
		obj := parseJSONObjectCandidate(candidate)
		if obj == nil {
			break
		}
		next := ""
		for _, k := range []string{"response", "answer", "reply", "message", "content", "text", "output"} {
			if v, isStr := obj[k].(string); isStr && strings.TrimSpace(v) != "" {
				next = strings.TrimSpace(v)
				break
			}
		}
		if next == "" {
			break
		}
		s = next
	}
	return s
}

func formatAssistantReplyForUser(raw string, allowedTools map[string]bool) string {
	stripped := strings.TrimSpace(stripEmbeddedToolJSONBlocks(raw, allowedTools))
	return unwrapAssistantJSONEnvelope(stripped)
}

var internalControlRes = []*regexp.Regexp{
	regexp.MustCompile(`duplicate_(directory_listing|retrieval_call)`),
	regexp.MustCompile(`already listed successfully`),
	regexp.MustCompile(`duplicate call to (query|context_pack|list_workspace_directory)`),
	regexp.MustCompile(`synthesize(?:\s+information)?\s+from\s+prior\s+results`),
	regexp.MustCompile(`search(?:ing)?\s+with\s+distinct\s+terms`),
	regexp.MustCompile(`internal_control`),
	regexp.MustCompile(`user_visible\s*:\s*false`),
	regexp.MustCompile(`re-read the prior tool json`),
	regexp.MustCompile(`avoid listing the same directory`),
	regexp.MustCompile(`do not list it again`),
}

func looksLikeInternalControlOnly(s string) bool {
	t := strings.ToLower(s)
	for _, re := range internalControlRes {
		if re.MatchString(t) {
			return true
		}
	}
	return false
}

var nonAnswerCommentaryRes = []*regexp.Regexp{
	regexp.MustCompile(`\bduplicate\s+call\s+to\s+(query|context_pack|list_workspace_directory)\b`),
	regexp.MustCompile(`\bsynthesiz\w+\s+information\s+from\s+prior\s+results\b`),
	regexp.MustCompile(`\bsearch(?:ing)?\s+with\s+distinct\s+terms\b`),
	regexp.MustCompile(`\bthe provided (json|tool response|data|hits)\b`),
	regexp.MustCompile(`\b(this|the)\s+json(\s+data)?\s+(includes|contains)\b`),
	regexp.MustCompile(`\blist of code symbols\b`),
	regexp.MustCompile(`\bthe freshness section\b`),
}

var apologizeRe = regexp.MustCompile(`\b(i|we)\s+apologiz\w*\b`)
var duplicateCallRe = regexp.MustCompile(`\bduplicate\s+call\b`)
var ifYouNeedRe = regexp.MustCompile(`\bif you need (more|further|additional)\b`)
var letMeKnowRe = regexp.MustCompile(`\b(let me know|please)\b`)

// looksLikeNonAnswerToolCommentary flags drafts that comment on tool JSON
// instead of answering the user.
func looksLikeNonAnswerToolCommentary(s string) bool {
	t := strings.ToLower(s)
	if apologizeRe.MatchString(t) && duplicateCallRe.MatchString(t) {
		return true
	}
	for _, re := range nonAnswerCommentaryRes {
		if re.MatchString(t) {
			return true
		}
	}
	if ifYouNeedRe.MatchString(t) && letMeKnowRe.MatchString(t) {
		return true
	}
	return false
}

var userMustRunToolsRes = []*regexp.Regexp{
	regexp.MustCompile(`\b(run|execute)\s+these\s+queries\b`),
	regexp.MustCompile(`\bhere\s+are\s+my\s+(first|next)\s+set\s+of\s+queries\b`),
}
var pleaseRunRe = regexp.MustCompile(`\bplease\s+(run|execute|call|invoke)\b`)
var toolNounsRe = regexp.MustCompile(`\b(query|queries|context_pack|mcp|tool|tools)\b`)
var provideResultsRe = regexp.MustCompile(`\bprovide\s+the\s+results\b`)
var toolWordRe = regexp.MustCompile(`\b(query|queries|tool|tools)\b`)

// looksLikeUserMustRunToolsInstruction flags replies that delegate MCP
// execution to the user.
func looksLikeUserMustRunToolsInstruction(s string) bool {
	t := strings.ToLower(s)
	if pleaseRunRe.MatchString(t) && toolNounsRe.MatchString(t) {
		return true
	}
	for _, re := range userMustRunToolsRes {
		if re.MatchString(t) {
			return true
		}
	}
	if provideResultsRe.MatchString(t) && toolWordRe.MatchString(t) {
		return true
	}
	return false
}

var listRootsNameRe = regexp.MustCompile(`(?i)list\s*roots`)
var listsRootsDescRe = regexp.MustCompile(`lists the roots of a repository`)

func looksLikeToolSchemaLeak(s string) bool {
	candidate := strings.TrimSpace(s)
	if m := wholeFencedJSONRe.FindStringSubmatch(candidate); m != nil {
		candidate = strings.TrimSpace(m[1])
	}
	obj := parseJSONObjectCandidate(candidate)
	if obj == nil {
		return false
	}
	fnName := ""
	for _, k := range []string{"function_name", "functionName", "name"} {
		if v, isStr := obj[k].(string); isStr && v != "" {
			fnName = v
			break
		}
	}
	desc := ""
	if v, isStr := obj["description"].(string); isStr {
		desc = strings.ToLower(v)
	}
	paramLooksTyped := false
	if params, isObj := obj["parameters"].(map[string]any); isObj {
		for _, v := range params {
			if s, isStr := v.(string); isStr {
				if embeddedParamTypePlaceholders[strings.ToLower(strings.TrimSpace(s))] {
					paramLooksTyped = true
					break
				}
				continue
			}
			if rec, isRec := v.(map[string]any); isRec {
				if tp, isStr := rec["type"].(string); isStr && embeddedParamTypePlaceholders[strings.ToLower(strings.TrimSpace(tp))] {
					paramLooksTyped = true
					break
				}
			}
		}
	}
	if paramLooksTyped && fnName != "" {
		return true
	}
	if listRootsNameRe.MatchString(fnName) || listsRootsDescRe.MatchString(desc) {
		return true
	}
	return false
}

// looksLikeBadUserFacingAnswer guards anything that must never be the user's
// visible answer.
func looksLikeBadUserFacingAnswer(s string) bool {
	t := strings.TrimSpace(s)
	return t == "" ||
		looksLikeToolSchemaLeak(t) ||
		looksLikeNonAnswerToolCommentary(t) ||
		looksLikeDeferredWorkAnswer(t) ||
		looksLikeUserMustRunToolsInstruction(t) ||
		looksLikeInternalControlOnly(t)
}

var backtickPathRe = regexp.MustCompile("(?i)`[^`\n]*(?:/|\\\\)[^`\n]*\\.[a-z0-9]+(?::\\d+)?`")
var plainPathRe = regexp.MustCompile(`(?:^|[\s(])(?:\.{0,2}/)?(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+\.[A-Za-z0-9]+(?::\d+)?(?:[\s).,;:]|$)`)
var absPathRe = regexp.MustCompile(`(?:^|[\s(])/(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+\.[A-Za-z0-9]+(?::\d+)?(?:[\s).,;:]|$)`)

// hasPathCitation reports whether the answer contains at least one repo path
// citation.
func hasPathCitation(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	return backtickPathRe.MatchString(t) || plainPathRe.MatchString(t) || absPathRe.MatchString(t)
}

var stepNOfMRe = regexp.MustCompile(`(?i)\bstep\s+\d+\s*/\s*\d+\b`)
var deferredWorkRes = []*regexp.Regexp{
	regexp.MustCompile(`\b(i'?ll|i\s+will|we'?ll|we\s+will)\s+(start|begin)\s+(by\s+)?(using|calling|running|invoking|listing|checking|querying|reading|opening)\b`),
	regexp.MustCompile(`(?s)\b(i'?ll|i\s+will|we'?ll|we\s+will)\b.{0,80}\b(by\s+)?(using|calling|running|invoking|listing|checking|querying|reading|opening)\b`),
	regexp.MustCompile(`\b(next|now)\s+(i'?ll|i\s+will|we'?ll|we\s+will)\s+(use|call|run|invoke|start|open|try|check|query|read|list)\b`),
	regexp.MustCompile(`\blet'?s\s+(start|begin|look|try)\b`),
	regexp.MustCompile(`\bonce\s+we\s+(have|get|obtain)\b`),
	regexp.MustCompile(`\bi\s+will\s+(now\s+)?(use|call|run|invoke|perform)\b`),
}
var proceedToRe = regexp.MustCompile(`\bproceed\s+to\b`)
var thenNextRe = regexp.MustCompile(`\b(then|next|after|will)\b`)
var fencedToolCallStartRe = regexp.MustCompile("```(?:json)?\\s*\\{\\s*\"name\"\\s*:")

// looksLikeDeferredWorkAnswer flags meta-planning replies instead of tools
// or a finished answer.
func looksLikeDeferredWorkAnswer(assistantText string) bool {
	t := strings.ToLower(assistantText)
	if stepNOfMRe.MatchString(assistantText) {
		return true
	}
	for _, re := range deferredWorkRes {
		if re.MatchString(t) {
			return true
		}
	}
	if proceedToRe.MatchString(t) && thenNextRe.MatchString(t) {
		return true
	}
	if fencedToolCallStartRe.MatchString(assistantText) {
		return true
	}
	if looksLikeUserMustRunToolsInstruction(assistantText) {
		return true
	}
	return false
}

// mcpToolOutputSucceeded reports usable tool output (not missing tool, not a
// JSON error payload).
var toolNotFoundRe = regexp.MustCompile(`(?i)tool ['"]?[\w_]+['"]? not found|not found: tool|MCP error.*not found`)

func mcpToolOutputSucceeded(text string) bool {
	s := strings.TrimSpace(text)
	if toolNotFoundRe.MatchString(s) {
		return false
	}
	var j map[string]any
	if err := json.Unmarshal([]byte(s), &j); err == nil && j != nil {
		if isErr, _ := j["isError"].(bool); isErr {
			return false
		}
		if v, ok := j["error"]; ok && v != nil && fmt.Sprint(v) != "" {
			return false
		}
	}
	return true
}
