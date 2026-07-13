package agentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/VeyrForge/codehelper/internal/agent"
	"github.com/VeyrForge/codehelper/internal/plan"
	"github.com/VeyrForge/codehelper/internal/taskstore"
)

func (s *Server) buildPlan(ctx context.Context, in plan.Input, enrichLLM *bool, quick bool) (plan.Output, error) {
	enrich := !quick && s.LLM.Ready()
	if enrichLLM != nil {
		enrich = *enrichLLM && !quick
	}
	return plan.BuildEnriched(ctx, in, plan.EnrichConfig{
		LLM: s.LLM, Tools: s.Tools, EnrichLLM: enrich,
	})
}

type taskCreateRequest struct {
	Request      string `json:"request"`
	Title        string `json:"title,omitempty"`
	Mode         string `json:"mode,omitempty"`
	ChangedArea  string `json:"changed_area,omitempty"`
	ProjectType  string `json:"project_type,omitempty"`
	Quick        bool   `json:"quick,omitempty"`
	EnrichLLM    *bool  `json:"enrich_llm,omitempty"`
	PriorContext string `json:"prior_context,omitempty"`
}

type taskFromPlanRequest struct {
	Request string           `json:"request"`
	Title   string           `json:"title,omitempty"`
	Mode    string           `json:"mode,omitempty"`
	Plan    taskstore.Plan   `json:"plan"`
	Todos   []taskstore.Todo `json:"todos"`
	// PlanText optionally carries assistant markdown; parsed when plan/todos empty.
	PlanText string `json:"plan_text,omitempty"`
}

type planUpdateRequest struct {
	Plan  taskstore.Plan   `json:"plan"`
	Todos []taskstore.Todo `json:"todos"`
}

type todoPatchRequest struct {
	UserNotes string `json:"user_notes,omitempty"`
	Status    string `json:"status,omitempty"`
	Title     string `json:"title,omitempty"`
}

type todoExecuteRequest struct {
	Verify        bool `json:"verify,omitempty"`
	MaxToolRounds int  `json:"max_tool_rounds,omitempty"`
	MaxFixRounds  int  `json:"max_fix_rounds,omitempty"`
}

func (s *Server) registerTaskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/tasks", s.handleListTasks)
	mux.HandleFunc("POST /v1/tasks", s.handleCreateTask)
	mux.HandleFunc("POST /v1/tasks/from-plan", s.handleCreateTaskFromPlan)
	mux.HandleFunc("GET /v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("PUT /v1/tasks/{id}/plan", s.handleUpdatePlan)
	mux.HandleFunc("PATCH /v1/tasks/{id}/todos/{todoId}", s.handlePatchTodo)
	mux.HandleFunc("POST /v1/tasks/{id}/todos/{todoId}/execute", s.handleExecuteTodo)
	mux.HandleFunc("GET /v1/tasks/{id}/next-todo", s.handleNextTodo)
	mux.HandleFunc("POST /v1/tasks/{id}/approve-all", s.handleApproveAll)
	mux.HandleFunc("GET /v1/tasks/{id}/timeline", s.handleTaskTimeline)
}

