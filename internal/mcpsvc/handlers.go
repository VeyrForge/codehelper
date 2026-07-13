package mcpsvc

import (
	"github.com/VeyrForge/codehelper/internal/registry"
	"github.com/mark3labs/mcp-go/server"
)

// AllToolHandlers wires every MCP tool handler (mirrors RegisterAll). Used by
// smoke tests, orchestration eval, and benchmarks.
func AllToolHandlers(reg *registry.Registry) map[string]server.ToolHandlerFunc {
	h := coreToolHandlers(reg)
	h["investigate"] = investigateHandler(reg, h)
	h["edit_cycle"] = editCycleHandler(reg, h)
	h["preflight"] = preflightHandler(reg, h)
	return h
}

// coreToolHandlers is the base handler map without composite fused tools (avoids
// recursion when composite handlers delegate to the core set).
func coreToolHandlers(reg *registry.Registry) map[string]server.ToolHandlerFunc {
	return map[string]server.ToolHandlerFunc{
		"project_context":            projectContextHandler(reg),
		"query":                      queryHandler(reg),
		"context":                    contextHandler(reg),
		"impact":                     impactHandler(reg),
		"detect_changes":             detectChangesHandler(reg),
		"scout":                      scoutHandler(reg),
		"test_impact":                testImpactHandler(reg),
		"since":                      sinceHandler(reg),
		"dead_code":                  deadCodeHandler(reg),
		"hotspots":                   hotspotsHandler(reg),
		"ast_query":                  astQueryHandler(reg),
		"api_surface":                apiSurfaceHandler(reg),
		"change_kit":                 changeKitHandler(reg),
		"find_implementations":       findImplementationsHandler(reg),
		"similar":                    similarHandler(reg),
		"trace":                      traceHandler(reg),
		"diagnostics":                diagnosticsHandler(reg),
		"verify":                     verifyHandler(reg),
		"review_diff":                reviewDiffHandler(reg),
		"docs":                       docsHandler(reg),
		"docs_add":                   docsAddHandler(reg),
		"web":                        webHandler(),
		"browser":                    browserHandler(),
		"web_search":                 webSearchHandler(),
		"usage_report":               usageReportHandler(reg),
		"read_workspace_file":        readWorkspaceFileHandler(reg),
		"write_workspace_file":       writeWorkspaceFileHandler(reg),
		"apply_patch_workspace_file": applyPatchWorkspaceFileHandler(reg),
		"revert_workspace_edit":      revertWorkspaceEditHandler(reg),
		"list_workspace_directory":   listWorkspaceDirectoryHandler(reg),
		"rename_symbol":              renameSymbolHandler(reg),
		"insert_at_symbol":           insertAtSymbolHandler(reg),
		"scope":                      scopeHandler(reg),
		"plan":                       planHandler(reg),
		"kickoff":                    kickoffHandler(reg),
		"review":                     reviewHandler(reg),
		"finish_check":               finishCheckHandler(reg),
		"agent_memory":               agentMemoryHandler(reg),
		"glossary":                   glossaryHandler(reg),
		"hints":                      hintsHandler(reg),
		"agent_plan":                 agentPlanHandler(reg),
		"agent_execute_todo":         agentExecuteTodoHandler(reg),
		"orchestration":              orchestrationControlHandler(reg),
		"orchestrate":                orchestrateHandler(reg),
		"orchestration_rerun":        orchestrationRerunHandler(reg),
		"orchestration_feedback":     orchestrationFeedbackHandler(reg),
		"run_trace":                  runTraceHandler(reg),
		"explain_run":                explainRunHandler(reg),
		"orchestration_memory":       orchestrationMemoryHandler(reg),
		"remote_list":                remoteListHandler(reg),
		"remote_exec":                remoteExecHandler(reg),
		"log_read":                   logReadHandler(reg),
		"db_query":                   dbQueryHandler(reg),
		"db_schema":                  dbSchemaHandler(reg),
		"run_alias":                  runAliasHandler(reg),
		"env_context":                envContextHandler(reg),
		"ci_status":                  ciStatusHandler(reg),
	}
}
