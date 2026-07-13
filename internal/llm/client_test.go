package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigCompletionURL(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{BaseURL: "http://localhost:8080"}, "http://localhost:8080/v1/chat/completions"},
		{Config{BaseURL: "http://localhost:8080/"}, "http://localhost:8080/v1/chat/completions"},
		{Config{BaseURL: "http://x", CompletionPath: "api/chat"}, "http://x/api/chat"},
		{Config{BaseURL: "https://llm.example.org/api/chat"}, "https://llm.example.org/api/chat"},
		{Config{BaseURL: "https://llm.example.org", CompletionPath: "/api/chat"}, "https://llm.example.org/api/chat"},
		{Config{ChatURL: "http://full/override"}, "http://full/override"},
		{Config{}, ""},
	}
	for _, c := range cases {
		if got := c.cfg.CompletionURL(); got != c.want {
			t.Errorf("CompletionURL(%+v) = %q, want %q", c.cfg, got, c.want)
		}
	}
}

func TestCompleteNonStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer k" {
			t.Errorf("missing bearer auth, got %q", got)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "m1" {
			t.Errorf("model = %v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	defer srv.Close()

	c := NewClient(Config{ChatURL: srv.URL, APIKey: "k", Model: "m1"})
	msg, err := c.Complete(context.Background(), ChatRequest{
		Model:    "m1",
		Messages: []Message{TextMessage("user", "hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Message.Text() != "hello" {
		t.Errorf("content = %q", msg.Message.Text())
	}
}

func TestCompleteOllamaNDJSONStream(t *testing.T) {
	payload := `{"model":"m","message":{"role":"assistant","content":"Hel"},"done":false} ` +
		`{"model":"m","message":{"role":"assistant","content":"lo"},"done":false} ` +
		`{"model":"m","message":{"role":"assistant","content":"!"},"done":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	var tokens []string
	c := NewClient(Config{ChatURL: srv.URL + "/api/chat", APIKey: "k"})
	msg, err := c.CompleteStreaming(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{TextMessage("user", "hi")},
	}, func(s string) { tokens = append(tokens, s) })
	if err != nil {
		t.Fatal(err)
	}
	if msg.Message.Text() != "Hello!" {
		t.Errorf("content = %q", msg.Message.Text())
	}
}

func TestParseOllamaNDJSONStreamSpaceSeparated(t *testing.T) {
	raw := []byte(`{"message":{"role":"assistant","content":"Error"}} {"message":{"role":"assistant","content":":"}}`)
	cr, ok := parseOllamaNDJSONStream(raw, nil)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if cr.Message.Text() != "Error:" {
		t.Errorf("text = %q", cr.Message.Text())
	}
}

func TestOllamaBodyToolHistory(t *testing.T) {
	toolJSON := `{"repo":"codehelper"}`
	req := ChatRequest{
		Model: "m",
		Messages: []Message{
			TextMessage("user", "what do you know?"),
			{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: ToolCallFunction{
						Name:      "project_context",
						Arguments: `{}`,
					},
				}},
			},
			{Role: "tool", ToolCallID: "call_1", Content: &toolJSON},
		},
		Tools: []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":       "project_context",
					"parameters": map[string]any{"type": "object", "additionalProperties": false},
				},
			},
		},
	}
	body := req.ollamaBody(false)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, `"arguments":"`) {
		t.Fatalf("arguments must be JSON object, got %s", s)
	}
	if !strings.Contains(s, `"arguments":{}`) {
		t.Fatalf("expected empty arguments object, got %s", s)
	}
	if !strings.Contains(s, `"tool_name":"project_context"`) {
		t.Fatalf("expected tool_name on tool message, got %s", s)
	}
	if strings.Contains(s, "additionalProperties") {
		t.Fatalf("additionalProperties should be stripped, got %s", s)
	}
}

func TestParseOllamaChatCompletionToolCalls(t *testing.T) {
	raw := []byte(`{
		"model":"m",
		"message":{
			"role":"assistant",
			"content":"",
			"tool_calls":[{"type":"function","function":{"name":"project_context","arguments":{}}}]
		},
		"done":true
	}`)
	cr, ok := parseOllamaChatCompletion(raw)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if len(cr.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %v", cr.Message.ToolCalls)
	}
	if cr.Message.ToolCalls[0].Function.Name != "project_context" {
		t.Fatalf("name = %q", cr.Message.ToolCalls[0].Function.Name)
	}
	if cr.Message.ToolCalls[0].Function.Arguments != `{}` {
		t.Fatalf("arguments = %q", cr.Message.ToolCalls[0].Function.Arguments)
	}
}

func TestCompleteOllamaNativeChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"trollbot:latest","message":{"role":"assistant","content":"works"},"done":true}`))
	}))
	defer srv.Close()

	c := NewClient(Config{ChatURL: srv.URL + "/api/chat", APIKey: "k"})
	msg, err := c.Complete(context.Background(), ChatRequest{
		Model:    "trollbot:latest",
		Messages: []Message{TextMessage("user", "hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Message.Text() != "works" {
		t.Errorf("content = %q", msg.Message.Text())
	}
}

func TestCompleteOllamaAfterToolResult(t *testing.T) {
	toolJSON := `{"repo":"codehelper"}`
	round := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(body)
		if strings.Contains(string(raw), `"arguments":"`) {
			t.Fatalf("round %d: stringified tool arguments in request: %s", round, string(raw))
		}
		w.Header().Set("Content-Type", "application/json")
		if round == 1 {
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"project_context","arguments":{}}}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"It is a Go project."}}`))
	}))
	defer srv.Close()

	c := NewClient(Config{ChatURL: srv.URL + "/api/chat", APIKey: "k"})
	req := ChatRequest{
		Model:    "m",
		Messages: []Message{TextMessage("user", "what is this project?")},
		Tools:    []any{map[string]any{"type": "function", "function": map[string]any{"name": "project_context"}}},
	}
	cr1, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if len(cr1.Message.ToolCalls) != 1 {
		t.Fatalf("round 1 tool calls = %v", cr1.Message.ToolCalls)
	}
	req.Messages = append(req.Messages, Message{
		Role:      "assistant",
		ToolCalls: cr1.Message.ToolCalls,
	})
	req.Messages = append(req.Messages, Message{Role: "tool", ToolCallID: cr1.Message.ToolCalls[0].ID, Content: &toolJSON})
	cr2, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	if cr2.Message.Text() != "It is a Go project." {
		t.Fatalf("round 2 content = %q", cr2.Message.Text())
	}
}

func TestIsOllamaNativeChatURL(t *testing.T) {
	if !IsOllamaNativeChatURL("https://llm.example.org/api/chat") {
		t.Fatal("expected ollama chat url")
	}
	if IsOllamaNativeChatURL("https://llm.example.org/v1/chat/completions") {
		t.Fatal("openai url should not match")
	}
}

func TestCompleteHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"tool_choice required is unsupported"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := NewClient(Config{ChatURL: srv.URL, APIKey: "k"})
	_, err := c.Complete(context.Background(), ChatRequest{Model: "m", Messages: []Message{TextMessage("user", "x")}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "LLM HTTP 400") {
		t.Errorf("error = %v", err)
	}
	if !ShouldDowngradeToolControls(err) {
		t.Errorf("expected downgrade signal for %v", err)
	}
}

func TestCompleteStreamingMergesToolCallFragments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"content":"Think"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"query","arguments":"{\"que"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ry\":\"x\"}"}}]}}]}`,
			"[DONE]",
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
	}))
	defer srv.Close()

	var tokens []string
	c := NewClient(Config{ChatURL: srv.URL, APIKey: "k"})
	msg, err := c.CompleteStreaming(context.Background(), ChatRequest{Model: "m", Messages: []Message{TextMessage("user", "x")}}, func(s string) {
		tokens = append(tokens, s)
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Message.Text() != "Think" {
		t.Errorf("content = %q", msg.Message.Text())
	}
	if len(tokens) != 1 || tokens[0] != "Think" {
		t.Errorf("tokens = %v", tokens)
	}
	if len(msg.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %v", msg.Message.ToolCalls)
	}
	tc := msg.Message.ToolCalls[0]
	if tc.ID != "c1" || tc.Function.Name != "query" || tc.Function.Arguments != `{"query":"x"}` {
		t.Errorf("merged tool call = %+v", tc)
	}
}

func TestCompleteStreamingJSONFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"plain"}}]}`))
	}))
	defer srv.Close()

	var tokens []string
	c := NewClient(Config{ChatURL: srv.URL, APIKey: "k"})
	msg, err := c.CompleteStreaming(context.Background(), ChatRequest{Model: "m", Messages: []Message{TextMessage("user", "x")}}, func(s string) {
		tokens = append(tokens, s)
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Message.Text() != "plain" || len(tokens) != 1 {
		t.Errorf("msg=%q tokens=%v", msg.Message.Text(), tokens)
	}
}
