package agentapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/agent"
	"github.com/VeyrForge/codehelper/internal/llm"
)

func TestGateRoutesHealth(t *testing.T) {
	root := t.TempDir()
	s := &Server{WorkspaceRoot: root, LLM: llm.Config{}, Tools: agent.ToolCaller(nil)}
	mux := http.NewServeMux()
	s.registerGateRoutes(mux)

	for _, path := range []string{"/v1/verify", "/v1/review", "/v1/finish"} {
		body := bytes.NewBufferString(`{}`)
		req := httptest.NewRequest(http.MethodPost, path, body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	_ = filepath.Join(root, ".codehelper")
	var dummy map[string]any
	_ = json.Unmarshal([]byte(`{"use_profile":false}`), &dummy)
}
