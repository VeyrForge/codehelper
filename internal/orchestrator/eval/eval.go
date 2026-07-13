package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/orchestrator"
	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/VeyrForge/codehelper/internal/workspacectx"
)

// Mode is one evaluation arm.
type Mode string

const (
	ModeOrchestrate Mode = "orchestrate"
	ModeManualMCP   Mode = "manual_mcp"
	ModeBaseline    Mode = "baseline_no_mcp"
)

// Task is a project-specific eval case.
type Task struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Task        string   `json:"task"`
	MustContain []string `json:"must_contain"`
}

// ArmResult is one arm's outcome.
type ArmResult struct {
	Mode        Mode                     `json:"mode"`
	Score       float64                  `json:"score"`
	Coverage    float64                  `json:"coverage"`
	Structure   float64                  `json:"structure"`
	Efficiency  float64                  `json:"efficiency"`
	TokenScore  float64                  `json:"token_score"`
	Ms          int64                    `json:"ms"`
	ToolCalls   int                      `json:"tool_calls"`
	RespTokens  int                      `json:"resp_tokens"`
	AgentTokens int                      `json:"agent_tokens,omitempty"`
	RespBytes   int                      `json:"resp_bytes"`
	Workflow    string                   `json:"workflow,omitempty"`
	Tools       []string                 `json:"tools,omitempty"`
	Gaps        []string                 `json:"gaps,omitempty"`
	Preview     string                   `json:"preview,omitempty"`
	Usage       orchestrator.UsageTotals `json:"usage,omitempty"`
}

// CaseResult compares three arms for one task.
type CaseResult struct {
	Task        Task      `json:"task"`
	Orchestrate ArmResult `json:"orchestrate"`
	ManualMCP   ArmResult `json:"manual_mcp"`
	Baseline    ArmResult `json:"baseline_no_mcp"`
	Winner      string    `json:"winner"`
	Notes       []string  `json:"notes,omitempty"`
}

// ProjectReport is all cases for one repo.
type ProjectReport struct {
	Repo         string       `json:"repo"`
	Root         string       `json:"root"`
	ProjectType  string       `json:"project_type,omitempty"`
	Anchors      []string     `json:"anchors,omitempty"`
	IndexRefresh string       `json:"index_refresh,omitempty"`
	Cases        []CaseResult `json:"cases"`
	Summary      Summary      `json:"summary"`
}

// Summary aggregates scores across cases.
type Summary struct {
	OrchestrateAvg         float64 `json:"orchestrate_avg"`
	ManualAvg              float64 `json:"manual_mcp_avg"`
	BaselineAvg            float64 `json:"baseline_avg"`
	OrchestrateTokens      int     `json:"orchestrate_tokens"`
	OrchestrateAgentTokens int     `json:"orchestrate_agent_tokens"`
	ManualTokens           int     `json:"manual_mcp_tokens"`
	BaselineTokens         int     `json:"baseline_tokens"`
	WinsOrchestrate        int     `json:"wins_orchestrate"`
	WinsManual             int     `json:"wins_manual_mcp"`
	WinsBaseline           int     `json:"wins_baseline"`
	Ties                   int     `json:"ties"`
}

// Report is the full multi-project benchmark output.
type Report struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Projects    []ProjectReport `json:"projects"`
	Overall     Summary         `json:"overall"`
}

// Runner executes benchmarks using injected tool handlers.
type Runner struct {
	Handlers     func(reg *registry.Registry) map[string]func(context.Context, map[string]any) (string, error)
	RefreshIndex bool
	Config       Config
}

// RunAll benchmarks every indexed repo in the registry.
func (r *Runner) RunAll(ctx context.Context, reg *registry.Registry) (Report, error) {
	var rep Report
	rep.GeneratedAt = time.Now().UTC()
	for _, e := range indexedRepos(reg) {
		pr, err := r.RunProject(ctx, reg, e)
		if err != nil {
			continue
		}
		rep.Projects = append(rep.Projects, pr)
		mergeSummary(&rep.Overall, pr.Summary, len(pr.Cases))
	}
	if len(rep.Projects) > 0 {
		n := float64(countCases(rep))
		if n > 0 {
			rep.Overall.OrchestrateAvg /= n
			rep.Overall.ManualAvg /= n
			rep.Overall.BaselineAvg /= n
		}
	}
	return rep, nil
}

