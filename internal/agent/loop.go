package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/llm"
	"github.com/VeyrForge/codehelper/internal/retrieval"
	"github.com/VeyrForge/codehelper/internal/skills"
)

// Turn is one prior user/assistant exchange entry.
type Turn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Hooks stream loop progress to clients (panel, chat participant, CLI, API).
// All fields are optional.
type Hooks struct {
	OnRound                func(iteration, maxIterations int)
	OnAssistantModelTiming func(elapsedSec float64, willUseTools bool)
	OnAssistantPlanning    func(text string, toolNames []string)
	OnAssistantToken       func(chunk string)
	OnToolStart            func(name string, args map[string]any)
	OnToolComplete         func(name string, args map[string]any, result string)
	OnWorkspaceEdit        func(edit WorkspaceEditEvent)
	OnTokenUsage           func(summary TokenUsageSummary)
}

// Options configures one agent chat run.
type Options struct {
	Mode          Mode
	UserText      string
	PriorTurns    []Turn
	Hooks         Hooks
	Log           func(string)
	LLM           llm.Config
	Tools         ToolCaller
	WorkspaceRoot string
	TaskID        string
	ForceWrite    bool
	// MaxToolRounds caps LLM rounds per turn (default 48, hard cap 128).
	MaxToolRounds int
	// PrefetchBroadAskEvidence pre-seeds MCP evidence for broad first asks.
	PrefetchBroadAskEvidence bool
	// Stream forces SSE streaming even without an OnAssistantToken hook.
	Stream bool
}

// Result is the outcome of one agent chat run.
type Result struct {
	Text                 string             `json:"text"`
	FinalAlreadyStreamed bool               `json:"final_already_streamed"`
	WrittenRelativePaths []string           `json:"written_relative_paths,omitempty"`
	TokenUsage           *TokenUsageSummary `json:"token_usage,omitempty"`
}

// TokenUsageSummary aggregates provider usage and context size for one agent turn.
type TokenUsageSummary struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	EstimatedContext int `json:"estimated_context_tokens"`
	MessageCount     int `json:"message_count"`
	LlmRounds        int `json:"llm_rounds"`
}

const (
	maxMetaAnswerNudges        = 3
	maxSchemaLeakNudges        = 2
	maxDeferNudges             = 3
	maxConsecutiveUnknownTools = 3
	defaultMaxToolRounds       = 48
	hardMaxToolRounds          = 128
	toolArgLogPreviewLimit     = 600
)

