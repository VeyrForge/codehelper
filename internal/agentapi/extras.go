package agentapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/VeyrForge/codehelper/internal/agent"
	"github.com/VeyrForge/codehelper/internal/memory"
	"github.com/VeyrForge/codehelper/internal/plan"
	"github.com/VeyrForge/codehelper/internal/research"
	"github.com/VeyrForge/codehelper/internal/taskstore"
	"github.com/VeyrForge/codehelper/internal/verify"
)

func (s *Server) registerExtraRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/research", s.handleResearch)
	mux.HandleFunc("POST /v1/tasks/{id}/plan/regenerate", s.handleRegeneratePlan)
	mux.HandleFunc("POST /v1/tasks/{id}/decisions", s.handleRecordDecision)
	mux.HandleFunc("GET /v1/memory/proposals", s.handleListMemoryProposals)
	mux.HandleFunc("POST /v1/memory/proposals", s.handleProposeMemory)
	mux.HandleFunc("POST /v1/memory/approve", s.handleMemoryApprove)
	mux.HandleFunc("POST /v1/memory/reject", s.handleMemoryReject)
	mux.HandleFunc("GET /v1/tasks/{id}/commands", s.handleListCommands)
	mux.HandleFunc("POST /v1/tasks/{id}/commands", s.handleProposeCommand)
	mux.HandleFunc("POST /v1/tasks/{id}/commands/{cmdId}/approve", s.handleApproveCommand)
	mux.HandleFunc("POST /v1/tasks/{id}/todos/{todoId}/debug", s.handleDebugTodo)
}

type researchRequest struct {
	Query    string   `json:"query"`
	Sources  []string `json:"sources,omitempty"`
	Approved bool     `json:"approved,omitempty"`
	TaskID   string   `json:"task_id,omitempty"`
}

