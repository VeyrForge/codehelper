package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/VeyrForge/codehelper/internal/llm"
)

func TestBlocksDuplicateCallLeakagePhrasingAsInternalControl(t *testing.T) {
	s := "I apologize for the duplicate call to context_pack. Let's proceed with synthesizing information from prior results or searching with distinct terms."
	if !looksLikeInternalControlOnly(s) {
		t.Error("looksLikeInternalControlOnly = false")
	}
	if !looksLikeNonAnswerToolCommentary(s) {
		t.Error("looksLikeNonAnswerToolCommentary = false")
	}
	if !looksLikeBadUserFacingAnswer(s) {
		t.Error("looksLikeBadUserFacingAnswer = false")
	}
}

func TestBlocksUserDelegatedToolExecutionInstructions(t *testing.T) {
	s := "Please run these queries and send me the results."
	if !looksLikeUserMustRunToolsInstruction(s) {
		t.Error("looksLikeUserMustRunToolsInstruction = false")
	}
	if !looksLikeBadUserFacingAnswer(s) {
		t.Error("looksLikeBadUserFacingAnswer = false")
	}
}

func TestKeepsNormalMarkdownAnswersAsUserFacing(t *testing.T) {
	s := "This project provides MCP indexing and retrieval. Start in `cmd/codehelper/main.go` and `internal/mcpsvc/register.go`."
	if looksLikeBadUserFacingAnswer(s) {
		t.Error("looksLikeBadUserFacingAnswer = true for normal answer")
	}
	if !hasPathCitation(s) {
		t.Error("hasPathCitation = false")
	}
}

func TestStripsEmbeddedToolCallJSONFromVisibleReply(t *testing.T) {
	raw := "```json\n{\"name\":\"query\",\"arguments\":{\"query\":\"register\"}}\n```\n\nFinal answer text."
	cleaned := formatAssistantReplyForUser(raw, map[string]bool{"query": true})
	if cleaned != "Final answer text." {
		t.Errorf("cleaned = %q", cleaned)
	}
}

func TestUnwrapAssistantJSONEnvelope(t *testing.T) {
	if got := unwrapAssistantJSONEnvelope(`{"response":"Hello **world**"}`); got != "Hello **world**" {
		t.Errorf("got %q", got)
	}
	if got := unwrapAssistantJSONEnvelope("plain text"); got != "plain text" {
		t.Errorf("got %q", got)
	}
}

func TestParseEmbeddedToolRequests(t *testing.T) {
	allowed := allowedToolsForMode(ModeAsk)
	calls := parseEmbeddedToolRequests("```json\n{\"name\":\"read_file\",\"parameters\":{\"path\":\"go.mod\"}}\n```", allowed)
	if len(calls) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	if calls[0].Function.Name != "read_workspace_file" {
		t.Errorf("aliased name = %q", calls[0].Function.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatal(err)
	}
	if args["path"] != "go.mod" {
		t.Errorf("args = %v", args)
	}
}

func TestAliasToolName(t *testing.T) {
	allowed := allowedToolsForMode(ModeAgent)
	cases := map[string]string{
		"list_files":         "list_workspace_directory",
		"call:read_file":     "read_workspace_file",
		"Search":             "query",
		"edit_file":          "apply_patch_workspace_file",
		"list_workspace_dir": "list_workspace_directory",
		"nonsense_zzz":       "",
	}
	for in, want := range cases {
		if got := aliasToolName(in, allowed); got != want {
			t.Errorf("aliasToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolsForModeGatesWrites(t *testing.T) {
	askTools := toolsForMode(ModeAsk)
	for _, tool := range askTools {
		name := toolSchemaName(tool.(map[string]any))
		if writeToolNames[name] {
			t.Errorf("ask mode advertises write tool %q", name)
		}
	}
	if len(askTools) != len(allToolNames)-len(writeToolNames) {
		t.Errorf("ask tools = %d", len(askTools))
	}
	if got := len(toolsForMode(ModeAgent)); got != len(allToolNames) {
		t.Errorf("agent tools = %d, want %d", got, len(allToolNames))
	}
}

func TestNormalizeToolArguments(t *testing.T) {
	out := normalizeToolArguments("query", map[string]any{"q": "register handler", "repo": `"codehelper"`})
	if out["query"] != "register handler" {
		t.Errorf("query = %v", out["query"])
	}
	if _, hasQ := out["q"]; hasQ {
		t.Error("q alias not removed")
	}
	if out["repo"] != "codehelper" {
		t.Errorf("repo = %v", out["repo"])
	}

	out = normalizeToolArguments("query", map[string]any{"repo": "<repository_name>"})
	if _, hasRepo := out["repo"]; hasRepo {
		t.Error("placeholder repo not dropped")
	}
	if out["query"] != queryFallbackText {
		t.Errorf("fallback query = %v", out["query"])
	}

	out = normalizeToolArguments("read_workspace_file", map[string]any{"file_path": "/internal/foo.go", "isError": true})
	if out["path"] != "internal/foo.go" {
		t.Errorf("path = %v", out["path"])
	}
	if _, hasJunk := out["isError"]; hasJunk {
		t.Error("junk key survived sanitization")
	}

	out = normalizeToolArguments("list_workspace_directory", map[string]any{"path": "repo root"})
	if out["path"] != "." {
		t.Errorf("path = %v", out["path"])
	}
}

func TestCountsAsImplementationRead(t *testing.T) {
	cases := map[string]bool{
		"internal/mcpsvc/register.go": true,
		"vscode-extension/src/x.ts":   true,
		"README.md":                   false,
		"go.mod":                      false,
		"docs/notes.txt":              false,
		"internal/config.yaml":        false,
		"cmd/codehelper/main.go":      true,
	}
	for path, want := range cases {
		if got := countsAsImplementationRead(path); got != want {
			t.Errorf("countsAsImplementationRead(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestOverviewBreadthMet(t *testing.T) {
	counts := map[string]int{"query": 3, "context_pack": 2, "read_workspace_file": 2, "list_workspace_directory": 1}
	if !overviewBreadthMet(counts, true, 2, 0) {
		t.Error("strict breadth should be met")
	}
	if overviewBreadthMet(map[string]int{"query": 1}, true, 0, 0) {
		t.Error("thin evidence should not satisfy breadth")
	}
	if !overviewBreadthMet(map[string]int{}, true, 0, maxBreadthNudges) {
		t.Error("nudge budget exhaustion should end breadth pressure")
	}
}

// stubTools records calls and returns canned JSON payloads.
type stubTools struct {
	mu      sync.Mutex
	calls   []string
	results map[string]string
}

func (s *stubTools) Call(_ context.Context, name string, args map[string]any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
	if out, ok := s.results[name]; ok {
		return out, nil
	}
	return `{"ok":true}`, nil
}

func (s *stubTools) WorkspaceToolsAvailable() bool { return true }

// stubLLM serves scripted chat-completion responses in order.
func stubLLM(t *testing.T, responses []string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	i := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ix := i
		if i < len(responses)-1 {
			i++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[ix]))
	}))
}

func completionWithContent(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": content}}},
	})
	return string(b)
}

func completionWithToolCall(name, args string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []map[string]any{{
				"id":   "c1",
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			}},
		}}},
	})
	return string(b)
}

