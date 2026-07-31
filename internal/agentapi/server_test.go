package agentapi

import (
	"context"
	"encoding/json"
	"io"
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

func doAuthed(t *testing.T, method, url, token, contentType, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestHealthz(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	api := newTestServer(t, upstream.URL, "sekret")
	defer api.Close()

	for _, path := range []string{"/healthz", "/ready"} {
		res, err := http.Get(api.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			t.Fatalf("%s status = %d", path, res.StatusCode)
		}
		var body map[string]any
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			res.Body.Close()
			t.Fatal(err)
		}
		res.Body.Close()
		if body["ok"] != true || body["llm_ready"] != true {
			t.Errorf("%s body = %v", path, body)
		}
		if _, ok := body["llm_completion_url"]; ok {
			t.Errorf("%s unauthenticated must omit llm_completion_url, got %v", path, body)
		}
		if _, ok := body["llm_model"]; ok {
			t.Errorf("%s unauthenticated must omit llm_model, got %v", path, body)
		}
	}
}

func TestHealthzAuthenticatedIncludesLLMDetails(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	api := newTestServer(t, upstream.URL, "sekret")
	defer api.Close()

	res := doAuthed(t, http.MethodGet, api.URL+"/healthz", "sekret", "", "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["llm_model"] != "m" {
		t.Fatalf("authenticated healthz missing llm_model: %v", body)
	}
	if _, ok := body["llm_completion_url"]; !ok {
		t.Fatalf("authenticated healthz missing llm_completion_url: %v", body)
	}
}

func TestBearerTokenRequired(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	api := newTestServer(t, upstream.URL, "sekret")
	defer api.Close()

	// Health stays public (slim).
	res, err := http.Get(api.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unauthenticated healthz status = %d", res.StatusCode)
	}

	// Chat requires bearer.
	resChat, err := http.Post(api.URL+"/v1/agent/chat", "application/json",
		strings.NewReader(`{"mode":"ask","text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	resChat.Body.Close()
	if resChat.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated chat status = %d", resChat.StatusCode)
	}

	res2 := doAuthed(t, http.MethodGet, api.URL+"/healthz", "sekret", "", "")
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("authenticated healthz status = %d", res2.StatusCode)
	}
}

func TestEmptyTokenRefusesChatAndTools(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	api := newTestServer(t, upstream.URL, "")
	defer api.Close()

	res, err := http.Get(api.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("healthz with empty token status = %d", res.StatusCode)
	}

	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/v1/agent/chat", `{"mode":"ask","text":"hi"}`},
		{http.MethodPost, "/v1/tools/call", `{"name":"query","args":{"query":"x"}}`},
		{http.MethodPost, "/v1/tasks", `{"request":"add logging"}`},
	} {
		r := doAuthed(t, tc.method, api.URL+tc.path, "", "application/json", tc.body)
		r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", tc.method, tc.path, r.StatusCode)
		}
	}
}

func TestChatStreamsSSEEvents(t *testing.T) {
	upstream := stubLLMServer(t, "Hello from the core!")
	defer upstream.Close()
	const token = "sekret"
	api := newTestServer(t, upstream.URL, token)
	defer api.Close()

	res := doAuthed(t, http.MethodPost, api.URL+"/v1/agent/chat", token, "application/json",
		`{"mode":"ask","text":"hi"}`)
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
	const token = "sekret"
	root := t.TempDir()
	srv := &Server{
		WorkspaceRoot: root,
		LLM:           llm.Config{ChatURL: upstream.URL, Model: "m", APIKey: "k"},
		Tools:         &stubTools{},
		Token:         token,
		Version:       "test",
	}
	api := httptest.NewServer(srv.Handler())
	defer api.Close()

	res := doAuthed(t, http.MethodPost, api.URL+"/v1/tasks", token, "application/json",
		`{"request":"add logging middleware"}`)
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
	patchRes := doAuthed(t, http.MethodPatch, api.URL+"/v1/tasks/"+id+"/todos/"+todoID, token,
		"application/json", patchBody)
	defer patchRes.Body.Close()
	if patchRes.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d", patchRes.StatusCode)
	}

	getRes := doAuthed(t, http.MethodGet, api.URL+"/v1/tasks/"+id+"/timeline", token, "", "")
	defer getRes.Body.Close()
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("timeline status = %d", getRes.StatusCode)
	}
}

func TestChatRejectsEmptyText(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	const token = "sekret"
	api := newTestServer(t, upstream.URL, token)
	defer api.Close()

	res := doAuthed(t, http.MethodPost, api.URL+"/v1/agent/chat", token, "application/json",
		`{"mode":"ask","text":""}`)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestToolCallWriteRequiresApprovedPlan(t *testing.T) {
	upstream := stubLLMServer(t, "hi")
	defer upstream.Close()
	const token = "sekret"
	api := newTestServer(t, upstream.URL, token)
	defer api.Close()

	res := doAuthed(t, http.MethodPost, api.URL+"/v1/tools/call", token, "application/json",
		`{"name":"write_workspace_file","args":{"path":"x.txt","content":"hi"}}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.StatusCode)
	}
}
