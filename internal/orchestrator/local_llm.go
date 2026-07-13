package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/VeyrForge/codehelper/internal/enrich"
	"github.com/VeyrForge/codehelper/internal/green"
	"github.com/VeyrForge/codehelper/internal/llm"
)

var classifyCache sync.Map // key: task hash → Plan

const classifySkipConfidence = 0.80

// LocalChat is the bounded chat surface for orchestration (classify, rewrite, judge).
// Implemented by enrich.FromEnv (green-engine chat server) or llm.ConfigFromEnv fallback.
type LocalChat interface {
	Complete(ctx context.Context, system, user string) (string, error)
	Model() string
}

type llmChatAdapter struct {
	client *llm.Client
	model  string
}

func (a *llmChatAdapter) Model() string { return a.model }

func (a *llmChatAdapter) Complete(ctx context.Context, system, user string) (string, error) {
	res, err := a.client.Complete(ctx, llm.ChatRequest{
		Model: a.model,
		Messages: []llm.Message{
			llm.TextMessage("system", system),
			llm.TextMessage("user", user),
		},
	})
	if err != nil {
		return "", err
	}
	return res.Message.Text(), nil
}

type enrichChatAdapter struct{ inner enrich.Chat }

func (a enrichChatAdapter) Model() string { return a.inner.Model() }

func (a enrichChatAdapter) Complete(ctx context.Context, system, user string) (string, error) {
	return a.inner.Complete(ctx, system, user)
}

// ResolveLocalChat wires the green engine (if enabled) and returns a chat client
// when a local model endpoint is configured. Order:
//  1. green.LoadAndExport → CODEHELPER_ENRICH_URL (ge-managed chat / compressed GGUF)
//  2. ~/.codehelper/llm.json + CODEHELPER_LLM_* (Ollama, OpenAI-compatible)
//
// Returns nil when no local LLM is available — orchestration stays deterministic.
func ResolveLocalChat() LocalChat {
	_, _ = green.LoadAndExport()
	if c := enrich.FromEnv(); c != nil {
		return enrichChatAdapter{inner: c}
	}
	cfg := llm.ConfigFromEnv()
	if !cfg.Ready() {
		return nil
	}
	return &llmChatAdapter{client: llm.NewClient(cfg), model: cfg.Model}
}

// llmPlanSchema is the strict JSON the local model may suggest; Go validates and picks tools.
type llmPlanSchema struct {
	Intent     string   `json:"intent"`
	Workflow   string   `json:"workflow"`
	Confidence float64  `json:"confidence"`
	Entities   []string `json:"entities"`
	Queries    []string `json:"queries"`
	Avoid      []string `json:"avoid,omitempty"`
}

const classifySystemPrompt = `You route code investigation tasks. Reply with ONLY a JSON object:
{"intent":"bugfix|feature|refactor|explain|review","workflow":"bugfix_triage|feature_scope|refactor_impact|explain_code|review_gate","confidence":0.0-1.0,"entities":["..."],"queries":["concrete search terms"],"avoid":["areas to skip"]}
Rules: entities and queries must use identifiers a codebase search would match. Do not invent file paths. If unsure, lower confidence. No prose outside JSON.`

// ShouldSkipLLMClassify reports when deterministic routing is confident enough
// to avoid a local-LLM round trip (saves CPU/latency on obvious tasks).
func ShouldSkipLLMClassify(plan Plan) bool {
	return plan.Confidence >= classifySkipConfidence
}

// ClassifyTaskHybrid uses the local LLM when available, else deterministic ClassifyTask.
func ClassifyTaskHybrid(ctx context.Context, chat LocalChat, task string, constraints Constraints, memoryRules []string) Plan {
	base := ClassifyTask(task, constraints, memoryRules)
	if chat == nil || ShouldSkipLLMClassify(base) {
		return base
	}
	if key := classifyCacheKey(task, constraints); key != "" {
		if v, ok := classifyCache.Load(key); ok {
			if cached, ok := v.(Plan); ok {
				return cached
			}
		}
	}
	user := buildClassifyUserPrompt(task, constraints, memoryRules)
	raw, err := chat.Complete(ctx, classifySystemPrompt, user)
	if err != nil {
		return base
	}
	parsed, err := parseLLMPlan(raw)
	if err != nil {
		return base
	}
	out := validateLLMPlan(parsed, base, constraints)
	if key := classifyCacheKey(task, constraints); key != "" {
		classifyCache.Store(key, out)
	}
	return out
}

func classifyCacheKey(task string, constraints Constraints) string {
	task = strings.TrimSpace(strings.ToLower(task))
	if task == "" {
		return ""
	}
	h := sha256.Sum256([]byte(task + "|" + strings.TrimSpace(constraints.Instruction)))
	return hex.EncodeToString(h[:8])
}

