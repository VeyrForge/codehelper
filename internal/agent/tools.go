// Package agent implements the IDE-agnostic LLM agent loop: OpenAI-style
// tool schemas over the Codehelper MCP tool surface, the multi-round
// orchestrator with grounding/breadth/meta nudges, and the post-run
// verification gate. Clients (VS Code, CLI, HTTP API) only render events.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Mode mirrors the panel task modes: Ask/Plan are read-only, Agent may write.
type Mode string

const (
	ModeAsk   Mode = "ask"
	ModePlan  Mode = "plan"
	ModeAgent Mode = "agent"
	ModeDebug Mode = "debug"
)

// NormalizeMode coerces arbitrary client input to a valid Mode.
func NormalizeMode(raw string) Mode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "plan":
		return ModePlan
	case "agent":
		return ModeAgent
	case "debug":
		return ModeDebug
	default:
		return ModeAsk
	}
}

// ToolCaller abstracts in-process MCP tool execution.
type ToolCaller interface {
	Call(ctx context.Context, name string, args map[string]any) (string, error)
	WorkspaceToolsAvailable() bool
}

var writeToolNames = map[string]bool{
	"write_workspace_file":       true,
	"apply_patch_workspace_file": true,
	"revert_workspace_edit":      true,
}

var allToolNames = []string{
	"project_context",
	"query",
	"context",
	"impact",
	"detect_changes",
	"read_workspace_file",
	"write_workspace_file",
	"apply_patch_workspace_file",
	"revert_workspace_edit",
	"list_workspace_directory",
}

