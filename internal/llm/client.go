// Package llm provides an OpenAI-compatible chat-completions client for the
// IDE-agnostic agent core. It supports native tool calls, optional SSE
// streaming, and configuration from environment variables so any client
// (VS Code extension, CLI, HTTP API) shares the same provider wiring.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is one OpenAI-style chat message. Content is a pointer so an
// assistant message that only carries tool calls round-trips as null.
type Message struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Text returns the message content or empty string.
func (m Message) Text() string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

// TextMessage builds a plain message for a role.
func TextMessage(role, content string) Message {
	c := content
	return Message{Role: role, Content: &c}
}

// ToolCall is an OpenAI-style function tool call.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction carries the function name and raw JSON arguments.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Usage reports token counts from an OpenAI-compatible completion response.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Add merges another usage record into this one (for multi-round agent runs).
func (u *Usage) Add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	if other.TotalTokens > 0 {
		u.TotalTokens += other.TotalTokens
	} else if other.PromptTokens > 0 || other.CompletionTokens > 0 {
		u.TotalTokens += other.PromptTokens + other.CompletionTokens
	}
}

// CompletionResult is one chat completion plus optional provider usage stats.
type CompletionResult struct {
	Message Message
	Usage   Usage
}

// ChatRequest describes one completion call.
type ChatRequest struct {
	Model             string
	Messages          []Message
	Tools             []any
	ToolChoice        string // "", "auto", "required"
	ParallelToolCalls *bool
	Temperature       *float64
}

// Config resolves the provider endpoint, model, and key for one run.
type Config struct {
	BaseURL        string
	ChatURL        string
	CompletionPath string
	Model          string
	APIKey         string
	Temperature    *float64
}

// ConfigFromEnv reads provider settings from environment variables, then fills
// any unset fields from ~/.codehelper/llm.json. Env always wins over the file.
// API keys are env-only (CODEHELPER_LLM_API_KEY or OPENAI_API_KEY).
func ConfigFromEnv() Config {
	env := configFromEnvRaw()
	uf, err := LoadUserFile()
	if err != nil {
		return env
	}
	return mergeUserFile(env, uf)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// CompletionURL resolves the full chat-completions URL.
func (c Config) CompletionURL() string {
	if full := strings.TrimSpace(c.ChatURL); full != "" {
		return full
	}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return ""
	}
	// Base URL already points at Ollama POST /api/chat (not an OpenAI API root).
	if IsOllamaNativeChatURL(base) {
		return base
	}
	p := strings.TrimSpace(c.CompletionPath)
	if p == "" {
		p = "/v1/chat/completions"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return base + p
}

// Ready reports whether URL, model, and key are all configured.
func (c Config) Ready() bool {
	return c.CompletionURL() != "" && strings.TrimSpace(c.Model) != "" && strings.TrimSpace(c.APIKey) != ""
}

// Client posts chat completions to one OpenAI-compatible endpoint.
type Client struct {
	URL        string
	APIKey     string
	HTTPClient *http.Client
}

// NewClient builds a client from config with a generous default timeout
// (local models can be slow; streaming keeps the connection alive).
func NewClient(cfg Config) *Client {
	return &Client{
		URL:        cfg.CompletionURL(),
		APIKey:     cfg.APIKey,
		HTTPClient: &http.Client{Timeout: 30 * time.Minute},
	}
}

func (r ChatRequest) body(stream bool) map[string]any {
	body := map[string]any{
		"model":    r.Model,
		"messages": r.Messages,
	}
	if len(r.Tools) > 0 {
		body["tools"] = r.Tools
	}
	if r.ToolChoice != "" {
		body["tool_choice"] = r.ToolChoice
	}
	if r.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *r.ParallelToolCalls
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	return body
}

func (c *Client) post(ctx context.Context, body map[string]any, sse bool) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if sse {
		req.Header.Set("Accept", "text/event-stream, application/json")
	}
	return c.HTTPClient.Do(req)
}