func (s *Server) handleResearch(w http.ResponseWriter, r *http.Request) {
	var req researchRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	out, err := research.Run(r.Context(), research.Input{
		Query:        req.Query,
		Sources:      req.Sources,
		NetworkOK:    research.NetworkEnabled(s.WorkspaceRoot),
		UserApproved: req.Approved,
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if taskID := strings.TrimSpace(req.TaskID); taskID != "" && out.BlockedReason == "" {
		t, lerr := taskstore.New(s.WorkspaceRoot).Load(taskID)
		if lerr == nil {
			t.Plan.ResearchSummary = research.ToPlanSummary(out)
			_ = taskstore.New(s.WorkspaceRoot).Save(t)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRegeneratePlan(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	st := taskstore.New(s.WorkspaceRoot)
	t, err := st.Load(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	out, err := s.buildPlan(r.Context(), plan.Input{Request: t.UserRequest, RepoRoot: s.WorkspaceRoot}, nil, false)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	t.Plan = out.Plan
	t.Todos = out.Todos
	t.DecisionPoints = out.DecisionPoints
	_ = st.AppendEvent(t, taskstore.Event{Type: "plan_regenerated", Actor: "api"})
	if err := st.Save(t); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

type decisionRequest struct {
	DecisionID string `json:"decision_id"`
	Choice     string `json:"choice"`
}

func (s *Server) handleRecordDecision(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var req decisionRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	t, err := taskstore.New(s.WorkspaceRoot).RecordDecisionChoice(id, req.DecisionID, req.Choice)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleListMemoryProposals(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
	if taskID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"proposals": []taskstore.MemoryProposal{}})
		return
	}
	t, err := taskstore.New(s.WorkspaceRoot).Load(taskID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": t.MemoryProposals})
}

type memoryActionRequest struct {
	TaskID     string `json:"task_id"`
	ProposalID string `json:"proposal_id"`
	Text       string `json:"text,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

func (s *Server) handleProposeMemory(w http.ResponseWriter, r *http.Request) {
	var req memoryActionRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeJSONError(w, http.StatusBadRequest, "text is required")
		return
	}
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" {
		writeJSONError(w, http.StatusBadRequest, "task_id is required")
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "pattern"
	}
	t, err := taskstore.New(s.WorkspaceRoot).ProposeMemory(taskID, taskstore.MemoryProposal{
		Kind: kind, Text: text, Status: "pending",
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleMemoryApprove(w http.ResponseWriter, r *http.Request) {
	var req memoryActionRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	st := taskstore.New(s.WorkspaceRoot)
	ms := memory.Open(s.WorkspaceRoot)
	if req.ProposalID != "" {
		t, err := st.ResolveMemoryProposal(req.TaskID, req.ProposalID, "approved")
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		for _, mp := range t.MemoryProposals {
			if mp.ID == req.ProposalID {
				_ = ms.AddDecision(mp.Text)
				break
			}
		}
		writeJSON(w, http.StatusOK, t)
		return
	}
	if strings.TrimSpace(req.Text) != "" {
		_ = ms.AddDecision(req.Text)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMemoryReject(w http.ResponseWriter, r *http.Request) {
	var req memoryActionRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	t, err := taskstore.New(s.WorkspaceRoot).ResolveMemoryProposal(req.TaskID, req.ProposalID, "rejected")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleListCommands(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	t, err := taskstore.New(s.WorkspaceRoot).Load(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": t.Commands})
}

type commandProposal struct {
	Command []string `json:"command"`
	Purpose string   `json:"purpose,omitempty"`
	Mode    string   `json:"mode,omitempty"`
	TodoID  string   `json:"todo_id,omitempty"`
}

func (s *Server) handleProposeCommand(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var req commandProposal
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	if len(req.Command) == 0 {
		writeJSONError(w, http.StatusBadRequest, "command is required")
		return
	}
	if blocked, reason := verify.CommandBlocked(req.Command); blocked {
		writeJSONError(w, http.StatusForbidden, reason)
		return
	}
	if req.Mode == "shell" {
		writeJSONError(w, http.StatusForbidden, "shell mode requires explicit project policy")
		return
	}
	t, err := taskstore.New(s.WorkspaceRoot).AddCommandProposal(id, taskstore.CommandRecord{
		Command: req.Command, Purpose: req.Purpose, Mode: req.Mode, TodoID: req.TodoID,
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleApproveCommand(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	cmdID := strings.TrimSpace(r.PathValue("cmdId"))
	st := taskstore.New(s.WorkspaceRoot)
	t, err := st.Load(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	var rec *taskstore.CommandRecord
	for i := range t.Commands {
		if t.Commands[i].ID == cmdID {
			rec = &t.Commands[i]
			break
		}
	}
	if rec == nil {
		writeJSONError(w, http.StatusNotFound, "command not found")
		return
	}
	if rec.Mode == "shell" {
		writeJSONError(w, http.StatusForbidden, "shell mode requires explicit project policy")
		return
	}
	if blocked, reason := verify.CommandBlocked(rec.Command); blocked {
		writeJSONError(w, http.StatusForbidden, reason)
		return
	}
	outcomes := verify.RunCommandLines(r.Context(), []string{strings.Join(rec.Command, " ")}, verify.RunCommandsOptions{
		RepoRoot: s.WorkspaceRoot, ExecMode: verify.ExecArgv,
	})
	rec.Status = taskstore.CommandRan
	ok := len(outcomes) > 0 && outcomes[0].ExitCode == 0 && outcomes[0].Error == ""
	if !ok {
		rec.Status = taskstore.CommandFailed
	}
	b, _ := json.Marshal(outcomes)
	rec.Output = string(b)
	t, err = st.PatchCommand(id, cmdID, func(c *taskstore.CommandRecord) error {
		*c = *rec
		return nil
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

type debugTodoRequest struct {
	MaxAttempts int `json:"max_attempts,omitempty"`
}

const defaultDebugMaxAttempts = 3

func (s *Server) handleDebugTodo(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	todoID := strings.TrimSpace(r.PathValue("todoId"))
	var req debugTodoRequest
	if r.Body != nil && r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatBodyBytes))
		_ = dec.Decode(&req)
	}
	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultDebugMaxAttempts
	}
	st := taskstore.New(s.WorkspaceRoot)
	task, err := st.Load(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	td, _ := taskstore.FindTodo(task, todoID)
	if td == nil {
		writeJSONError(w, http.StatusNotFound, "todo not found")
		return
	}
	td.DebugAttempts++
	if td.DebugAttempts > maxAttempts {
		td.Status = taskstore.TodoNeedsUserInput
		td.BlockedReason = fmt.Sprintf("debug limit reached (%d attempts)", maxAttempts)
		_ = st.AppendEvent(task, taskstore.Event{Type: "debug_limit", Actor: "api", TodoID: todoID, Details: td.BlockedReason})
		_ = st.Save(task)
		writeJSON(w, http.StatusOK, map[string]any{"task": task, "limit_reached": true})
		return
	}
	td.Status = taskstore.TodoDebugging
	prompt := "Debug the failed verification for todo " + todoID + ". " + strings.TrimSpace(td.BlockedReason)
	res, err := agent.Run(r.Context(), agent.Options{
		Mode: ModeDebug(), UserText: prompt, TaskID: id, ForceWrite: true,
		WorkspaceRoot: s.WorkspaceRoot, LLM: s.LLM, Tools: s.Tools, MaxToolRounds: 8,
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = st.AppendEvent(task, taskstore.Event{
		Type: "debug_attempt", Actor: "api", TodoID: todoID,
		Details: fmt.Sprintf("attempt %d/%d", td.DebugAttempts, maxAttempts),
	})
	if len(res.WrittenRelativePaths) > 0 {
		gate, gerr := agent.RunVerificationGate(r.Context(), agent.GateOptions{
			WorkspaceRoot: s.WorkspaceRoot, WrittenRelativePaths: res.WrittenRelativePaths,
			UserPromptEnriched: prompt, AssistantReplyPrefix: res.Text,
			AutoVerify: true, AutoReview: true, MaxFixRounds: 2,
			LLM: s.LLM, Tools: s.Tools,
		})
		if gerr == nil && gate != nil && !gate.RemainingErrors {
			td.Status = taskstore.TodoComplete
			td.BlockedReason = ""
			td.Evidence = "debug fix verified"
		} else if gerr != nil {
			td.Status = taskstore.TodoFailed
			td.BlockedReason = gerr.Error()
		} else {
			td.Status = taskstore.TodoFailed
			td.BlockedReason = "verification still failing after debug attempt"
		}
		task, _ = st.RecordChangedFiles(id, res.WrittenRelativePaths)
	}
	_ = st.Save(task)
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "task": task, "gate_ran": len(res.WrittenRelativePaths) > 0})
}

// ModeDebug returns agent debug mode constant as string for JSON clients.
func ModeDebug() agent.Mode { return agent.ModeDebug }