func TestRunAnswersDirectlyForSocialMessage(t *testing.T) {
	srv := stubLLM(t, []string{completionWithContent("Hello! Ask me about this workspace.")})
	defer srv.Close()

	tools := &stubTools{}
	res, err := Run(context.Background(), Options{
		Mode:     ModeAsk,
		UserText: "hi",
		LLM:      llm.Config{ChatURL: srv.URL, Model: "m", APIKey: "k"},
		Tools:    tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Hello") {
		t.Errorf("text = %q", res.Text)
	}
	if len(tools.calls) != 0 {
		t.Errorf("expected no tool calls, got %v", tools.calls)
	}
}

func TestRunExecutesToolCallThenAnswer(t *testing.T) {
	srv := stubLLM(t, []string{
		completionWithToolCall("query", `{"query":"register handler"}`),
		completionWithContent("Registration happens in `internal/mcpsvc/register.go` via RegisterAll."),
	})
	defer srv.Close()

	tools := &stubTools{results: map[string]string{
		"query": `{"hits":[{"path":"internal/mcpsvc/register.go"}]}`,
	}}
	var started, completed []string
	res, err := Run(context.Background(), Options{
		Mode:     ModeAsk,
		UserText: "where are MCP tools registered in this repo?",
		LLM:      llm.Config{ChatURL: srv.URL, Model: "m", APIKey: "k"},
		Tools:    tools,
		Hooks: Hooks{
			OnToolStart:    func(name string, _ map[string]any) { started = append(started, name) },
			OnToolComplete: func(name string, _ map[string]any, _ string) { completed = append(completed, name) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.calls) != 1 || tools.calls[0] != "query" {
		t.Errorf("tool calls = %v", tools.calls)
	}
	if len(started) != 1 || len(completed) != 1 {
		t.Errorf("hooks: started=%v completed=%v", started, completed)
	}
	if !strings.Contains(res.Text, "register.go") {
		t.Errorf("text = %q", res.Text)
	}
}

func TestRunRejectsWriteToolsOutsideAgentMode(t *testing.T) {
	srv := stubLLM(t, []string{
		completionWithToolCall("write_workspace_file", `{"path":"a.txt","content":"x"}`),
		completionWithContent("Cannot write in Ask mode; here is the plan instead, see `a.txt`."),
	})
	defer srv.Close()

	tools := &stubTools{}
	res, err := Run(context.Background(), Options{
		Mode:     ModeAsk,
		UserText: "please edit a.txt in this repo",
		LLM:      llm.Config{ChatURL: srv.URL, Model: "m", APIKey: "k"},
		Tools:    tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range tools.calls {
		if writeToolNames[c] {
			t.Errorf("write tool %q executed in ask mode", c)
		}
	}
	if res.Text == "" {
		t.Error("empty result")
	}
}

func TestRunTracksWrittenPathsInAgentMode(t *testing.T) {
	editResult := `{"path":"a.txt","repo_root":"/r","diff":"--- a\n+++ b\n","revert_token":"tok1","created":false}`
	srv := stubLLM(t, []string{
		completionWithToolCall("apply_patch_workspace_file", `{"path":"a.txt","hunks":[{"old_string":"x","new_string":"y"}]}`),
		completionWithContent("Patched `a.txt` as requested."),
	})
	defer srv.Close()

	tools := &stubTools{results: map[string]string{"apply_patch_workspace_file": editResult}}
	var edits []WorkspaceEditEvent
	res, err := Run(context.Background(), Options{
		Mode:       ModeAgent,
		UserText:   "fix the typo in a.txt in this repo",
		ForceWrite: true,
		LLM:        llm.Config{ChatURL: srv.URL, Model: "m", APIKey: "k"},
		Tools:      tools,
		Hooks: Hooks{
			OnWorkspaceEdit: func(e WorkspaceEditEvent) { edits = append(edits, e) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.WrittenRelativePaths) != 1 || res.WrittenRelativePaths[0] != "a.txt" {
		t.Errorf("written = %v", res.WrittenRelativePaths)
	}
	if len(edits) != 1 || edits[0].RevertToken != "tok1" {
		t.Errorf("edits = %+v", edits)
	}
}

func TestRunDeduplicatesIdenticalRetrievalCalls(t *testing.T) {
	srv := stubLLM(t, []string{
		completionWithToolCall("query", `{"query":"register"}`),
		completionWithToolCall("query", `{"query":"register"}`),
		completionWithContent("MCP registration lives in `internal/mcpsvc/register.go`."),
	})
	defer srv.Close()

	tools := &stubTools{results: map[string]string{"query": `{"hits":[]}`}}
	var dupSeen bool
	_, err := Run(context.Background(), Options{
		Mode:     ModeAsk,
		UserText: "explain the mcp registration code in this repo",
		LLM:      llm.Config{ChatURL: srv.URL, Model: "m", APIKey: "k"},
		Tools:    tools,
		Hooks: Hooks{
			OnToolComplete: func(_ string, _ map[string]any, result string) {
				if strings.Contains(result, "duplicate_retrieval_call") {
					dupSeen = true
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(tools.calls); got != 1 {
		t.Errorf("server-side tool executions = %d, want 1 (duplicate must be skipped)", got)
	}
	if !dupSeen {
		t.Error("expected duplicate_retrieval_call control payload")
	}
}

func TestRunUnknownToolGetsErrorPayload(t *testing.T) {
	srv := stubLLM(t, []string{
		completionWithToolCall("frobnicate_zzz_qqq", `{}`),
		completionWithContent("Answer from context: see `cmd/codehelper/main.go`."),
	})
	defer srv.Close()

	tools := &stubTools{}
	var sawUnknownPayload bool
	_, err := Run(context.Background(), Options{
		Mode:     ModeAsk,
		UserText: "how does the cli work in this repo?",
		LLM:      llm.Config{ChatURL: srv.URL, Model: "m", APIKey: "k"},
		Tools:    tools,
		Hooks: Hooks{
			OnToolComplete: func(_ string, _ map[string]any, result string) {
				if strings.Contains(result, "available_tools") {
					sawUnknownPayload = true
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.calls) != 0 {
		t.Errorf("unknown tool reached the server: %v", tools.calls)
	}
	if !sawUnknownPayload {
		t.Error("expected unknown-tool error payload")
	}
}

func TestRunHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := stubLLM(t, []string{completionWithContent("never seen")})
	defer srv.Close()
	_, err := Run(ctx, Options{
		Mode:     ModeAsk,
		UserText: "hello there workspace",
		LLM:      llm.Config{ChatURL: srv.URL, Model: "m", APIKey: "k"},
		Tools:    &stubTools{},
	})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestUnknownToolErrorPayloadSuggestsClosest(t *testing.T) {
	allowed := allowedToolsForMode(ModeAsk)
	payload := unknownToolErrorPayload("list_directory_contents", allowed)
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		t.Fatal(err)
	}
	if obj["closest_match"] != "list_workspace_directory" {
		t.Errorf("closest_match = %v", obj["closest_match"])
	}
	if _, ok := obj["example_call"]; !ok {
		t.Error("missing example_call")
	}
}

func TestEmbeddedToolCallIDsAreUnique(t *testing.T) {
	a := makeEmbeddedToolCallID(0)
	b := makeEmbeddedToolCallID(1)
	if a == b {
		t.Errorf("ids collide: %s", a)
	}
	if !strings.HasPrefix(a, "embedded_") {
		t.Errorf("id = %s", a)
	}
}

func TestNormalizeModeDefaultsToAsk(t *testing.T) {
	for in, want := range map[string]Mode{"agent": ModeAgent, "PLAN": ModePlan, "": ModeAsk, "junk": ModeAsk} {
		if got := NormalizeMode(in); got != want {
			t.Errorf("NormalizeMode(%q) = %q", in, got)
		}
	}
}

var _ = fmt.Sprintf // keep fmt referenced when test tags change