// RunProject benchmarks one repository.
func (r *Runner) RunProject(ctx context.Context, reg *registry.Registry, repo registry.Entry) (ProjectReport, error) {
	repoCtx := workspacectx.WithRoots(repo.RootPath)
	_ = orchestrator.SetEnabled(repo.RootPath, true)
	indexNote, err := r.ensureFreshIndex(ctx, repo)
	if err != nil {
		return ProjectReport{}, err
	}
	h := r.Handlers(reg)
	inv := &orchestrator.MeteredInvoker{Inner: &handlerInvoker{h: h, repo: repo.Name}}
	store, err := orchestrator.OpenStore(repo.RootPath)
	if err != nil {
		return ProjectReport{}, err
	}
	defer store.Close()

	anchor := discoverAnchor(repoCtx, inv, repo)
	tasks := tasksForProject(anchor)
	orch := orchestrator.New(orchestrator.Options{
		RepoRoot: repo.RootPath,
		RepoName: repo.Name,
		Invoker:  inv,
		Store:    store,
		Chat:     nil,
	})

	pr := ProjectReport{
		Repo: repo.Name, Root: repo.RootPath,
		ProjectType: anchor.ProjectType, Anchors: anchor.Symbols,
		IndexRefresh: indexNote,
	}
	for _, task := range tasks {
		cr := CaseResult{Task: task}
		cr.Orchestrate = r.runOrchestrate(repoCtx, orch, inv, task)
		cr.ManualMCP = r.runManual(repoCtx, inv, h, repo, task)
		cr.Baseline = r.runBaseline(repoCtx, inv, h, repo, task)
		cr.Winner, cr.Notes = pickWinner(cr)
		pr.Cases = append(pr.Cases, cr)
		accumulate(&pr.Summary, cr)
	}
	normalizeSummary(&pr.Summary, len(tasks))
	return pr, nil
}

func (r *Runner) runOrchestrate(ctx context.Context, orch *orchestrator.Orchestrator, inv *orchestrator.MeteredInvoker, task Task) ArmResult {
	inv.Reset()
	start := time.Now()
	res, err := orch.Run(ctx, task.Task, orchestrator.Constraints{})
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return ArmResult{Mode: ModeOrchestrate, Gaps: []string{err.Error()}, Ms: ms}
	}
	blob := resultBlob(res)
	ar := scoreArm(task, blob, inv.Last, int(ms), len(res.ToolTraceCompact))
	ar.Mode = ModeOrchestrate
	ar.Workflow = string(res.Workflow)
	ar.Tools = toolNames(res.ToolTraceCompact)
	ar.Preview = preview(res.AgentBrief, 400)
	ar.Usage = inv.Last
	ar.AgentTokens = orchestrator.AgentFacingTokensFormat(res, r.effectiveOrchestrateFormat())
	if ar.AgentTokens <= 0 {
		ar.AgentTokens = orchestrator.AgentFacingTokens(res)
	}
	ar.Gaps = diagnoseGaps(task, blob, res.ContextPack, string(res.Workflow))
	return ar
}

func (r *Runner) runManual(ctx context.Context, inv *orchestrator.MeteredInvoker, h map[string]func(context.Context, map[string]any) (string, error), repo registry.Entry, task Task) ArmResult {
	inv.Reset()
	chain := manualChainForKind(task.Kind)
	start := time.Now()
	text, tools := runToolChain(ctx, inv, h, repo.Name, task.Task, chain, r.effectiveManualFormat())
	ms := time.Since(start).Milliseconds()
	ar := scoreArm(task, text, inv.Last, int(ms), len(tools))
	ar.Mode = ModeManualMCP
	ar.Tools = tools
	ar.Preview = preview(text, 400)
	ar.Usage = inv.Last
	ar.Gaps = diagnoseGaps(task, text, orchestrator.ContextPack{}, "manual:"+strings.Join(chain, "+"))
	return ar
}