// IsOllamaNativeChatURL reports endpoints that use Ollama's POST /api/chat
// contract instead of OpenAI /v1/chat/completions.
func IsOllamaNativeChatURL(url string) bool {
	u := strings.TrimRight(strings.TrimSpace(url), "/")
	return strings.HasSuffix(u, "/api/chat")
}

// parseOllamaNDJSONStream reads one or more Ollama chat chunks from a response
// body (newline- or space-separated JSON objects).
func parseOllamaNDJSONStream(raw []byte, onToken func(string)) (CompletionResult, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return CompletionResult{}, false
	}
	var content strings.Builder
	role := "assistant"
	dec := json.NewDecoder(bytes.NewReader(raw))
	chunks := 0
	for dec.More() {
		var chunk struct {
			Message Message `json:"message"`
		}
		if err := dec.Decode(&chunk); err != nil {
			if chunks == 0 {
				return CompletionResult{}, false
			}
			break
		}
		chunks++
		if chunk.Message.Role != "" {
			role = chunk.Message.Role
		}
		if chunk.Message.Content != nil {
			piece := *chunk.Message.Content
			if piece != "" {
				content.WriteString(piece)
				if onToken != nil {
					onToken(piece)
				}
			}
		}
	}
	if content.Len() == 0 {
		return CompletionResult{}, false
	}
	text := content.String()
	return CompletionResult{Message: Message{Role: role, Content: &text}}, true
}

func parseCompletionBody(raw []byte) (CompletionResult, error) {
	cr, err := parseCompletionBodyOnce(raw)
	if err == nil {
		return cr, nil
	}
	if cr, ok := parseOllamaNDJSONStream(raw, nil); ok {
		return cr, nil
	}
	return CompletionResult{}, err
}

func normalizeToolCallFunction(name string, raw json.RawMessage) ToolCallFunction {
	fn := ToolCallFunction{Name: name, Arguments: "{}"}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return fn
	}
	if raw[0] == '{' || raw[0] == '[' {
		fn.Arguments = string(raw)
		return fn
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		fn.Arguments = s
		return fn
	}
	fn.Arguments = string(raw)
	return fn
}

