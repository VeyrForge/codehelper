package agentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/internal/llm"
)

type stubTools struct{ calls []string }

func (s *stubTools) Call(_ context.Context, name string, _ map[string]any) (string, error) {
	s.calls = append(s.calls, name)
	return `{"ok":true}`, nil
}

func (s *stubTools) WorkspaceToolsAvailable() bool { return true }

func stubLLMServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": content}}},
	})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func newTestServer(t *testing.T, llmURL, token string) *httptest.Server {
	t.Helper()
	srv := &Server{
		WorkspaceRoot: t.TempDir(),
		LLM:           llm.Config{ChatURL: llmURL, Model: "m", APIKey: "k"},
		Tools:         &stubTools{},
		Token:         token,
		Version:       "test",
	}
	return httptest.NewServer(srv.Handler())
}

func TestHealthz(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	api := newTestServer(t, upstream.URL, "")
	defer api.Close()

	res, err := http.Get(api.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true || body["llm_ready"] != true {
		t.Errorf("body = %v", body)
	}
}

func TestBearerTokenRequired(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	api := newTestServer(t, upstream.URL, "sekret")
	defer api.Close()

	res, err := http.Get(api.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, api.URL+"/healthz", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status = %d", res2.StatusCode)
	}
}

func TestChatStreamsSSEEvents(t *testing.T) {
	upstream := stubLLMServer(t, "Hello from the core!")
	defer upstream.Close()
	api := newTestServer(t, upstream.URL, "")
	defer api.Close()

	res, err := http.Post(api.URL+"/v1/agent/chat", "application/json",
		strings.NewReader(`{"mode":"ask","text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	raw := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, rerr := res.Body.Read(buf)
		raw.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	out := raw.String()
	for _, want := range []string{"event: final", "event: done", "Hello from the core!"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q in:\n%s", want, out)
		}
	}
}

func TestTaskCreateAndPatch(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	root := t.TempDir()
	srv := &Server{
		WorkspaceRoot: root,
		LLM:           llm.Config{ChatURL: upstream.URL, Model: "m", APIKey: "k"},
		Tools:         &stubTools{},
		Version:       "test",
	}
	api := httptest.NewServer(srv.Handler())
	defer api.Close()

	res, err := http.Post(api.URL+"/v1/tasks", "application/json",
		strings.NewReader(`{"request":"add logging middleware"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", res.StatusCode)
	}
	var task map[string]any
	if err := json.NewDecoder(res.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	id, _ := task["id"].(string)
	if id == "" {
		t.Fatalf("task = %v", task)
	}
	todos, _ := task["todos"].([]any)
	if len(todos) == 0 {
		t.Fatal("expected todos")
	}
	first, _ := todos[0].(map[string]any)
	todoID, _ := first["id"].(string)

	patchBody := `{"user_notes":"use structured logging","status":"approved"}`
	req, _ := http.NewRequest(http.MethodPatch, api.URL+"/v1/tasks/"+id+"/todos/"+todoID,
		strings.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	patchRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer patchRes.Body.Close()
	if patchRes.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d", patchRes.StatusCode)
	}

	getRes, err := http.Get(api.URL + "/v1/tasks/" + id + "/timeline")
	if err != nil {
		t.Fatal(err)
	}
	defer getRes.Body.Close()
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("timeline status = %d", getRes.StatusCode)
	}
}

func TestChatRejectsEmptyText(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	api := newTestServer(t, upstream.URL, "")
	defer api.Close()

	res, err := http.Post(api.URL+"/v1/agent/chat", "application/json",
		strings.NewReader(`{"mode":"ask","text":""}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestToolCallWriteRequiresApprovedPlan(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	api := newTestServer(t, upstream.URL, "")
	defer api.Close()

	res, err := http.Post(api.URL+"/v1/tools/call", "application/json",
		strings.NewReader(`{"name":"write_workspace_file","args":{"path":"x.txt","content":"hi"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.StatusCode)
	}
}