func (r *Runner) runBaseline(ctx context.Context, inv *orchestrator.MeteredInvoker, h map[string]func(context.Context, map[string]any) (string, error), repo registry.Entry, task Task) ArmResult {
	inv.Reset()
	start := time.Now()
	text, tools := runBaselineChain(ctx, inv, h, repo, task.Task)
	ms := time.Since(start).Milliseconds()
	ar := scoreBaselineArm(task, baselineScoreBlob(text), inv.Last, int(ms), len(tools))
	ar.Mode = ModeBaseline
	ar.Tools = tools
	ar.Preview = preview(text, 400)
	ar.Usage = inv.Last
	ar.Gaps = []string{"no graph tools — blind file reads only"}
	if ar.Coverage < 0.2 {
		ar.Gaps = append(ar.Gaps, "symbol anchors not found in raw file reads")
	}
	return ar
}

// handlerInvoker adapts eval handlers to orchestrator.ToolInvoker.
type handlerInvoker struct {
	h    map[string]func(context.Context, map[string]any) (string, error)
	repo string
}

func (hi *handlerInvoker) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	fn, ok := hi.h[name]
	if !ok {
		return "", fmt.Errorf("tool %q not available", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	if hi.repo != "" {
		if s, _ := args["repo"].(string); strings.TrimSpace(s) == "" {
			args["repo"] = hi.repo
		}
	}
	return fn(ctx, args)
}

func indexedRepos(reg *registry.Registry) []registry.Entry {
	var out []registry.Entry
	for _, e := range reg.List() {
		if e.RootPath == "" {
			continue
		}
		if e.Name == "ch-init-test" || strings.HasPrefix(e.RootPath, "/tmp/") {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.RootPath, ".git")); err != nil {
			continue
		}
		idx := paths.RepoIndexDir(e.RootPath)
		if _, err := os.Stat(filepath.Join(idx, "graph.db")); err != nil {
			if _, err2 := os.Stat(filepath.Join(e.RootPath, ".codehelper", "graph.db")); err2 != nil {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func countCases(rep Report) int {
	n := 0
	for _, p := range rep.Projects {
		n += len(p.Cases)
	}
	return n
}

func mergeSummary(dst *Summary, src Summary, caseCount int) {
	if caseCount <= 0 {
		return
	}
	f := float64(caseCount)
	dst.OrchestrateAvg += src.OrchestrateAvg * f
	dst.ManualAvg += src.ManualAvg * f
	dst.BaselineAvg += src.BaselineAvg * f
	dst.OrchestrateTokens += src.OrchestrateTokens
	dst.OrchestrateAgentTokens += src.OrchestrateAgentTokens
	dst.ManualTokens += src.ManualTokens
	dst.BaselineTokens += src.BaselineTokens
	dst.WinsOrchestrate += src.WinsOrchestrate
	dst.WinsManual += src.WinsManual
	dst.WinsBaseline += src.WinsBaseline
	dst.Ties += src.Ties
}

func accumulate(s *Summary, cr CaseResult) {
	s.OrchestrateAvg += cr.Orchestrate.Score
	s.ManualAvg += cr.ManualMCP.Score
	s.BaselineAvg += cr.Baseline.Score
	s.OrchestrateTokens += cr.Orchestrate.RespTokens
	s.OrchestrateAgentTokens += cr.Orchestrate.AgentTokens
	s.ManualTokens += cr.ManualMCP.RespTokens
	s.BaselineTokens += cr.Baseline.RespTokens
	switch cr.Winner {
	case string(ModeOrchestrate):
		s.WinsOrchestrate++
	case string(ModeManualMCP):
		s.WinsManual++
	case string(ModeBaseline):
		s.WinsBaseline++
	default:
		s.Ties++
	}
}

func normalizeSummary(s *Summary, n int) {
	if n <= 0 {
		return
	}
	f := float64(n)
	s.OrchestrateAvg /= f
	s.ManualAvg /= f
	s.BaselineAvg /= f
}

func pickWinner(cr CaseResult) (string, []string) {
	scores := map[string]float64{
		string(ModeOrchestrate): cr.Orchestrate.Score,
		string(ModeManualMCP):   cr.ManualMCP.Score,
		string(ModeBaseline):    cr.Baseline.Score,
	}
	best, bestV := "", -1.0
	second := -1.0
	for k, v := range scores {
		if v > bestV {
			second = bestV
			bestV = v
			best = k
		} else if v > second {
			second = v
		}
	}
	var notes []string
	notes = append(notes, fmt.Sprintf("winner=%s (highest weighted score: 40%% coverage + 30%% structure + 15%% efficiency + 15%% token use)", best))
	if bestV-second < 0.05 {
		// Tie-break: prefer orchestrate when coverage is comparable and tokens are lower.
		if cr.Orchestrate.RespTokens > 0 && cr.ManualMCP.RespTokens > 0 &&
			cr.Orchestrate.Coverage >= cr.ManualMCP.Coverage*0.92 &&
			cr.Orchestrate.RespTokens < cr.ManualMCP.RespTokens {
			return string(ModeOrchestrate), append(notes, "tie broken: orchestrate comparable coverage at lower token cost")
		}
		if cr.ManualMCP.RespTokens > 0 && cr.Orchestrate.RespTokens > cr.ManualMCP.RespTokens*2 &&
			cr.ManualMCP.Coverage <= cr.Orchestrate.Coverage+0.05 {
			return string(ModeOrchestrate), append(notes, "tie broken: orchestrate similar quality at much lower token cost")
		}
		return "tie", append(notes, "scores within 5% — compare tokens for tie-break")
	}
	if cr.Orchestrate.RespTokens > 0 && cr.ManualMCP.RespTokens > 0 {
		if cr.Orchestrate.RespTokens < cr.ManualMCP.RespTokens {
			notes = append(notes, fmt.Sprintf("orchestrate used %d fewer tokens than manual", cr.ManualMCP.RespTokens-cr.Orchestrate.RespTokens))
		}
	}
	if cr.Baseline.Coverage < cr.Orchestrate.Coverage {
		notes = append(notes, "baseline lacks graph search — lower coverage expected")
	}
	return best, notes
}

func resultBlob(res *orchestrator.Result) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	if res.AgentBrief != "" {
		b.WriteString(res.AgentBrief)
	}
	for _, ex := range res.ContextPack.SourceExcerpts {
		b.WriteString(" ")
		b.WriteString(ex)
	}
	pack := res.ContextPack
	b.WriteString(strings.Join(pack.Locations, " "))
	b.WriteString(" ")
	b.WriteString(strings.Join(pack.Files, " "))
	b.WriteString(" ")
	b.WriteString(strings.Join(pack.Symbols, " "))
	if pack.OrientLine != "" {
		b.WriteString(" ")
		b.WriteString(pack.OrientLine)
	}
	b.WriteString(" ")
	b.WriteString(strings.Join(pack.Steps, " "))
	b.WriteString(" ")
	b.WriteString(strings.Join(pack.Decisions, " "))
	b.WriteString(" ")
	b.WriteString(strings.Join(pack.Verification, " "))
	for _, s := range pack.Snippets {
		b.WriteString(" ")
		b.WriteString(s)
	}
	for _, t := range res.ToolTraceCompact {
		b.WriteString(" ")
		b.WriteString(t.Result)
	}
	return strings.ToLower(b.String())
}

func toolNames(trace []orchestrator.CompactTrace) []string {
	out := make([]string, 0, len(trace))
	for _, t := range trace {
		out = append(out, t.Tool)
	}
	return out
}

func preview(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// baselineScoreBlob strips the echoed task text so baseline coverage reflects
// file reads only, not the task string itself.
func baselineScoreBlob(blob string) string {
	if idx := strings.Index(blob, "task_without_graph:"); idx >= 0 {
		return strings.ToLower(blob[:idx])
	}
	return strings.ToLower(blob)
}

func (r *Runner) effectiveManualFormat() string {
	if f := strings.ToLower(strings.TrimSpace(r.Config.ManualFormat)); f != "" {
		return f
	}
	return "toon"
}

func (r *Runner) effectiveOrchestrateFormat() string {
	if f := strings.ToLower(strings.TrimSpace(r.Config.OrchestrateFormat)); f != "" {
		return f
	}
	return "toon"
}