func decodeOllamaWireMessage(raw json.RawMessage) (Message, bool) {
	var wire struct {
		Role      string  `json:"role"`
		Content   *string `json:"content"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Message{}, false
	}
	msg := Message{Role: wire.Role, Content: wire.Content}
	for _, tc := range wire.ToolCalls {
		typ := tc.Type
		if typ == "" {
			typ = "function"
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID:       tc.ID,
			Type:     typ,
			Function: normalizeToolCallFunction(tc.Function.Name, tc.Function.Arguments),
		})
	}
	return msg, wire.Role != "" || wire.Content != nil || len(wire.ToolCalls) > 0
}

func parseOllamaChatCompletion(raw []byte) (CompletionResult, bool) {
	var data struct {
		Message json.RawMessage `json:"message"`
		Usage   Usage           `json:"usage"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || len(data.Message) == 0 {
		return CompletionResult{}, false
	}
	msg, ok := decodeOllamaWireMessage(data.Message)
	if !ok {
		return CompletionResult{}, false
	}
	usage := data.Usage
	if usage.TotalTokens == 0 && (usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return CompletionResult{Message: msg, Usage: usage}, true
}

func parseCompletionBodyOnce(raw []byte) (CompletionResult, error) {
	if cr, ok := parseOllamaChatCompletion(raw); ok {
		return cr, nil
	}
	var data struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Message Message `json:"message"`
		Usage   Usage   `json:"usage"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return CompletionResult{}, fmt.Errorf("LLM returned non-JSON: %s", clip(string(raw), 400))
	}
	usage := data.Usage
	if usage.TotalTokens == 0 && (usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if len(data.Choices) > 0 {
		return CompletionResult{Message: data.Choices[0].Message, Usage: usage}, nil
	}
	if data.Message.Role != "" || data.Message.Content != nil || len(data.Message.ToolCalls) > 0 {
		return CompletionResult{Message: data.Message, Usage: usage}, nil
	}
	return CompletionResult{}, fmt.Errorf("LLM response missing choices[0].message")
}

func ollamaToolArguments(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func ollamaToolCalls(calls []ToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, tc := range calls {
		fn := map[string]any{
			"name":      tc.Function.Name,
			"arguments": ollamaToolArguments(tc.Function.Arguments),
		}
		item := map[string]any{
			"type":     "function",
			"function": fn,
		}
		out = append(out, item)
	}
	return out
}

func ollamaMessages(msgs []Message) []map[string]any {
	idToName := map[string]string{}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				idToName[tc.ID] = tc.Function.Name
			}
		}
		item := map[string]any{"role": m.Role}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			item["content"] = ""
		} else if m.Content != nil {
			item["content"] = *m.Content
		} else {
			item["content"] = ""
		}
		if len(m.ToolCalls) > 0 {
			item["tool_calls"] = ollamaToolCalls(m.ToolCalls)
		}
		if m.Role == "tool" {
			if name := idToName[m.ToolCallID]; name != "" {
				item["tool_name"] = name
			}
		}
		out = append(out, item)
	}
	return out
}

func stripAdditionalProperties(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "additionalProperties")
		for k, val := range x {
			x[k] = stripAdditionalProperties(val)
		}
		return x
	case []any:
		for i, val := range x {
			x[i] = stripAdditionalProperties(val)
		}
		return x
	default:
		return v
	}
}

func ollamaTools(tools []any) []any {
	if len(tools) == 0 {
		return nil
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return tools
	}
	var raw []any
	if err := json.Unmarshal(b, &raw); err != nil {
		return tools
	}
	for i, t := range raw {
		raw[i] = stripAdditionalProperties(t)
	}
	return raw
}

func (r ChatRequest) ollamaBody(stream bool) map[string]any {
	body := map[string]any{
		"model":    r.Model,
		"messages": ollamaMessages(r.Messages),
		"stream":   stream,
	}
	// Ollama /api/chat: tools list is ok; OpenAI-only tool_choice / parallel_tool_calls are not.
	if len(r.Tools) > 0 {
		body["tools"] = ollamaTools(r.Tools)
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	return body
}

func (c *Client) completeOllamaChat(ctx context.Context, req ChatRequest, onToken func(string)) (CompletionResult, error) {
	// Prefer non-streaming first (matches typical Ollama curl); fall back to stream.
	for _, stream := range []bool{false, true} {
		res, err := c.post(ctx, req.ollamaBody(stream), false)
		if err != nil {
			return CompletionResult{}, err
		}
		raw, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr != nil {
			return CompletionResult{}, readErr
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			if !stream {
				continue
			}
			return CompletionResult{}, fmt.Errorf("LLM HTTP %d from %s: %s", res.StatusCode, c.URL, clip(string(raw), 800))
		}
		tokenFn := onToken
		if !stream {
			tokenFn = nil
		}
		if cr, ok := parseOllamaNDJSONStream(raw, tokenFn); ok {
			if !stream && onToken != nil {
				if t := strings.TrimSpace(cr.Message.Text()); t != "" {
					onToken(t)
				}
			}
			return cr, nil
		}
		if cr, err := parseCompletionBodyOnce(raw); err == nil {
			if t := strings.TrimSpace(cr.Message.Text()); t != "" && onToken != nil {
				onToken(t)
			}
			return cr, nil
		} else if !stream {
			continue
		} else {
			return CompletionResult{}, err
		}
	}
	return CompletionResult{}, fmt.Errorf("LLM returned unparseable Ollama response from %s", c.URL)
}

func parseUsageFromSSEPayload(payload string) (Usage, bool) {
	var chunk struct {
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return Usage{}, false
	}
	u := chunk.Usage
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 {
		return Usage{}, false
	}
	if u.TotalTokens == 0 && (u.PromptTokens > 0 || u.CompletionTokens > 0) {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u, true
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Complete performs one non-streaming chat completion.
func (c *Client) Complete(ctx context.Context, req ChatRequest) (CompletionResult, error) {
	body := req.body(false)
	if IsOllamaNativeChatURL(c.URL) {
		body = req.ollamaBody(false)
	}
	res, err := c.post(ctx, body, false)
	if err != nil {
		return CompletionResult{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return CompletionResult{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return CompletionResult{}, fmt.Errorf("LLM HTTP %d from %s: %s", res.StatusCode, c.URL, clip(string(raw), 800))
	}
	return parseCompletionBody(raw)
}

type streamDelta struct {
	Content   *string `json:"content"`
	ToolCalls []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// CompleteStreaming performs an SSE chat completion, merging streamed
// tool_call fragments by index. onToken may be nil.
func (c *Client) CompleteStreaming(ctx context.Context, req ChatRequest, onToken func(string)) (CompletionResult, error) {
	// Ollama /api/chat uses NDJSON lines, not OpenAI SSE.
	if IsOllamaNativeChatURL(c.URL) {
		return c.completeOllamaChat(ctx, req, onToken)
	}
	res, err := c.post(ctx, req.body(true), true)
	if err != nil {
		return CompletionResult{}, err
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(res.Body)
		return CompletionResult{}, fmt.Errorf("LLM HTTP %d from %s: %s", res.StatusCode, c.URL, clip(string(raw), 800))
	}

	if ct := res.Header.Get("Content-Type"); strings.Contains(ct, "application/json") {
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			return CompletionResult{}, err
		}
		cr, err := parseCompletionBody(raw)
		if err != nil {
			return CompletionResult{}, err
		}
		if t := strings.TrimSpace(cr.Message.Text()); t != "" && onToken != nil {
			onToken(t)
		}
		return cr, nil
	}

	type slot struct {
		id   string
		name string
		args strings.Builder
	}
	slots := map[int]*slot{}
	maxIdx := -1
	var content strings.Builder
	var usage Usage

	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			continue
		}
		if u, ok := parseUsageFromSSEPayload(payload); ok {
			usage = u
		}
		var chunk struct {
			Choices []struct {
				Delta streamDelta `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil || len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != nil && *delta.Content != "" {
			content.WriteString(*delta.Content)
			if onToken != nil {
				onToken(*delta.Content)
			}
		}
		for _, tc := range delta.ToolCalls {
			ix := 0
			if tc.Index != nil {
				ix = *tc.Index
			}
			s, ok := slots[ix]
			if !ok {
				s = &slot{}
				slots[ix] = s
			}
			if ix > maxIdx {
				maxIdx = ix
			}
			if tc.ID != "" {
				s.id = tc.ID
			}
			if tc.Function.Name != "" {
				s.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				s.args.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return CompletionResult{}, err
	}

	var toolCalls []ToolCall
	for ix := 0; ix <= maxIdx; ix++ {
		s, ok := slots[ix]
		if !ok || s.name == "" {
			continue
		}
		id := s.id
		if id == "" {
			id = fmt.Sprintf("call_%d", ix)
		}
		toolCalls = append(toolCalls, ToolCall{
			ID:   id,
			Type: "function",
			Function: ToolCallFunction{
				Name:      s.name,
				Arguments: s.args.String(),
			},
		})
	}

	msg := Message{Role: "assistant"}
	if content.Len() > 0 {
		text := content.String()
		msg.Content = &text
	}
	msg.ToolCalls = toolCalls
	if usage.TotalTokens == 0 && (usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return CompletionResult{Message: msg, Usage: usage}, nil
}

// ShouldDowngradeToolControls reports whether a provider rejected strict
// tool controls (tool_choice=required / parallel_tool_calls) so the caller
// can retry with safer defaults.
func ShouldDowngradeToolControls(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	mentionsControl := strings.Contains(msg, "tool_choice") ||
		strings.Contains(msg, "tool choice") ||
		strings.Contains(msg, "required") ||
		strings.Contains(msg, "parallel_tool_calls") ||
		strings.Contains(msg, "parallel tool calls")
	if !mentionsControl {
		return false
	}
	for _, marker := range []string{"unsupported", "unknown", "invalid", "not allowed", "must be one of", "unrecognized", "bad request", "400"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
