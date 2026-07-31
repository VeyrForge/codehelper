package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/VeyrForge/codehelper/internal/paths"
	"github.com/VeyrForge/codehelper/internal/prompts"
)

const header = `# Codehelper agent rules

Use the codehelper MCP tools FIRST for reading, searching, and reasoning about this
codebase — every agent and subagent (pass these rules when you spawn one). One call
answers in ~300-1000 tokens what raw Read/Grep/Glob needs 6-7 calls and 10k-110k tokens
for, and more accurately (resolved call graph, not grep). Built-ins first = more tokens,
worse answers.

Fall back to Read/Grep/Glob/Bash ONLY after a codehelper tool was tried for the task and
errored, came back empty/too vague, or the task is out of scope (non-indexed files, raw
git, running the app). Say so in one line when you do.

**Advertise profile (default focused):** tools/list shows ~12 entry points. Hidden
specialists stay callable by name; project_context still returns the full catalog.
Full advertise: CODEHELPER_TOOL_PROFILE=full.

## Conceptual slots — pick ONE starting tool

| slot | when you are… | call (prefer composites) |
|---|---|---|
| **project** | orienting in a repo | project_context verbosity=short (BOOTSTRAP once — never stop here) |
| **workflow** | starting a feature/fix / "how does X work" | **kickoff** (preferred); if orchestration enabled → **orchestrate**. Cheaper: sections=orient,reuse |
| **search** | finding code by name/concept | query (scout by name for reuse-first) |
| **understand** | one symbol, or locate+deep-dive | **investigate** (preferred) or context name=… |
| **impact** | gauging blast radius before an edit | impact target=… (skip if context/investigate already returned blast_radius) |
| **change** | about to edit a symbol | change_kit → apply_patch_workspace_file |
| **check** | just edited / claiming done | verify → finish_check |
| **browser** | verifying a real page after UI change | browser url=… |

Also: scope for vague ideas; plan role=architect|… by name when weighing design.

Query came back thin? Rephrase using concrete identifiers from the codebase and
re-query before grepping ("how does login work" → "auth session token middleware").

## Anti-patterns — wasted tokens and failed calls

| mistake | do instead |
|---|---|
| Skipping MCP; reaching for Read/Grep first | codehelper tools first; built-ins only after error/empty/out-of-scope (say so in one line) |
| Chaining query→context→impact | investigate (or kickoff/orchestrate for open-ended starts) |
| Full project_context when you only need a symbol/file | kickoff or query (bootstrap once with verbosity=short) |
| context / impact with symbol | pass name (or a sym: id from query) |
| change_kit without target | query first, then change_kit with target=<symbol> |
| Retrying the same wrong MCP arg | read the tool schema once, fix the param, call once |
| Glob with **/* | narrow glob or Grep with a path |
| context then separate impact on the same symbol | context already includes blast_radius — skip redundant impact |
| Multiple context calls before reading source | one context, or read_workspace_file when query already gave the path |

**Key parameters:** context → name · impact/change_kit → target · trace → from + to · query → query (required) · kickoff/orchestrate → task · project_context → verbosity=short (default) or detailed

## Web & browser — verify the real page, not just the code

Two local, fast tools for CHECKING a running site after a change (not for crawling):

| need | call |
|---|---|
| API / SSR / health check, JSON assert — HTTP only, ms-fast | web (no browser, no JS) — by name / full profile |
| SEE a page / client-side JS result — a screenshot the model can view | browser url=… |
| a long page in full, readable (not downscaled) | browser url=… split=true (pieces top→bottom) |
| just one region / specific height | browser url=… clip_y=… clip_height=… |
| responsive check across mobile + tablet + desktop in one call | browser url=… devices=["all"] |
| is it slow? FCP, load, request count, page weight | browser url=… metrics=true |
| accessibility + Core Web Vitals (LCP/CLS) audit | browser url=… audit=lite (or audit=full for axe-core) |
| drive a flow then capture (click/type/fill/scroll) | browser url=… actions=[…] |
| real e2e test — drive a flow and assert the result | browser url=… actions=[…,{"action":"assert","selector":…,"text":…}] |
| visual regression — did the UI change? | browser url=… baseline="name" (saves, then diffs) |
| console errors / uncaught JS / failed requests after a change | browser url=… (always reported) |
| find current info / docs / error messages on the web | web_search query=… (then web/browser the URLs) |

browser returns a WebP screenshot (small) + a BOUNDED text report (console, JS
errors, failed requests, optional perf). It deliberately does NOT dump the DOM or
accessibility tree — that is the difference from heavier browser MCPs that burn
100K+ tokens per call. device=mobile|tablet|desktop sets the correct size, pixel
ratio, and UA. Loopback is always allowed; LAN needs allow_private. First use
needs a one-time "ch browser install" (an isolated managed Chromium that never
touches the browsers you already have).

## Specialized — when the table doesn't fit (callable by name)

test_impact · since · find_implementations · ast_query · dead_code · api_surface ·
detect_changes · docs / web · read_workspace_file / list_workspace_directory ·
diagnostics · review_diff · scout · search_hybrid · context_bundle · trace · plan ·
usage_report · glossary · hints.

Prefer a tool over a hand-rolled grep/awk/compile script — they're deterministic, local,
and already return what you'd script for: risks (plan role=security/performance),
edit blast-radius (impact.risk_tier), tests (test_impact), build status (diagnostics).
On dynamic stacks (PHP/Ruby/C/C++) the call graph is sparse — don't trust a "0 tests /
low risk" as ground truth (check impact confidence).

## Defaults

- Reuse before adding; never create _v2/_new/copy duplicates when you can extend.
- Security/perf-first: validate inputs, no injection primitives, no N+1 or O(n²) on big sets.
- Ambiguous request, or adjacent work needed (tests/docs/migration/flag/compat)? ASK,
  offering the concrete options you can see — don't silently assume.
- Don't claim done until: index fresh · diagnostics/verify pass · no blocking review
  findings · changed contracts preserved or documented.

## Index freshness

Kept fresh by the watch daemon (codehelper watch --daemon; --status / --stop). Freshness
is git-COMMIT gated: after editing WITHOUT committing, run "codehelper analyze --force"
(or rely on the daemon) so query/context/impact reflect your working tree, not just HEAD.
`