// Run executes the multi-round LLM agent loop. It is a faithful port of the
// original VS Code host orchestration: native + embedded tool calls, mode
// gating, dedupe of repeated retrieval, grounding/breadth/meta/schema-leak
// nudges, a finalize pass, and a deterministic overview fallback.
func Run(ctx context.Context, opts Options) (*Result, error) {
	log := opts.Log
	if log == nil {
		log = func(string) {}
	}
	hooks := opts.Hooks
	mode := opts.Mode
	if mode == "" {
		mode = ModeAsk
	}
	if opts.Tools == nil {
		return nil, fmt.Errorf("agent: Options.Tools is required")
	}
	if !opts.LLM.Ready() {
		return nil, fmt.Errorf("configure LLM base URL (or chat URL), model, and API key")
	}

	allowedTools := allowedToolsForMode(mode)
	advertisedTools := toolsForMode(mode)
	allowWrites, writeBlockReason := WritesAllowed(mode, opts.WorkspaceRoot, opts.TaskID, opts.ForceWrite)

	client := llm.NewClient(opts.LLM)
	model := opts.LLM.Model
	temperature := opts.LLM.Temperature

	maxIter := opts.MaxToolRounds
	if maxIter <= 0 {
		maxIter = defaultMaxToolRounds
	}
	if maxIter > hardMaxToolRounds {
		maxIter = hardMaxToolRounds
	}

	useStream := opts.Stream || hooks.OnAssistantToken != nil

	var writtenRelativePaths []string

	messages := []llm.Message{llm.TextMessage("system", buildSystemPrompt(mode, opts.WorkspaceRoot)+skills.PromptAddendum(opts.UserText, ""))}
	for _, t := range opts.PriorTurns {
		role := t.Role
		if role != "user" && role != "assistant" {
			continue
		}
		messages = append(messages, llm.TextMessage(role, t.Text))
	}
	messages = append(messages, llm.TextMessage("user", opts.UserText))

	qCore := userQuestionCore(opts.UserText)
	priorAssistant := false
	for _, t := range opts.PriorTurns {
		if t.Role == "assistant" && strings.TrimSpace(t.Text) != "" {
			priorAssistant = true
			break
		}
	}
	if opts.PrefetchBroadAskEvidence && !isPlainSocialOrOffTopic(qCore) && isProbablyBroadExplorationAsk(qCore) && !priorAssistant {
		if seeded := buildPrefetchedBroadAskEvidence(ctx, opts.Tools, qCore, model, log); seeded != "" {
			seedMsg := llm.TextMessage("system", "<host_prefetched_evidence>\n"+seeded+"\n</host_prefetched_evidence>")
			messages = append(messages[:1], append([]llm.Message{seedMsg}, messages[1:]...)...)
		}
	}

	executedTools := map[string]bool{}
	toolCallCounts := map[string]int{}
	implementationReadCount := 0
	groundingNudgeCount := 0
	breadthNudgeCount := 0
	deferredNudgeCount := 0
	metaAnswerNudgeCount := 0
	schemaLeakNudgeCount := 0
	successfulRetrievalKeys := map[string]bool{}
	listDirHits := map[string]int{}

	pushUser := func(content string) {
		messages = append(messages, llm.TextMessage("user", content))
	}
	pushAssistantText := func(content string) {
		messages = append(messages, llm.TextMessage("assistant", content))
	}
	pushToolResult := func(id, content string) {
		messages = append(messages, llm.Message{Role: "tool", ToolCallID: id, Content: &content})
	}

	iterations := 0
	var totalUsage llm.Usage
	emitUsage := func(roundMessages []llm.Message, rounds int) {
		summary := TokenUsageSummary{
			PromptTokens:     totalUsage.PromptTokens,
			CompletionTokens: totalUsage.CompletionTokens,
			TotalTokens:      totalUsage.TotalTokens,
			EstimatedContext: estimateMessagesTokens(roundMessages),
			MessageCount:     len(roundMessages),
			LlmRounds:        rounds,
		}
		if hooks.OnTokenUsage != nil {
			hooks.OnTokenUsage(summary)
		}
	}
	makeResult := func(text string, streamed bool) *Result {
		emitUsage(messages, iterations)
		summary := TokenUsageSummary{
			PromptTokens:     totalUsage.PromptTokens,
			CompletionTokens: totalUsage.CompletionTokens,
			TotalTokens:      totalUsage.TotalTokens,
			EstimatedContext: estimateMessagesTokens(messages),
			MessageCount:     len(messages),
			LlmRounds:        iterations,
		}
		return &Result{
			Text:                 text,
			FinalAlreadyStreamed: streamed,
			WrittenRelativePaths: writtenRelativePaths,
			TokenUsage:           &summary,
		}
	}

	complete := func(req llm.ChatRequest) (llm.Message, bool, error) {
		streamed := false
		var choice llm.Message
		var err error
		if useStream {
			// Buffer streamed text: rounds that end in tool calls often
			// include planning prose and fenced tool JSON — clients should
			// not mirror that into the activity stream.
			var cr llm.CompletionResult
			cr, err = client.CompleteStreaming(ctx, req, nil)
			if err == nil {
				streamed = true
				choice = cr.Message
				totalUsage.Add(cr.Usage)
				emitUsage(req.Messages, iterations)
			} else {
				log(fmt.Sprintf("[LLM] streaming failed, retrying non-streaming: %v", err))
				cr, err = client.Complete(ctx, req)
				if err == nil {
					choice = cr.Message
					totalUsage.Add(cr.Usage)
					emitUsage(req.Messages, iterations)
				}
			}
		} else {
			var cr llm.CompletionResult
			cr, err = client.Complete(ctx, req)
			if err == nil {
				choice = cr.Message
				totalUsage.Add(cr.Usage)
				emitUsage(req.Messages, iterations)
			}
		}
		return choice, streamed, err
	}

	consecutiveUnknownToolCalls := 0
	for iterations < maxIter {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		iterations++
		if hooks.OnRound != nil {
			hooks.OnRound(iterations, maxIter)
		}
		forceToolUseThisRound := !isPlainSocialOrOffTopic(qCore) &&
			(isProbablyBroadExplorationAsk(qCore) || mentionsCodeOrWorkspaceIntent(qCore)) &&
			!hasGroundingDepth(executedTools) &&
			iterations <= 2

		tools := advertisedTools
		if llm.IsOllamaNativeChatURL(client.URL) && isPlainSocialOrOffTopic(qCore) {
			tools = nil
		}
		parallel := false
		req := llm.ChatRequest{
			Model:             model,
			Messages:          messages,
			Tools:             tools,
			ToolChoice:        "auto",
			ParallelToolCalls: &parallel,
			Temperature:       temperature,
		}
		if forceToolUseThisRound {
			req.ToolChoice = "required"
		}

		log(fmt.Sprintf("[LLM] POST %s (round %d/%d, tools on, stream=%v, tool_choice=%s)", client.URL, iterations, maxIter, useStream, req.ToolChoice))

		llmT0 := time.Now()
		choice, _, err := complete(req)
		if err != nil {
			if (req.ToolChoice == "required" || req.ParallelToolCalls != nil) && llm.ShouldDowngradeToolControls(err) {
				log("[LLM] provider rejected strict tool controls; retrying with tool_choice=auto")
				req.ToolChoice = "auto"
				req.ParallelToolCalls = nil
				choice, _, err = complete(req)
			}
			if err != nil {
				return nil, err
			}
		}
		if llm.IsOllamaNativeChatURL(client.URL) && len(req.Tools) > 0 && ollamaErrorTextReply(choice) {
			log("[LLM] Ollama returned error-like text with tools advertised; retrying without tools")
			req.Tools = nil
			req.ToolChoice = ""
			req.ParallelToolCalls = nil
			choice, _, err = complete(req)
			if err != nil {
				return nil, err
			}
		}
		llmElapsedSec := time.Since(llmT0).Seconds()

		var nativeCalls []llm.ToolCall
		for _, tc := range choice.ToolCalls {
			if tc.Type == "function" {
				nativeCalls = append(nativeCalls, tc)
			}
		}
		toolCalls := nativeCalls
		assistantHistoryContent := choice.Content

		if len(nativeCalls) == 0 {
			embedded := parseEmbeddedToolRequests(choice.Text(), allowedTools)
			if len(embedded) > 0 {
				toolCalls = embedded
				stripped := strings.TrimSpace(stripEmbeddedToolJSONBlocks(choice.Text(), allowedTools))
				if stripped != "" {
					parsed := parseJSONObjectCandidate(stripped)
					if parsed != nil && embeddedToolFromObject(parsed, allowedTools) != nil {
						assistantHistoryContent = nil
					} else {
						assistantHistoryContent = &stripped
					}
				} else {
					assistantHistoryContent = nil
				}
				log(fmt.Sprintf("[LLM] parsed %d tool request(s) from assistant text (no native tool_calls — executing MCP anyway)", len(embedded)))
			}
		}

		if hooks.OnAssistantModelTiming != nil {
			hooks.OnAssistantModelTiming(llmElapsedSec, len(toolCalls) > 0)
		}

		if len(toolCalls) == 0 {
			rawText := strings.TrimSpace(choice.Text())
			displayText := formatAssistantReplyForUser(rawText, allowedTools)
			log("[LLM] assistant message (no tool calls)")

			allowHostNudges := !isPlainSocialOrOffTopic(qCore) &&
				(hasUserAttachedPaths(opts.UserText) || mentionsCodeOrWorkspaceIntent(qCore))

			if allowHostNudges && displayText != "" && looksLikeNonAnswerToolCommentary(displayText) &&
				metaAnswerNudgeCount < maxMetaAnswerNudges && iterations < maxIter {
				metaAnswerNudgeCount++
				log(fmt.Sprintf("[LLM] meta-answer nudge #%d (iter %d/%d)", metaAnswerNudgeCount, iterations, maxIter))
				pushAssistantText(choice.Text())
				pushUser("[Orchestrator — answer the user]\nYou summarized or described **tool JSON** instead of answering the user's question in **normal Markdown**.\n" +
					"Write a direct reply (what the project does, main subsystems, cited `path`s). Do **not** wrap the whole message in `{\"response\":…}`.\n" +
					"Do **not** start with “The provided JSON …”. Reuse **README** / **query** / **query** results already in this thread.")
				continue
			}

			if allowHostNudges && displayText != "" && looksLikeToolSchemaLeak(displayText) &&
				schemaLeakNudgeCount < maxSchemaLeakNudges && iterations < maxIter {
				schemaLeakNudgeCount++
				log(fmt.Sprintf("[LLM] tool-schema nudge #%d (iter %d/%d)", schemaLeakNudgeCount, iterations, maxIter))
				pushAssistantText(choice.Text())
				pushUser("[Orchestrator — tool calls only]\nYou printed a tool schema/object (for example `function_name`, typed `parameters`) instead of a real answer or real tool call.\n" +
					"Do this now: either (a) emit valid tool calls using `name` + real argument values, or (b) write the final Markdown answer from existing tool evidence.\n" +
					"Do not output function schema JSON, placeholder types, or protocol docs.")
				continue
			}

			if allowHostNudges && rawText != "" &&
				(looksLikeDeferredWorkAnswer(rawText) || looksLikeUserMustRunToolsInstruction(rawText)) &&
				deferredNudgeCount < maxDeferNudges && iterations < maxIter {
				deferredNudgeCount++
				log(fmt.Sprintf("[LLM] deferral nudge #%d (iter %d/%d)", deferredNudgeCount, iterations, maxIter))
				pushAssistantText(choice.Text())
				pushUser("[Orchestrator — no play-by-play]\nYou replied with **plans for later** tools or steps instead of a **finished** answer or immediate **tool_calls**.\n" +
					"Do one of the following **now**: (a) Issue the concrete **tool_calls** you described (no narration); or (b) If this thread already has enough tool JSON, write the **complete** Markdown answer with cited paths.\n" +
					"Do **not** say ‘next I will’, ‘Step N’, ask the user to run MCP queries, or paste tool JSON in chat prose—use the native tool channel only.")
				continue
			}

			// If the model skipped tools entirely for a workspace question,
			// push it toward evidence. Breadth nudges run only for broad asks.
			if allowHostNudges && !hasGroundingDepth(executedTools) && iterations < maxIter {
				groundingNudgeCount++
				log(fmt.Sprintf("[LLM] grounding nudge #%d (iter %d/%d, budget-based)", groundingNudgeCount, iterations, maxIter))
				if rawText != "" {
					pushAssistantText(choice.Text())
				}
				diskHint := " (Disk listing tools are unavailable—use **query** / **query** / **context** only.)"
				if opts.Tools.WorkspaceToolsAvailable() {
					diskHint = " Use **list_workspace_directory** then **read_workspace_file** on `cmd/*`, core packages (`internal/*`), and extension code—not README alone."
				}
				pushUser("[Orchestrator — continue investigating]\nThis question needs evidence from the index or disk, not registry metadata alone. Call **query** with the user's topic and **query** with concrete strings (README, main, internal/, symbols)." +
					diskHint +
					" If tools genuinely return empty or errors after tries, say so explicitly—you may then finalize.")
				continue
			}

			wsAvail := opts.Tools.WorkspaceToolsAvailable()
			if allowHostNudges && isProbablyBroadExplorationAsk(qCore) && hasGroundingDepth(executedTools) &&
				!overviewBreadthMet(toolCallCounts, wsAvail, implementationReadCount, breadthNudgeCount) &&
				iterations < maxIter {
				breadthNudgeCount++
				countsJSON, _ := json.Marshal(toolCallCounts)
				log(fmt.Sprintf("[LLM] breadth nudge #%d (iter %d/%d; workspace=%v; counts=%s)", breadthNudgeCount, iterations, maxIter, wsAvail, string(countsJSON)))
				if rawText != "" {
					pushAssistantText(choice.Text())
				}
				breadthBody := "[Orchestrator — broaden coverage (index-only)]\n" +
					"Workspace file tools are **not available** on your MCP server binary—do **not** keep calling **list_workspace_directory** / **read_workspace_file**. Instead deepen coverage with:\n" +
					"- Run several **query** calls yourself with distinct angles (for example CLI, MCP, indexer, vscode-extension).\n" +
					"- Run **query** with distinct queries (use **limit** 24+) and **context** on key symbols returned by query.\n" +
					"- Do not ask the user to execute queries or provide tool output; you own tool execution in this chat.\n" +
					"Then write the full project overview directly in Markdown from the gathered tool evidence."
				if wsAvail {
					breadthBody = "[Orchestrator — deepen evidence before answering]\n" +
						"The user’s question sounds **broad** (whole repo / architecture). Do **not** repeat **list_workspace_directory** on the **same** path—once root (`.`) is listed, **read files** next.\n" +
						"Deliver one **coherent** answer grounded in this repo—not checklist prose or ‘we will continue…’.\n" +
						"- **read_workspace_file** on ≥2 distinct **implementation** files (.go/.ts under cmd/, internal/, vscode-extension/src/…).\n" +
						"- **query** for subsystem terms; **query** again with a different query if needed.\n" +
						"If tools were exhausted or duplicate-skipped, **finalize now** from JSON already in this thread."
				}
				pushUser(breadthBody)
				continue
			}

			if looksLikeBadUserFacingAnswer(displayText) {
				if rawText != "" {
					pushAssistantText(choice.Text())
				}
				if iterations < maxIter {
					log("[LLM] no-tool draft blocked as non-user-facing; requesting direct answer")
					pushUser("[Orchestrator — user-safe final answer]\nYour previous draft was not user-facing (internal/tool commentary, deferred steps, or schema-like text).\n" +
						"Write only the final Markdown answer for the user now. Do not mention duplicate tool calls, orchestrator notes, internal_control, or ask the user to run tools.")
					continue
				}
				log("[LLM] no-tool draft blocked at round budget; moving to finalize pass")
				break
			}

			noToolFinalStreamed := false
			if useStream && hooks.OnAssistantToken != nil && displayText != "" {
				hooks.OnAssistantToken(displayText)
				noToolFinalStreamed = true
			}
			if displayText == "" {
				displayText = "(empty response)"
			}
			return makeResult(displayText, noToolFinalStreamed), nil
		}

		log(fmt.Sprintf("[LLM] assistant requested %d tool call(s)", len(toolCalls)))

		if hooks.OnAssistantPlanning != nil {
			names := make([]string, 0, len(toolCalls))
			for _, tc := range toolCalls {
				names = append(names, tc.Function.Name)
			}
			planningText := ""
			if assistantHistoryContent != nil {
				planningText = *assistantHistoryContent
			}
			hooks.OnAssistantPlanning(planningText, names)
		}

		messages = append(messages, llm.Message{
			Role:      "assistant",
			Content:   assistantHistoryContent,
			ToolCalls: toolCalls,
		})

		for _, tc := range toolCalls {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			rawName := tc.Function.Name
			args := map[string]any{}
			if strings.TrimSpace(tc.Function.Arguments) != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					args = map[string]any{}
				}
			}

			// Resolve hallucinated / prefixed names to canonical ones.
			name := rawName
			if !allowedTools[rawName] {
				aliased := aliasToolName(rawName, allowedTools)
				if aliased == "" {
					consecutiveUnknownToolCalls++
					errPayload := unknownToolErrorPayload(rawName, allowedTools)
					if hooks.OnToolStart != nil {
						hooks.OnToolStart(rawName, args)
					}
					if hooks.OnToolComplete != nil {
						hooks.OnToolComplete(rawName, args, errPayload)
					}
					pushToolResult(tc.ID, errPayload)
					if consecutiveUnknownToolCalls >= maxConsecutiveUnknownTools {
						// One more chance to compose a real answer instead of
						// guessing tool names.
						log(fmt.Sprintf("[LLM] %d consecutive unknown tool calls — forcing finalize without tools.", consecutiveUnknownToolCalls))
						pushUser(fmt.Sprintf("[Orchestrator — finalize] You called tools that do not exist on this server %d times in a row. **Stop calling tools.** "+
							"Reply directly to the user's last message using only the conversation so far. "+
							"Do not list, describe, or invent tools. Keep the answer short and on-topic.", consecutiveUnknownToolCalls))
						break
					}
					continue
				}
				log(fmt.Sprintf("[MCP] aliased tool name %q → %q", rawName, aliased))
				name = aliased
				consecutiveUnknownToolCalls = 0
			} else {
				consecutiveUnknownToolCalls = 0
			}
			if coerced := coerceLegacyPackTool(rawName, args); coerced != name {
				log(fmt.Sprintf("[MCP] coerced legacy tool %q → %q", rawName, coerced))
				name = coerced
			}

			normalizedForCall := normalizeToolArguments(name, args)
			argPreviewBytes, _ := json.Marshal(normalizedForCall)
			argPreview := string(argPreviewBytes)
			if len(argPreview) > toolArgLogPreviewLimit {
				argPreview = argPreview[:toolArgLogPreviewLimit] + "…"
			}
			log(fmt.Sprintf("[MCP] tools/call %s %s", name, argPreview))
			if hooks.OnToolStart != nil {
				hooks.OnToolStart(name, args)
			}

			if writeToolNames[name] && !allowWrites {
				msg := fmt.Sprintf("%s is disabled — switch to Agent mode with an approved plan.", name)
				if writeBlockReason != "" {
					msg = writeBlockReason
				}
				out := fmt.Sprintf(`{"error": %q}`, msg)
				if hooks.OnToolComplete != nil {
					hooks.OnToolComplete(name, args, out)
				}
				pushToolResult(tc.ID, out)
				continue
			}

			if name == "query" {
				rk := retrievalDedupeKey(name, normalizedForCall)
				if rk != "" && successfulRetrievalKeys[rk] {
					dupBytes, _ := json.MarshalIndent(map[string]any{
						"internal_control": true,
						"code":             "duplicate_retrieval_call",
						"message":          fmt.Sprintf("This exact %s call already succeeded in this session. Use earlier evidence or choose a different search string.", name),
						"user_visible":     false,
						"next_action":      "synthesize_from_prior_results_or_search_distinct_terms",
					}, "", "  ")
					dupMsg := string(dupBytes)
					log(fmt.Sprintf("[MCP] %s skipped duplicate retrieval key=%s", name, rk))
					if hooks.OnToolComplete != nil {
						hooks.OnToolComplete(name, args, dupMsg)
					}
					pushToolResult(tc.ID, dupMsg)
					continue
				}
			}
			if name == "list_workspace_directory" {
				dk := listWorkspaceDedupeKey(normalizedForCall)
				if listDirHits[dk] >= 2 {
					dupBytes, _ := json.MarshalIndent(map[string]any{
						"internal_control": true,
						"code":             "duplicate_directory_listing",
						"message":          "This directory was already listed successfully twice in this session.",
						"user_visible":     false,
						"next_action":      "read_concrete_files_from_prior_listing_or_synthesize_answer",
					}, "", "  ")
					dupMsg := string(dupBytes)
					log(fmt.Sprintf("[MCP] %s skipped duplicate path=%s", name, dk))
					if hooks.OnToolComplete != nil {
						hooks.OnToolComplete(name, args, dupMsg)
					}
					pushToolResult(tc.ID, dupMsg)
					continue
				}
			}

			out, callErr := opts.Tools.Call(ctx, name, normalizedForCall)
			if callErr != nil {
				msg := callErr.Error()
				log(fmt.Sprintf("[MCP] %s error: %s", name, msg))
				errBytes, _ := json.Marshal(map[string]any{"error": msg})
				errPayload := string(errBytes)
				if hooks.OnToolComplete != nil {
					hooks.OnToolComplete(name, args, errPayload)
				}
				pushToolResult(tc.ID, errPayload)
				continue
			}
			mcpOk := mcpToolOutputSucceeded(out)
			if mcpOk {
				executedTools[name] = true
				toolCallCounts[name]++
				if name == "query" {
					if rk := retrievalDedupeKey(name, normalizedForCall); rk != "" {
						successfulRetrievalKeys[rk] = true
					}
				}
				if name == "list_workspace_directory" {
					listDirHits[listWorkspaceDedupeKey(normalizedForCall)]++
				}
				if name == "read_workspace_file" && countsAsImplementationRead(peekWorkspaceReadPath(normalizedForCall)) {
					implementationReadCount++
				}
				if (name == "write_workspace_file" || name == "apply_patch_workspace_file") && mode == ModeAgent {
					rel := peekWorkspaceReadPath(args)
					if rel != "" && !containsString(writtenRelativePaths, rel) {
						writtenRelativePaths = append(writtenRelativePaths, rel)
					}
					if edit := parseWorkspaceEditResult(name, out); edit != nil && hooks.OnWorkspaceEdit != nil {
						hooks.OnWorkspaceEdit(*edit)
					}
				}
			}
			status := "failed"
			if mcpOk {
				status = "ok"
			}
			log(fmt.Sprintf("[MCP] %s %s (%d chars)", name, status, len(out)))
			if hooks.OnToolComplete != nil {
				hooks.OnToolComplete(name, args, out)
			}
			pushToolResult(tc.ID, out)
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	log(fmt.Sprintf("[LLM] finalization pass (no tools) after %d tool rounds — synthesizing answer from thread", maxIter))
	pushUser("[Orchestrator — finalize]\nThe **LLM round budget** for this request is exhausted. Reply with one **complete** answer for the user using **only** evidence already in this conversation (tool JSON + prior assistant text). If you only skimmed README, say so—but still synthesize whatever **queries, packs, symbol context, or code files** you did retrieve.\nPrefer **subsystem breakdown + cited paths/code roles** over repeating marketing bullets alone. Ignore tool payloads marked `internal_control` / `user_visible:false`; they are host steering, not user-facing evidence. Be explicit about what failed or was not retrieved. **Do not** output tool-call JSON; **do not** say you will run more tools.")

	finalReq := llm.ChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: temperature,
	}

	finalStreamed := false
	var finalChoice llm.Message
	var err error
	if useStream && hooks.OnAssistantToken != nil {
		cr, cerr := client.CompleteStreaming(ctx, finalReq, nil)
		err = cerr
		if err == nil {
			finalChoice = cr.Message
			totalUsage.Add(cr.Usage)
			emitUsage(finalReq.Messages, iterations+1)
		} else {
			log(fmt.Sprintf("[LLM] finalization stream failed (%v), retry non-stream", err))
			cr, err = client.Complete(ctx, finalReq)
			if err == nil {
				finalChoice = cr.Message
				totalUsage.Add(cr.Usage)
				emitUsage(finalReq.Messages, iterations+1)
			}
		}
	} else {
		cr, cerr := client.Complete(ctx, finalReq)
		err = cerr
		if err == nil {
			finalChoice = cr.Message
			totalUsage.Add(cr.Usage)
			emitUsage(finalReq.Messages, iterations+1)
		}
	}
	if err != nil {
		return nil, err
	}

	finalText := formatAssistantReplyForUser(finalChoice.Text(), allowedTools)
	if looksLikeInternalControlOnly(finalText) {
		log("[LLM] finalization repeated internal control message; retrying once without tools")
		pushUser("[Orchestrator — retry final answer]\nYour previous draft repeated an internal host control diagnostic. Do not mention duplicate listings, prior tool JSON, or tool-loop instructions. Write the best grounded Markdown answer for the user's request from the evidence already gathered. If evidence is insufficient, say exactly what is missing in one sentence.")
		finalReq.Messages = messages
		cr, err := client.Complete(ctx, finalReq)
		if err != nil {
			return nil, err
		}
		totalUsage.Add(cr.Usage)
		emitUsage(finalReq.Messages, iterations+1)
		finalChoice = cr.Message
		finalText = formatAssistantReplyForUser(finalChoice.Text(), allowedTools)
		if looksLikeInternalControlOnly(finalText) {
			finalText = ""
		}
	}

	finalLooksBad := looksLikeBadUserFacingAnswer(finalText) ||
		(!isPlainSocialOrOffTopic(qCore) &&
			(isProbablyBroadExplorationAsk(qCore) || mentionsCodeOrWorkspaceIntent(qCore)) &&
			hasGroundingDepth(executedTools) &&
			!hasPathCitation(finalText))

	if !finalLooksBad {
		if useStream && hooks.OnAssistantToken != nil && finalText != "" {
			hooks.OnAssistantToken(finalText)
			finalStreamed = true
		}
		return makeResult(finalText, finalStreamed), nil
	}

	if !isPlainSocialOrOffTopic(qCore) && (isProbablyBroadExplorationAsk(qCore) || mentionsCodeOrWorkspaceIntent(qCore)) {
		if recovered := buildDeterministicOverviewFallback(ctx, opts.Tools, qCore, model, log); strings.TrimSpace(recovered) != "" {
			return makeResult(recovered, false), nil
		}
	}

	return makeResult(
		fmt.Sprintf("Ran %d agent rounds plus a finalize step but the model returned an empty reply. Increase the agent max tool rounds setting for more exploration.", maxIter),
		finalStreamed,
	), nil
}

func containsString(arr []string, s string) bool {
	for _, v := range arr {
		if v == s {
			return true
		}
	}
	return false
}

func estimateMessagesTokens(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += retrieval.RoughTokens(m.Text())
		for _, tc := range m.ToolCalls {
			total += retrieval.RoughTokens(tc.Function.Name + tc.Function.Arguments)
		}
	}
	return total
}