func (s *Server) handleListTasks(w http.ResponseWriter, _ *http.Request) {
	st := taskstore.New(s.WorkspaceRoot)
	ids, err := st.List()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tasks := make([]*taskstore.Task, 0, len(ids))
	for _, id := range ids {
		if t, err := st.Load(id); err == nil {
			tasks = append(tasks, t)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req taskCreateRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	request := strings.TrimSpace(req.Request)
	if request == "" {
		writeJSONError(w, http.StatusBadRequest, "request is required")
		return
	}
	out, err := s.buildPlan(r.Context(), plan.Input{
		Request: request, ProjectType: req.ProjectType,
		ChangedArea: req.ChangedArea, RepoRoot: s.WorkspaceRoot, Quick: req.Quick,
	}, req.EnrichLLM, req.Quick)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	st := taskstore.New(s.WorkspaceRoot)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = request
	}
	t, err := st.Create(title, request, req.Mode)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	t.Plan = out.Plan
	t.Todos = out.Todos
	t.DecisionPoints = out.DecisionPoints
	if pc := strings.TrimSpace(req.PriorContext); pc != "" {
		if strings.TrimSpace(t.Plan.CurrentUnderstanding) != "" {
			t.Plan.CurrentUnderstanding = pc + "\n\n" + t.Plan.CurrentUnderstanding
		} else {
			t.Plan.CurrentUnderstanding = pc
		}
	}
	_ = st.AppendEvent(t, taskstore.Event{Type: "plan_created", Actor: "api"})
	if err := st.Save(t); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleCreateTaskFromPlan(w http.ResponseWriter, r *http.Request) {
	var req taskFromPlanRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	request := strings.TrimSpace(req.Request)
	if request == "" {
		writeJSONError(w, http.StatusBadRequest, "request is required")
		return
	}
	planDoc := req.Plan
	todos := req.Todos
	if strings.TrimSpace(planDoc.Goal) == "" && len(todos) == 0 && strings.TrimSpace(req.PlanText) != "" {
		parsed, err := plan.ParsePersistPayload(req.PlanText)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		planDoc = parsed.Plan
		todos = parsed.Todos
	}
	if strings.TrimSpace(planDoc.Goal) == "" {
		planDoc.Goal = request
	}
	st := taskstore.New(s.WorkspaceRoot)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = request
	}
	t, err := st.Create(title, request, req.Mode)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	t.Plan = planDoc
	if todos == nil {
		todos = []taskstore.Todo{}
	}
	t.Todos = todos
	_ = st.AppendEvent(t, taskstore.Event{Type: "plan_created", Actor: "api", Details: "from-plan"})
	if err := st.Save(t); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	t, err := taskstore.New(s.WorkspaceRoot).Load(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleUpdatePlan(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var req planUpdateRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	t, err := taskstore.New(s.WorkspaceRoot).ReplacePlan(id, req.Plan, req.Todos)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handlePatchTodo(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	todoID := strings.TrimSpace(r.PathValue("todoId"))
	var req todoPatchRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		return
	}
	t, err := taskstore.New(s.WorkspaceRoot).PatchTodo(id, todoID, func(td *taskstore.Todo) error {
		if req.UserNotes != "" {
			td.UserNotes = req.UserNotes
		}
		if req.Status != "" {
			td.Status = req.Status
		}
		if req.Title != "" {
			td.Title = req.Title
		}
		return nil
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleExecuteTodo(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	todoID := strings.TrimSpace(r.PathValue("todoId"))
	req := todoExecuteRequest{Verify: true}
	if r.ContentLength != 0 {
		if err := decodeJSONBody(w, r, &req); err != nil {
			return
		}
	}

	st := taskstore.New(s.WorkspaceRoot)
	task, err := st.Load(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	execRes, task, err := agent.ExecuteTodo(r.Context(), agent.ExecuteTodoOptions{
		WorkspaceRoot: s.WorkspaceRoot,
		Task:          task,
		TodoID:        todoID,
		LLM:           s.LLM,
		Tools:         s.Tools,
		Verify:        req.Verify,
		MaxToolRounds: req.MaxToolRounds,
		MaxFixRounds:  req.MaxFixRounds,
		AutoVerify:    true,
		AutoReview:    true,
	})
	resp := map[string]any{"execution": execRes, "task": task}
	if err != nil {
		resp["error"] = err.Error()
		b, _ := json.MarshalIndent(resp, "", "  ")
		writeJSONError(w, http.StatusBadRequest, string(b))
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleTaskTimeline(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	t, err := taskstore.New(s.WorkspaceRoot).Load(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task_id": id, "events": t.Events})
}

func (s *Server) handleNextTodo(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	t, err := taskstore.New(s.WorkspaceRoot).Load(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	auto := r.URL.Query().Get("auto_approve_planned") == "true"
	td, idx := taskstore.NextExecutable(t, auto)
	if td == nil {
		writeJSON(w, http.StatusOK, map[string]any{"task_id": id, "todo": nil, "index": -1})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task_id": id, "todo": td, "index": idx})
}

func (s *Server) handleApproveAll(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	t, err := taskstore.New(s.WorkspaceRoot).ApproveAllPlanned(id)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}
