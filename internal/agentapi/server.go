// Package agentapi exposes the Go agent loop over a local HTTP API with
// Server-Sent Events, so thin clients (VS Code extension, terminals) can
// drive chats without owning any LLM orchestration.
package agentapi

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/VeyrForge/codehelper/internal/agent"
	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/taskstore"
)

const maxChatBodyBytes = 4 << 20

// Server wires the agent loop and verification gate behind HTTP.
type Server struct {
	WorkspaceRoot string
	LLM           llm.Config
	Tools         agent.ToolCaller
	// Token, when non-empty, requires Authorization: Bearer <Token>.
	Token       string
	Version     string
	DefaultRepo string
}

// chatRequest is the POST /v1/agent/chat body.
type chatRequest struct {
	Mode          string       `json:"mode"`
	Text          string       `json:"text"`
	PriorTurns    []agent.Turn `json:"prior_turns,omitempty"`
	Attachments   []string     `json:"attachments,omitempty"`
	ModelHint     string       `json:"model_hint,omitempty"`
	TaskID        string       `json:"task_id,omitempty"`
	ForceWrite    bool         `json:"force_write,omitempty"`
	MaxToolRounds int          `json:"max_tool_rounds,omitempty"`
	// Verify runs the post-agent verification gate after an agent-mode turn.
	Verify       bool  `json:"verify,omitempty"`
	MaxFixRounds int   `json:"max_fix_rounds,omitempty"`
	AutoVerify   *bool `json:"auto_verify,omitempty"`
	AutoReview   *bool `json:"auto_review,omitempty"`
}

type toolCallRequest struct {
	Name       string         `json:"name"`
	Args       map[string]any `json:"args,omitempty"`
	TaskID     string         `json:"task_id,omitempty"`
	ForceWrite bool           `json:"force_write,omitempty"`
}

// Handler returns the HTTP handler for the agent API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /v1/agent/chat", s.handleChat)
	mux.HandleFunc("POST /v1/tools/call", s.handleToolCall)
	s.registerTaskRoutes(mux)
	s.registerGateRoutes(mux)
	s.registerExtraRoutes(mux)
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				writeJSONError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) activeLLMConfig() llm.Config {
	cfg := llm.ConfigFromEnv()
	if cfg.Ready() {
		return cfg
	}
	return s.LLM
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	llmCfg := s.activeLLMConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"version":            s.Version,
		"default_repo":       s.DefaultRepo,
		"llm_ready":          llmCfg.Ready(),
		"llm_completion_url": llmCfg.CompletionURL(),
		"llm_model":          llmCfg.Model,
	})
}

// handleChat runs one agent turn and streams progress as SSE events:
// round, timing, planning, token, tool_start, tool_complete, workspace_edit,
// log, final, gate, error, done. Closing the connection cancels the turn.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeJSONError(w, http.StatusBadRequest, "text is required")
		return
	}
	userText := agent.EnrichUserText(agent.EnrichInput{
		Text:        req.Text,
		Workspace:   s.WorkspaceRoot,
		Attachments: req.Attachments,
		ModelHint:   req.ModelHint,
	})
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(event string, payload any) {
		b, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	mode := agent.NormalizeMode(req.Mode)
	ctx := r.Context()
	taskID := strings.TrimSpace(req.TaskID)
	if taskID != "" {
		_, _ = taskstore.New(s.WorkspaceRoot).RecordMessage(taskID, taskstore.Message{
			Role: "user", Content: req.Text, Source: "api",
		})
	}

	hooks := agent.Hooks{
		OnRound: func(iteration, maxIterations int) {
			emit("round", map[string]any{"iteration": iteration, "max_iterations": maxIterations})
		},
		OnAssistantModelTiming: func(elapsedSec float64, willUseTools bool) {
			emit("timing", map[string]any{"elapsed_sec": elapsedSec, "will_use_tools": willUseTools})
		},
		OnAssistantPlanning: func(text string, toolNames []string) {
			emit("planning", map[string]any{"text": text, "tools": toolNames})
		},
		OnAssistantToken: func(chunk string) {
			emit("token", map[string]any{"chunk": chunk})
		},
		OnToolStart: func(name string, args map[string]any) {
			emit("tool_start", map[string]any{"name": name, "args": args})
		},
		OnToolComplete: func(name string, args map[string]any, result string) {
			emit("tool_complete", map[string]any{"name": name, "args": args, "result": result})
		},
		OnWorkspaceEdit: func(edit agent.WorkspaceEditEvent) {
			emit("workspace_edit", edit)
		},
		OnTokenUsage: func(summary agent.TokenUsageSummary) {
			emit("usage", summary)
		},
	}
	logLine := func(line string) {
		emit("log", map[string]any{"line": line})
	}

	llmCfg := s.activeLLMConfig()
	if hint := strings.TrimSpace(req.ModelHint); hint != "" {
		llmCfg.Model = hint
	}

	res, err := agent.Run(ctx, agent.Options{
		Mode:          mode,
		UserText:      userText,
		PriorTurns:    req.PriorTurns,
		TaskID:        strings.TrimSpace(req.TaskID),
		ForceWrite:    req.ForceWrite,
		Hooks:         hooks,
		Log:           logLine,
		LLM:           llmCfg,
		Tools:         s.Tools,
		WorkspaceRoot: s.WorkspaceRoot,
		MaxToolRounds: req.MaxToolRounds,
		Stream:        true,

		PrefetchBroadAskEvidence: true,
	})
	if err != nil {
		emit("error", map[string]any{"message": err.Error()})
		emit("done", map[string]any{})
		return
	}
	emit("final", res)

	if taskID != "" && res != nil {
		_, _ = taskstore.New(s.WorkspaceRoot).RecordMessage(taskID, taskstore.Message{
			Role: "assistant", Content: res.Text, Source: "api",
		})
	}

	if req.Verify && mode == agent.ModeAgent && len(res.WrittenRelativePaths) > 0 && ctx.Err() == nil {
		gateOpts := agent.GateOptions{
			WorkspaceRoot:        s.WorkspaceRoot,
			WrittenRelativePaths: res.WrittenRelativePaths,
			PriorTurns:           req.PriorTurns,
			UserPromptEnriched:   userText,
			AssistantReplyPrefix: res.Text,
			AutoVerify:           req.AutoVerify == nil || *req.AutoVerify,
			AutoReview:           req.AutoReview == nil || *req.AutoReview,
			MaxFixRounds:         req.MaxFixRounds,
			Log:                  logLine,
			Hooks:                hooks,
			LLM:                  s.LLM,
			Tools:                s.Tools,
		}
		if req.MaxFixRounds == 0 {
			gateOpts.MaxFixRounds = 3
		}
		gate, gerr := agent.RunVerificationGate(ctx, gateOpts)
		if gerr != nil {
			emit("error", map[string]any{"message": gerr.Error()})
		} else {
			emit("gate", gate)
		}
	}

	emit("done", map[string]any{})
}

// handleToolCall invokes one MCP tool in-process (e.g. revert_workspace_edit
// when the user clicks Undo in a client UI) and returns its flattened result.
func (s *Server) handleToolCall(w http.ResponseWriter, r *http.Request) {
	var req toolCallRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if agent.IsWorkspaceWriteTool(req.Name) {
		ok, msg := agent.WritesAllowed(agent.ModeAgent, s.WorkspaceRoot, strings.TrimSpace(req.TaskID), req.ForceWrite)
		if !ok {
			writeJSONError(w, http.StatusForbidden, msg)
			return
		}
	}
	out, err := s.Tools.Call(r.Context(), req.Name, req.Args)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": out})
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