// toolSchemasJSON is the OpenAI tools array advertised to the LLM,
// descriptions aligned with the Codehelper MCP server (ported verbatim from
// the original VS Code host).
const toolSchemasJSON = `[
  {
    "type": "function",
    "function": {
      "name": "project_context",
      "description": "Bootstrap for the **current workspace only**: resolved ` + "`repo`" + `, ` + "`repo_root`" + `, index freshness, ` + "`recommended_next_tools`" + `, optional ` + "`warnings`" + `, and key manifest paths. Call once per session; other tools default to this repo when ` + "`repo`" + ` is omitted. If ` + "`index_status`" + ` is missing, run ` + "`codehelper analyze`" + ` in the project root.",
      "parameters": {
        "type": "object",
        "properties": {
          "repo": { "type": "string", "description": "Optional registry repo name; omit to use the open workspace." },
          "verbosity": { "type": "string", "enum": ["short", "detailed"], "description": "short (default: essentials only) | detailed (full layout, deps, scripts, git, language stats)." }
        },
        "additionalProperties": false
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "query",
      "description": "BM25 + trigram lexical search over the **indexed symbol graph** (not the open web). Match depth to the task: **narrow** asks → one or two focused queries; **broad** / architecture → set ` + "`include_context_pack`" + `: true and ` + "`limit`" + `: 24–32, then ` + "`read_workspace_file`" + ` on files you cite. Weak on prose — use ` + "`read_workspace_file`" + ` for README/manifests.",
      "parameters": {
        "type": "object",
        "properties": {
          "query": { "type": "string", "description": "1–6 keywords. Prefer concrete subsystem names (e.g. ` + "`register`" + `, ` + "`indexer`" + `, ` + "`auth`" + `) over full English sentences." },
          "q": { "type": "string", "description": "Deprecated alias for ` + "`query`" + `; pass ` + "`query`" + ` instead." },
          "repo": { "type": "string", "description": "Registry repo ` + "`name`" + ` from ` + "`project_context`" + ` (or omit). Omit when only one repo applies. Never put a git ref or path here." },
          "intent": { "type": "string", "enum": ["explore", "debug", "test", "refactor"], "description": "Optional intent tag — biases ranking and downstream review tools." },
          "include_context_pack": { "type": "boolean", "description": "If true, also returns the ranked context_pack in the same response." },
          "limit": { "type": "number", "description": "Max items in context_pack when include_context_pack is true (default 24; use 24–32 for broad overviews)." },
          "budget_tokens": { "type": "number", "description": "When set (>0), also return token-budgeted buckets." },
          "base_ref": { "type": "string", "description": "Git ref for change boosting (e.g. ` + "`HEAD~1`" + `). Optional." }
        },
        "additionalProperties": false
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "context",
      "description": "Fetch the 360° view of one symbol: callers, callees, imports, and surrounding context. **Use after** ` + "`query`" + ` has surfaced a symbol name you want to inspect. **Returns** ` + "`{symbol, callers:[…], callees:[…], imports:[…], bundle}`" + `. Example: ` + "`{\\\"name\\\":\\\"RegisterAll\\\"}`" + `.",
      "parameters": {
        "type": "object",
        "properties": {
          "name": { "type": "string", "description": "Either a symbol name (e.g. ` + "`RegisterAll`" + `) or a fully-qualified ` + "`sym:`" + ` id from a previous tool result." },
          "repo": { "type": "string", "description": "Registry ` + "`name`" + ` from ` + "`project_context`" + ` (or omit). Never a git ref." }
        },
        "required": ["name"],
        "additionalProperties": false
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "impact",
      "description": "Compute the blast radius of a symbol over the call graph. **Use when** the user asks 'what breaks if I change X?' or before risky refactors. **Returns** ` + "`{target, affected_symbols, affected_files, risk_tier}`" + `. Example: ` + "`{\\\"target\\\":\\\"RegisterAll\\\",\\\"direction\\\":\\\"upstream\\\"}`" + `.",
      "parameters": {
        "type": "object",
        "properties": {
          "target": { "type": "string", "description": "Symbol name or ` + "`sym:`" + ` id whose blast radius you want." },
          "direction": { "type": "string", "enum": ["upstream", "downstream"], "description": "` + "`upstream`" + ` = who calls this (default for refactors). ` + "`downstream`" + ` = what this depends on." },
          "depth": { "type": "number", "description": "Graph depth (default 3)." },
          "include_tests": { "type": "boolean", "description": "Include test files in the radius." },
          "max_candidates": { "type": "number", "description": "Cap the result size (default 200)." },
          "repo": { "type": "string", "description": "Registry ` + "`name`" + ` from ` + "`project_context`" + ` (or omit)." }
        },
        "required": ["target"],
        "additionalProperties": false
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "detect_changes",
      "description": "Map current git diff against ` + "`base_ref`" + ` to affected symbols and files. **Use when** the user asks about *recent edits*, what's *changed*, or what's in the *current PR*. **Returns** ` + "`{changed_files:[…], changed_symbols:[…], base_ref}`" + `. Example: ` + "`{\\\"base_ref\\\":\\\"HEAD~1\\\"}`" + `.",
      "parameters": {
        "type": "object",
        "properties": {
          "base_ref": { "type": "string", "description": "Git ref (default ` + "`HEAD~1`" + `). Accepts branches, tags, SHAs." },
          "repo": { "type": "string", "description": "Registry ` + "`name`" + ` from ` + "`project_context`" + ` (or omit)." }
        },
        "additionalProperties": false
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "read_workspace_file",
      "description": "Read a text file from disk inside an indexed repo. **Use when** you know the path (from ` + "`query`" + `, ` + "`list_workspace_directory`" + `, or the user's @-mention) and need the actual content to quote or reason about. **Returns** ` + "`{path, repo_root, size_bytes, read_bytes, truncated, content}`" + `. Default cap ~512 KiB; raise via ` + "`max_bytes`" + ` (cap 4 MiB). Example: ` + "`{\\\"path\\\":\\\"internal/mcpsvc/register.go\\\"}`" + `.",
      "parameters": {
        "type": "object",
        "properties": {
          "path": { "type": "string", "description": "Repo-relative file path (e.g. ` + "`internal/foo/bar.go`" + `). Never absolute, never starts with ` + "`/`" + `." },
          "repo": { "type": "string", "description": "Registry ` + "`name`" + ` from ` + "`project_context`" + ` (or omit) (omit if only one repo applies)." },
          "max_bytes": { "type": "number", "description": "Read at most this many bytes (default ~512 KiB, cap ~4 MiB)." }
        },
        "required": ["path"],
        "additionalProperties": false
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "write_workspace_file",
      "description": "Replace a file's content wholesale, or create a new file. **Use only** when (a) the file does not yet exist, (b) the user explicitly asked for a wholesale rewrite, or (c) ` + "`apply_patch_workspace_file`" + ` failed twice. For every other edit, use ` + "`apply_patch_workspace_file`" + `. Blocked paths: ` + "`.git/**`" + `, ` + "`node_modules/**`" + `, real secret files (` + "`.env`" + `, ` + "`.env.local`" + `, …; ` + "`.env.example`" + ` is allowed). The server rejects content that looks truncated. **Returns** ` + "`{path, repo_root, bytes_before, bytes_written, created, no_op, diff, revert_token}`" + `.",
      "parameters": {
        "type": "object",
        "properties": {
          "path": { "type": "string", "description": "Repo-relative file path. Will be created if it does not exist." },
          "content": { "type": "string", "description": "Full UTF-8 file content — every byte that should end up on disk." },
          "repo": { "type": "string", "description": "Registry ` + "`name`" + ` from ` + "`project_context`" + ` (or omit)." },
          "create_directories": { "type": "boolean", "description": "Create parent directories if missing (default true)." }
        },
        "required": ["path", "content"],
        "additionalProperties": false
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "apply_patch_workspace_file",
      "description": "**Preferred edit tool.** Apply one or more surgical search/replace hunks to an existing file. Each hunk's ` + "`old_string`" + ` must appear **exactly once** in the current file (copy it verbatim from a fresh ` + "`read_workspace_file`" + ` result, including indentation and newlines), or pass ` + "`replace_all`" + ` if you really mean every match. Preserves untouched content byte-for-byte. **Returns** ` + "`{path, repo_root, hunks_applied, diff, revert_token}`" + `.",
      "parameters": {
        "type": "object",
        "properties": {
          "path": { "type": "string", "description": "Path relative to repo root" },
          "repo": { "type": "string", "description": "Registry repo name from project_context" },
          "hunks": {
            "type": "array",
            "description": "Array of { old_string, new_string, replace_all? } applied in order. old_string must match exactly (including whitespace).",
            "items": {
              "type": "object",
              "properties": {
                "old_string": { "type": "string" },
                "new_string": { "type": "string" },
                "replace_all": { "type": "boolean" }
              },
              "required": ["old_string", "new_string"],
              "additionalProperties": false
            }
          },
          "dry_run": { "type": "boolean", "description": "Return the diff without writing" }
        },
        "required": ["path", "hunks"],
        "additionalProperties": false
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "revert_workspace_edit",
      "description": "Revert a prior edit using the ` + "`revert_token`" + ` returned by ` + "`write_workspace_file`" + ` or ` + "`apply_patch_workspace_file`" + `. **Use when** the user clicks Undo or says 'revert / roll that back / undo the last change'. **Returns** ` + "`{path, repo_root, restored}`" + `.",
      "parameters": {
        "type": "object",
        "properties": {
          "revert_token": { "type": "string", "description": "Token from the previous write/patch tool response." }
        },
        "required": ["revert_token"],
        "additionalProperties": false
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "list_workspace_directory",
      "description": "List files and folders in a directory under an indexed repo (**non-recursive**). **Use when** you need to discover layout on disk — call it once on ` + "`.`" + ` for top-level shape, then on specific subfolders. **Returns** ` + "`{path, repo_root, entries:[{name,is_dir,size_bytes}], truncated}`" + `. Default cap 200 entries; raise via ` + "`max_entries`" + `. Example: ` + "`{\\\"path\\\":\\\".\\\"}`" + `.",
      "parameters": {
        "type": "object",
        "properties": {
          "path": { "type": "string", "description": "Repo-relative directory path. Defaults to ` + "`.`" + ` (repo root)." },
          "repo": { "type": "string", "description": "Registry ` + "`name`" + ` from ` + "`project_context`" + ` (or omit)." },
          "max_entries": { "type": "number", "description": "Cap the entry count (default 200)." }
        },
        "additionalProperties": false
      }
    }
  }
]`