func buildClassifyUserPrompt(task string, constraints Constraints, memoryRules []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task: %s\n", task)
	if constraints.Instruction != "" {
		fmt.Fprintf(&b, "Correction: %s\n", constraints.Instruction)
	}
	if len(constraints.PreferredEntities) > 0 {
		fmt.Fprintf(&b, "Prioritize: %s\n", strings.Join(constraints.PreferredEntities, ", "))
	}
	if len(constraints.AvoidEntities) > 0 {
		fmt.Fprintf(&b, "Avoid: %s\n", strings.Join(constraints.AvoidEntities, ", "))
	}
	for _, r := range memoryRules {
		fmt.Fprintf(&b, "Memory: %s\n", r)
	}
	return b.String()
}

func parseLLMPlan(raw string) (llmPlanSchema, error) {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return llmPlanSchema{}, fmt.Errorf("no JSON object")
	}
	var p llmPlanSchema
	if err := json.Unmarshal([]byte(raw[start:end+1]), &p); err != nil {
		return llmPlanSchema{}, err
	}
	return p, nil
}

func validateLLMPlan(p llmPlanSchema, fallback Plan, constraints Constraints) Plan {
	out := fallback
	switch Intent(strings.ToLower(strings.TrimSpace(p.Intent))) {
	case IntentBugfix, IntentFeature, IntentRefactor, IntentExplain, IntentReview:
		out.Intent = Intent(strings.ToLower(strings.TrimSpace(p.Intent)))
	}
	switch Workflow(strings.ToLower(strings.TrimSpace(p.Workflow))) {
	case WorkflowBugfixTriage, WorkflowFeatureScope, WorkflowRefactorImpact, WorkflowExplainCode, WorkflowReviewGate:
		out.Workflow = Workflow(strings.ToLower(strings.TrimSpace(p.Workflow)))
	}
	if p.Confidence > 0 && p.Confidence <= 1 {
		out.Confidence = p.Confidence
	}
	if ents := trimNonEmpty(p.Entities); len(ents) > 0 {
		out.Entities = uniqueAppend(constraints.PreferredEntities, ents...)
	}
	if qs := trimNonEmpty(p.Queries); len(qs) > 0 {
		out.Queries = qs
	}
	if av := trimNonEmpty(p.Avoid); len(av) > 0 {
		out.Avoid = uniqueAppend(constraints.AvoidEntities, av...)
	}
	// Ensure workflow matches intent if model sent a mismatched pair.
	out.Workflow = workflowForIntent(out.Intent)
	return out
}

func trimNonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

const compressAgentSystemPrompt = `Compress code investigation notes for a cloud coding agent.
Rules: bullet markdown only; keep every file path and symbol name; max ~500 words; mark weak evidence [UNCERTAIN]; no JSON; no preamble.`

// meteringChat records estimated local-LLM token usage separately from MCP metering.
type meteringChat struct {
	inner LocalChat
	usage *UsageTotals
}

func wrapMeteringChat(inner LocalChat, usage *UsageTotals) LocalChat {
	if inner == nil || usage == nil {
		return inner
	}
	return meteringChat{inner: inner, usage: usage}
}

func (m meteringChat) Model() string { return m.inner.Model() }

func (m meteringChat) Complete(ctx context.Context, system, user string) (string, error) {
	m.usage.ToolCalls++
	promptLen := len(system) + len(user)
	m.usage.ReqBytes += promptLen
	m.usage.RespTokens += estimateTokens(promptLen)
	out, err := m.inner.Complete(ctx, system, user)
	if err != nil {
		return "", err
	}
	m.usage.RespBytes += len(out)
	m.usage.RespTokens += estimateTokens(len(out))
	return out, nil
}

// CompressForAgent optionally tightens an already-compact brief with the local LLM.
// Local work stays off the cloud bill; only the returned brief counts toward agent tokens.
func CompressForAgent(ctx context.Context, chat LocalChat, task string, plan Plan, brief string, maxChars int) string {
	brief = strings.TrimSpace(brief)
	if chat == nil || brief == "" || len(brief) <= maxChars {
		return brief
	}
	user := fmt.Sprintf("Task: %s\nWorkflow: %s\nConfidence: %.2f\n\nBrief to compress:\n%s",
		task, plan.Workflow, plan.Confidence, brief)
	out, err := chat.Complete(ctx, compressAgentSystemPrompt, user)
	if err != nil || strings.TrimSpace(out) == "" {
		return truncate(brief, maxChars*4/3)
	}
	out = strings.TrimSpace(out)
	if len(out) > maxChars*2 {
		return truncate(out, maxChars*2)
	}
	return out
}