type learningConfig struct {
	Enabled           bool   `json:"enabled"`
	Mode              string `json:"mode"`
	ProjectScopedOnly bool   `json:"project_scoped_only"`
	MemoryDir         string `json:"memory_dir"`
	SkillsDir         string `json:"skills_dir"`
	TranscriptsDir    string `json:"transcripts_dir"`
}

func defaultLearningConfig() learningConfig {
	return learningConfig{
		Enabled:           true,
		Mode:              "approval",
		ProjectScopedOnly: true,
		MemoryDir:         ".codehelper/memory",
		SkillsDir:         ".codehelper/learned-skills",
		TranscriptsDir:    ".codehelper/memory/transcripts",
	}
}

func (c learningConfig) normalized() learningConfig {
	out := c
	if out.Mode != "auto" {
		out.Mode = "approval"
	}
	out.ProjectScopedOnly = true
	if strings.TrimSpace(out.MemoryDir) == "" {
		out.MemoryDir = ".codehelper/memory"
	}
	if strings.TrimSpace(out.SkillsDir) == "" {
		out.SkillsDir = ".codehelper/learned-skills"
	}
	if strings.TrimSpace(out.TranscriptsDir) == "" {
		out.TranscriptsDir = ".codehelper/memory/transcripts"
	}
	return out
}

func ensureLearningConfig(repoRoot string) (learningConfig, error) {
	cfgPath := filepath.Join(paths.RepoIndexDir(repoRoot), "learning.json")
	cfg := defaultLearningConfig()
	b, err := os.ReadFile(cfgPath)
	if err == nil {
		if jerr := json.Unmarshal(b, &cfg); jerr != nil {
			return learningConfig{}, jerr
		}
		cfg = cfg.normalized()
		out, merr := json.MarshalIndent(cfg, "", "  ")
		if merr != nil {
			return learningConfig{}, merr
		}
		return cfg, os.WriteFile(cfgPath, append(out, '\n'), 0o644)
	}
	if !os.IsNotExist(err) {
		return learningConfig{}, err
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return learningConfig{}, err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return learningConfig{}, err
	}
	if err := os.WriteFile(cfgPath, append(out, '\n'), 0o644); err != nil {
		return learningConfig{}, err
	}
	return cfg, nil
}

func learningPolicyBlock(cfg learningConfig) string {
	state := "disabled"
	if cfg.Enabled {
		state = "enabled"
	}
	mode := cfg.Mode
	if mode != "auto" {
		mode = "approval"
	}
	return `
## Local learning loop

Per-project learning policy is stored in .codehelper/learning.json.

- Scope: project-only memory (no cross-project memory sharing).
- State: ` + state + `.
- Mode: ` + mode + ` (auto = apply improvements automatically, approval = require explicit approval).
- Memory store: ` + cfg.MemoryDir + `.
- Learned skills store: ` + cfg.SkillsDir + `.
- Transcript memory index source: ` + cfg.TranscriptsDir + `.

When enabled:
- Capture reusable patterns from successful sessions and verifications.
- Persist only project-scoped memory and preferences.
- Use transcript search to retrieve prior decisions for this project.
- If mode=auto, apply safe local improvements automatically after verify gates pass.
- If mode=approval, propose improvements and wait for explicit approval before applying.
`
}

// Write creates AGENTS.md at repo root with intake/planning/guardrail prompts.
func Write(repoRoot string) error {
	// Zero-footprint (external index) mode writes nothing into the repo.
	if paths.ExternalIndexHome() != "" {
		return nil
	}
	cfg, err := ensureLearningConfig(repoRoot)
	if err != nil {
		return err
	}
	body := header + learningPolicyBlock(cfg) + "\n" + prompts.AgentGuardrails + "\n\n" + prompts.PlanningContract + "\n\n" + prompts.IntakeProjectBrief + "\n"
	appendPath := filepath.Join(paths.RepoIndexDir(repoRoot), "AGENTS.append.md")
	if b, err := os.ReadFile(appendPath); err == nil {
		extra := strings.TrimSpace(string(b))
		if extra != "" {
			body += "\n<repo_append>\n" + extra + "\n</repo_append>\n"
		}
	}
	p := filepath.Join(repoRoot, "AGENTS.md")
	return os.WriteFile(p, []byte(body), 0o644)
}