var allToolSchemas = mustParseToolSchemas()

func mustParseToolSchemas() []map[string]any {
	var out []map[string]any
	if err := json.Unmarshal([]byte(toolSchemasJSON), &out); err != nil {
		panic(fmt.Sprintf("agent: invalid embedded tool schemas: %v", err))
	}
	return out
}

func toolSchemaName(tool map[string]any) string {
	fn, _ := tool["function"].(map[string]any)
	if fn == nil {
		return ""
	}
	name, _ := fn["name"].(string)
	return name
}

// toolsForMode returns the tools advertised to the LLM (Ask/Plan omit writes).
func toolsForMode(mode Mode) []any {
	out := make([]any, 0, len(allToolSchemas))
	for _, t := range allToolSchemas {
		name := toolSchemaName(t)
		if mode != ModeAgent && mode != ModeDebug && writeToolNames[name] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// allowedToolsForMode returns the tool names permitted for this mode.
func allowedToolsForMode(mode Mode) map[string]bool {
	out := map[string]bool{}
	for _, n := range allToolNames {
		if mode != ModeAgent && mode != ModeDebug && writeToolNames[n] {
			continue
		}
		out[n] = true
	}
	return out
}

// toolNameAliases maps common hallucinated tool names to canonical ones.
// Keys are normalized (lowercased, prefixes stripped, `_` ≡ `-`).
var toolNameAliases = map[string]string{
	// MCP roots/schema leakage from some local models
	"listroots":      "project_context",
	"list_roots":     "project_context",
	"mcp_list_roots": "project_context",
	"get_roots":      "project_context",

	// Directory listing
	"list_files":         "list_workspace_directory",
	"list_dir":           "list_workspace_directory",
	"list_dirs":          "list_workspace_directory",
	"list_directory":     "list_workspace_directory",
	"list_directories":   "list_workspace_directory",
	"list_workspace_dir": "list_workspace_directory",
	"ls":                 "list_workspace_directory",
	"ls_workspace":       "list_workspace_directory",
	"readdir":            "list_workspace_directory",
	"read_dir":           "list_workspace_directory",
	"read_directory":     "list_workspace_directory",

	// File reading
	"read_file":          "read_workspace_file",
	"read_file_content":  "read_workspace_file",
	"read_file_contents": "read_workspace_file",
	"cat":                "read_workspace_file",
	"cat_file":           "read_workspace_file",
	"open_file":          "read_workspace_file",
	"get_file":           "read_workspace_file",
	"get_file_content":   "read_workspace_file",
	"show_file":          "read_workspace_file",
	"view_file":          "read_workspace_file",

	// File writing (full content)
	"write_file":         "write_workspace_file",
	"write_file_content": "write_workspace_file",
	"create_file":        "write_workspace_file",
	"save_file":          "write_workspace_file",
	"put_file":           "write_workspace_file",

	// Patch / edit
	"patch_file":           "apply_patch_workspace_file",
	"patch_workspace_file": "apply_patch_workspace_file",
	"apply_patch":          "apply_patch_workspace_file",
	"edit_file":            "apply_patch_workspace_file",
	"str_replace":          "apply_patch_workspace_file",
	"search_replace":       "apply_patch_workspace_file",
	"replace_in_file":      "apply_patch_workspace_file",

	// Revert
	"undo":     "revert_workspace_edit",
	"revert":   "revert_workspace_edit",
	"rollback": "revert_workspace_edit",

	// Search
	"search":      "query",
	"search_code": "query",
	"find":        "query",
	"find_symbol": "query",
	"lookup":      "query",

	// Context / impact / packs (legacy names map to query with pack)
	"context_pack":    "query",
	"context_package": "query",
	"context_list":    "query",
	"context_read":    "query",
	"context_tool":    "query",
	"pack_context":    "query",
	"get_context":     "query",
	"callers":         "context",
	"callees":         "context",
	"impact_analysis": "impact",
	"blast_radius":    "impact",

	// Change detection
	"git_changes":   "detect_changes",
	"detect_change": "detect_changes",
	"changed_files": "detect_changes",
}

var toolNamePrefixRe = regexp.MustCompile(`(?i)^(call|tool|mcp|function|fn):\s*`)
var toolNameSepRe = regexp.MustCompile(`[-\s]+`)

func normalizeToolName(raw string) string {
	s := strings.TrimSpace(raw)
	s = toolNamePrefixRe.ReplaceAllString(s, "")
	s = strings.ToLower(s)
	s = toolNameSepRe.ReplaceAllString(s, "_")
	return s
}

// aliasToolName resolves hallucinated/prefixed names to canonical ones.
func aliasToolName(raw string, allowed map[string]bool) string {
	norm := normalizeToolName(raw)
	if allowed[norm] {
		return norm
	}
	if aliased, ok := toolNameAliases[norm]; ok && allowed[aliased] {
		return aliased
	}
	// Suffix-tolerant match: `list_workspace_dir` vs `list_workspace_directory`.
	for real := range allowed {
		if strings.HasPrefix(real, norm) || strings.HasPrefix(norm, real) {
			if len(norm) >= 4 && len(real) >= 4 {
				return real
			}
		}
	}
	return ""
}

// coerceLegacyPackTool maps removed context_pack calls onto query with pack args.
func coerceLegacyPackTool(rawName string, args map[string]any) string {
	if normalizeToolName(rawName) != "context_pack" {
		return rawName
	}
	if args == nil {
		args = map[string]any{}
	}
	if _, ok := args["include_context_pack"]; !ok {
		args["include_context_pack"] = true
	}
	if _, ok := args["limit"]; !ok {
		args["limit"] = 24
	}
	return "query"
}

// toolUsageExamples gives one-line usage examples per tool for error payloads.
var toolUsageExamples = map[string]string{
	"project_context":            `{"name":"project_context","arguments":{}}`,
	"query":                      `{"name":"query","arguments":{"query":"register handler","include_context_pack":true,"limit":24}}`,
	"context":                    `{"name":"context","arguments":{"name":"RegisterAll"}}`,
	"impact":                     `{"name":"impact","arguments":{"target":"RegisterAll","direction":"upstream"}}`,
	"detect_changes":             `{"name":"detect_changes","arguments":{"base_ref":"HEAD~1"}}`,
	"read_workspace_file":        `{"name":"read_workspace_file","arguments":{"path":"internal/mcpsvc/register.go"}}`,
	"list_workspace_directory":   `{"name":"list_workspace_directory","arguments":{"path":"."}}`,
	"apply_patch_workspace_file": `{"name":"apply_patch_workspace_file","arguments":{"path":"path/to/file.go","hunks":[{"old_string":"<exact existing text>","new_string":"<replacement>"}]}}`,
	"write_workspace_file":       `{"name":"write_workspace_file","arguments":{"path":"path/to/new_file.ts","content":"// full file content here\n"}}`,
	"revert_workspace_edit":      `{"name":"revert_workspace_edit","arguments":{"revert_token":"<token returned by previous edit>"}}`,
}

// closestLegalTool picks the legal tool sharing the most characters with the
// rejected name.
func closestLegalTool(rawName string, allowed map[string]bool) string {
	norm := normalizeToolName(rawName)
	if norm == "" {
		return ""
	}
	best := ""
	bestScore := 0
	names := make([]string, 0, len(allowed))
	for n := range allowed {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, cand := range names {
		score := 0
		if strings.Contains(cand, norm) || strings.Contains(norm, cand) {
			score = min(len(cand), len(norm))
		} else {
			i := 0
			lim := min(len(cand), len(norm))
			for i < lim && cand[i] == norm[i] {
				i++
			}
			score = i
		}
		if score > bestScore {
			bestScore = score
			best = cand
		}
	}
	if bestScore >= 3 {
		return best
	}
	return ""
}

func unknownToolErrorPayload(rawName string, allowed map[string]bool) string {
	names := make([]string, 0, len(allowed))
	for n := range allowed {
		names = append(names, n)
	}
	sort.Strings(names)
	closest := closestLegalTool(rawName, allowed)
	payload := map[string]any{
		"error":           fmt.Sprintf("Tool %q does not exist on this server.", rawName),
		"available_tools": names,
		"hint": "Use one of the names in `available_tools` verbatim — no `call:` / `tool:` / `mcp:` prefix, no angle-bracket placeholders. " +
			"If you need a capability that is not in the list, stop calling tools and answer the user from what you already have.",
	}
	if closest != "" {
		payload["closest_match"] = closest
		if ex, ok := toolUsageExamples[closest]; ok {
			payload["example_call"] = ex
		}
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error":"Tool %q does not exist on this server."}`, rawName)
	}
	return string(b)
}
