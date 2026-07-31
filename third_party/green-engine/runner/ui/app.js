/* Green Engine UI v0.2 — models hub, compute bar, ChatGPT-style chat */

const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);

const TAB_TITLES = {
  chat: "Chat",
  models: "Models",
  activity: "Activity",
  setup: "Setup",
};

const LS_COMPUTE = "ge-ui-compute";
const LS_ACTIVE = "ge-ui-active-model";
const LS_SESSIONS = "ge-ui-chat-sessions";
const LS_CURRENT = "ge-ui-chat-current";
const LS_MAX_TOKENS = "ge-ui-max-tokens";
const LS_CHAT_CTX = "ge-ui-chat-ctx";
const LS_AUTO_CONTINUE = "ge-ui-auto-continue";
const LS_MAX_CONTINUE = "ge-ui-max-continue";
const LS_SELF_REVIEW = "ge-ui-self-review";
const LS_PROJECTS = "ge-ui-projects";
const CHAT_CONTINUE_MAX = 64;
const CHAT_CONTINUE_MAX_CODE_DEFAULT = 8192;
function getSelfReview() {
  const v = localStorage.getItem(LS_SELF_REVIEW);
  return v === null ? true : v === "1";
}

function getMaxContinueRounds(codegen) {
  if (!codegen) return CHAT_CONTINUE_MAX;
  const v = parseInt(localStorage.getItem(LS_MAX_CONTINUE) || String(CHAT_CONTINUE_MAX_CODE_DEFAULT), 10);
  return Number.isFinite(v) && v > 0 ? v : CHAT_CONTINUE_MAX_CODE_DEFAULT;
}

const CHAT_CONTINUE_PROMPT =
  "Continue exactly where you left off. Do not repeat earlier content. Finish the remaining files and code.";

const EXTERNAL_UIS = [
  { name: "Odysseus", tag: "Workspace", url: "http://127.0.0.1:7000", blurb: "Full AI workspace — add OpenAI provider → :8767/v1", steps: ["docker compose up", "Custom provider → http://127.0.0.1:8767/v1", "API key: local"] },
  { name: "Open WebUI", tag: "Chat", url: "http://127.0.0.1:3000", blurb: "Popular local chat shell.", steps: ["Connections → OpenAI", "URL: http://127.0.0.1:8767/v1"] },
  { name: "Ollama", tag: "Alt", url: "http://127.0.0.1:11434", blurb: "Alternative runner for codehelper.", steps: ["Optional — Green compress/bench stays here"] },
];

let status = null;
let recommendations = null;
let selectedJobId = null;
let chatSessions = [];
let chatProjects = [];
let activeProjectId = null;
let currentSessionId = null;
let chatBusy = false;
let chatAbortController = null;
let bubbleRenderTimer = null;
const projectWorkspace = window.GeProject ? new GeProject.ProjectWorkspace() : null;
let activityFileSet = new Set();
let activitySaveTimer = null;
let syncProjectTimer = null;
let lastDiagnosisPhase = "";
let lastDiagnosisAt = 0;
let genLiveTimer = null;
let genLiveStart = 0;
let genLivePart = 1;
let currentActivityMsgIndex = -1;

function activityLogHtml(entries) {
  return (entries || [])
    .map(
      (e) =>
        `<div class="msg-activity-line ${esc(e.level)}"><span class="msg-activity-time">${esc(e.time)}</span> ${esc(e.msg)}</div>`
    )
    .join("");
}

function msgActivityPanelHtml(msg, index, expanded) {
  const status = msg.activityStatus || "Thinking…";
  const collapsed = expanded ? "" : " collapsed";
  const entries = msg.activityLog || [];
  return `<div class="msg-activity${collapsed}" data-msg-index="${index}">
    <button type="button" class="msg-activity-toggle" aria-expanded="${expanded ? "true" : "false"}">
      <span><span class="msg-activity-label">Thinking</span> · <span class="msg-activity-status">${esc(status)}</span></span>
      <span class="msg-activity-chevron">▸</span>
    </button>
    <div class="msg-activity-body">${activityLogHtml(entries)}</div>
  </div>`;
}

function msgReviewPanelHtml(review, index) {
  if (!review?.trim()) return "";
  const body = window.GeMarkdown ? GeMarkdown.render(review) : esc(review);
  return `<div class="msg-review collapsed" data-msg-index="${index}">
    <button type="button" class="msg-activity-toggle msg-review-toggle" aria-expanded="false">
      <span><span class="msg-activity-label">Review</span> · <span class="msg-activity-status">Self-check</span></span>
      <span class="msg-activity-chevron">▸</span>
    </button>
    <div class="msg-review-body bubble-body">${body}</div>
  </div>`;
}

function wireMsgReviewPanel(panel) {
  if (!panel || panel.dataset.wired) return;
  panel.dataset.wired = "1";
  panel.querySelector(".msg-review-toggle")?.addEventListener("click", () => {
    panel.classList.toggle("collapsed");
    const open = !panel.classList.contains("collapsed");
    panel.querySelector(".msg-review-toggle")?.setAttribute("aria-expanded", open ? "true" : "false");
  });
}

function findMsgActivityPanel(index) {
  return document.querySelector(`#chat-messages .chat-bubble[data-msg-index="${index}"] .msg-activity`);
}

function wireMsgActivityPanel(panel) {
  if (!panel || panel.dataset.wired) return;
  panel.dataset.wired = "1";
  panel.querySelector(".msg-activity-toggle")?.addEventListener("click", () => {
    panel.classList.toggle("collapsed");
    const open = !panel.classList.contains("collapsed");
    panel.querySelector(".msg-activity-toggle")?.setAttribute("aria-expanded", open ? "true" : "false");
  });
}

function updateGenLiveStatus(chars, fileCount, part) {
  const sec = Math.round((Date.now() - genLiveStart) / 1000);
  const bits = [];
  if (part > 1) bits.push(`part ${part}`);
  if (fileCount > 0) bits.push(`${fileCount} file${fileCount !== 1 ? "s" : ""}`);
  if (chars > 0) bits.push(`${chars} chars`);
  else bits.push(`${sec}s waiting`);
  bits.push(`${sec}s elapsed`);
  const label =
    fileCount > 0 ? "Building project…" : chars > 0 ? "Generating…" : "Waiting for model…";
  GenActivity.setStatus(`${label} · ${bits.join(" · ")}`);
}

function startGenLiveStatus(isCodegen, part = 1) {
  genLiveStart = Date.now();
  genLivePart = part;
  GenActivity.setStatus(isCodegen ? "Starting project…" : "Starting…");
  if (genLiveTimer) clearInterval(genLiveTimer);
  genLiveTimer = setInterval(() => {
    const bubble = $("#chat-messages")?.querySelector(".chat-bubble.streaming .bubble-body");
    const chars = bubble?.dataset?.charCount ? parseInt(bubble.dataset.charCount, 10) : 0;
    const files = Object.keys(projectWorkspace?.files || {}).length;
    updateGenLiveStatus(chars, files, genLivePart);
  }, 1000);
}

function stopGenLiveStatus() {
  if (genLiveTimer) clearInterval(genLiveTimer);
  genLiveTimer = null;
}

function persistActivityToMessage() {
  const sess = currentSession();
  if (!sess || currentActivityMsgIndex < 0 || !sess.messages[currentActivityMsgIndex]) return;
  const msg = sess.messages[currentActivityMsgIndex];
  msg.activityLog = GenActivity.entries.slice(-120);
  msg.activityStatus = GenActivity.statusText || "Done";
  msg.activityFiles = [...activityFileSet].filter((k) => !k.startsWith("__"));
  saveSessions();
}

function scheduleActivityPersist() {
  clearTimeout(activitySaveTimer);
  activitySaveTimer = setTimeout(persistActivityToMessage, 250);
}

function migrateSessionActivity(sess) {
  if (!sess?.activityLog?.length) return;
  for (let i = sess.messages.length - 1; i >= 0; i--) {
    if (sess.messages[i].role === "assistant") {
      if (!sess.messages[i].activityLog?.length) {
        sess.messages[i].activityLog = sess.activityLog;
        sess.messages[i].activityStatus = sess.activityStatus || "Done";
        if (sess.activityFiles?.length) sess.messages[i].activityFiles = sess.activityFiles;
      }
      break;
    }
  }
  delete sess.activityLog;
  delete sess.activityStatus;
  delete sess.activityFiles;
}

function attachActivityToMessage(index, expanded = true) {
  currentActivityMsgIndex = index;
  const sess = currentSession();
  const msg = sess?.messages[index];
  if (!msg) return;
  if (!msg.activityLog) msg.activityLog = [];
  if (!msg.activityStatus) msg.activityStatus = "Thinking…";
  GenActivity.entries = msg.activityLog.slice();
  GenActivity.statusText = msg.activityStatus;
  if (msg.activityFiles?.length) activityFileSet = new Set(msg.activityFiles);

  const bubble = document.querySelector(`#chat-messages .chat-bubble[data-msg-index="${index}"]`);
  if (!bubble) return;
  let panel = bubble.querySelector(".msg-activity");
  if (!panel) {
    bubble.insertAdjacentHTML("beforeend", msgActivityPanelHtml(msg, index, expanded));
    panel = bubble.querySelector(".msg-activity");
    wireMsgActivityPanel(panel);
  } else if (expanded) {
    panel.classList.remove("collapsed");
  }
  GenActivity.renderDom();
}

const GenActivity = {
  entries: [],
  statusText: "Idle",

  clear() {
    this.entries = [];
    this.statusText = "Idle";
    this.renderDom();
  },

  setStatus(text) {
    this.statusText = text;
    const panel = findMsgActivityPanel(currentActivityMsgIndex);
    const el = panel?.querySelector(".msg-activity-status");
    if (el) el.textContent = text;
    const sess = currentSession();
    if (currentActivityMsgIndex >= 0 && sess?.messages[currentActivityMsgIndex]) {
      sess.messages[currentActivityMsgIndex].activityStatus = text;
    }
    scheduleActivityPersist();
  },

  log(level, msg) {
    const time = new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
    this.entries.push({ level, msg, time });
    if (this.entries.length > 150) this.entries.shift();
    this.renderDom();
  },

  renderDom() {
    const panel = findMsgActivityPanel(currentActivityMsgIndex);
    const body = panel?.querySelector(".msg-activity-body");
    if (body) {
      body.innerHTML = activityLogHtml(this.entries);
      body.scrollTop = body.scrollHeight;
    }
    scheduleActivityPersist();
  },

  onStart(codegen, msgIndex, reason) {
    activityFileSet.clear();
    lastDiagnosisPhase = "";
    this.clear();
    attachActivityToMessage(msgIndex, true);
    if (codegen) {
      this.log("info", 'Format: NDJSON — {"path":"…","content":"…"} one line per file');
      this.log("info", `Project mode: ${reason || "detected"}`);
    } else {
      this.log("warn", `Chat mode (not project): ${reason || "no keywords"} — use + New Project or say “create wordpress plugin”`);
      this.log("info", "Sending to model");
    }
    this.setStatus(codegen ? "Waiting for JSON files…" : "Generating…");
    startGenLiveStatus(codegen, 1);
  },

  onRound(n, chars) {
    genLivePart = n;
    const files = Object.keys(projectWorkspace?.files || {}).length;
    this.setStatus(`Part ${n} · ${chars} chars · ${files} file${files !== 1 ? "s" : ""}`);
    updateGenLiveStatus(chars, files, n);
  },

  onFile(path) {
    if (activityFileSet.has(path)) return;
    activityFileSet.add(path);
    this.log("file", `+ ${path}`);
    this.setStatus(`${activityFileSet.size} file${activityFileSet.size !== 1 ? "s" : ""}`);
  },

  onValidationResults(results) {
    if (!results || chatBusy || window.__geChatBusy) return;
    for (const [path, r] of Object.entries(results)) {
      if (r.ok) {
        if (r.note) this.log("info", `${path}: ${r.note}`);
        else this.log("ok", `✓ ${path}`);
      } else {
        const err = (r.error || "syntax error").split("\n")[0].slice(0, 120);
        this.log("err", `✗ ${path}: ${err}`);
      }
    }
    const bad = Object.values(results).filter((v) => v && !v.ok).length;
    const n = Object.keys(results).length;
    this.setStatus(bad ? `${n} files · ${bad} error${bad !== 1 ? "s" : ""}` : `${n} files · all ok`);
  },

  onDone(truncated, fileCount) {
    if (truncated) {
      this.log("warn", "Paused — auto-continuing…");
      this.setStatus(fileCount ? `${fileCount} files · continuing…` : "Continuing…");
    } else {
      this.log("ok", fileCount ? `Done — ${fileCount} files` : "Done");
      this.setStatus(fileCount ? `Done · ${fileCount} files` : "Done");
      const panel = findMsgActivityPanel(currentActivityMsgIndex);
      panel?.classList.add("collapsed");
      stopGenLiveStatus();
    }
    scheduleActivityPersist();
  },
};

function resetActivityTracking() {
  activityFileSet.clear();
  currentActivityMsgIndex = -1;
  GenActivity.clear();
  stopGenLiveStatus();
}

// --- state ---
function getComputeMode() {
  return localStorage.getItem(LS_COMPUTE) || "auto";
}

function setComputeMode(mode) {
  const prev = getComputeMode();
  localStorage.setItem(LS_COMPUTE, mode);
  $$("#global-compute button").forEach((b) => {
    b.classList.toggle("active", b.dataset.mode === mode);
  });
  loadRecommendations();
  const q = $("#hf-search-q")?.value?.trim();
  if (q) runHfSearch();
  if (prev !== mode && status?.servers?.chat?.up && !chatBusy) {
    const path = $("#chat-active-model")?.value;
    if (path && mode !== "cpu") {
      GenActivity?.log?.("info", `Compute → ${mode} — restarting chat with ${mode === "gpu" ? "max" : "shared"} GPU…`);
      startChatServer(path).then(refreshStatus).catch(() => {});
    }
  }
}

function getMaxTokens() {
  return parseInt(localStorage.getItem(LS_MAX_TOKENS) || "8192", 10);
}

function getChatCtxOverride() {
  return parseInt(localStorage.getItem(LS_CHAT_CTX) || "0", 10);
}

function getAutoContinue() {
  return localStorage.getItem(LS_AUTO_CONTINUE) !== "0";
}

function loadSettings() {
  const mt = $("#setting-max-tokens");
  const cx = $("#setting-ctx");
  const ac = $("#setting-auto-continue");
  const mc = $("#setting-max-continue");
  if (mt) mt.value = String(getMaxTokens());
  if (cx) cx.value = String(getChatCtxOverride());
  if (ac) ac.checked = getAutoContinue();
  if (mc) mc.value = String(getMaxContinueRounds(true));
  const sr = $("#setting-self-review");
  if (sr) sr.checked = getSelfReview();
}

function saveSettings() {
  if ($("#setting-max-tokens")) {
    localStorage.setItem(LS_MAX_TOKENS, $("#setting-max-tokens").value);
  }
  if ($("#setting-ctx")) {
    localStorage.setItem(LS_CHAT_CTX, $("#setting-ctx").value);
  }
  if ($("#setting-auto-continue")) {
    localStorage.setItem(LS_AUTO_CONTINUE, $("#setting-auto-continue").checked ? "1" : "0");
  }
  if ($("#setting-max-continue")) {
    localStorage.setItem(LS_MAX_CONTINUE, $("#setting-max-continue").value);
  }
  if ($("#setting-self-review")) {
    localStorage.setItem(LS_SELF_REVIEW, $("#setting-self-review").checked ? "1" : "0");
  }
}

function getActiveModel() {
  try {
    return JSON.parse(localStorage.getItem(LS_ACTIVE) || "null");
  } catch {
    return null;
  }
}

function setActiveModel(m) {
  localStorage.setItem(LS_ACTIVE, JSON.stringify(hydrateActiveModel(m)));
  renderActiveModel();
  syncChatModelSelect();
}

function hydrateActiveModel(m) {
  if (!m) return m;
  const path = m.local_path || m.path;
  if (!path) return m;
  const disk = status?.models?.find((x) => x.path === path);
  if (disk) {
    return {
      ...m,
      name: m.name || disk.name,
      downloaded: true,
      compressed: disk.compressed,
      compressed_path: disk.compressed_path,
      local_path: disk.path,
      size_gb: (disk.size || 0) / (1024 ** 3),
    };
  }
  return m;
}

function syncActiveFromStatus() {
  const m = getActiveModel();
  if (!m) return;
  const hydrated = hydrateActiveModel(m);
  if (JSON.stringify(hydrated) !== JSON.stringify(m)) {
    localStorage.setItem(LS_ACTIVE, JSON.stringify(hydrated));
  }
}

function refreshModelIndex() {
  const local = (status?.models || []).map((m) => ({
    id: m.path,
    name: m.name,
    local_path: m.path,
    downloaded: true,
    compressed: m.compressed,
    compressed_path: m.compressed_path,
    size_gb: (m.size || 0) / (1024 ** 3),
  }));
  window._allModels = [
    ...(recommendations?.recommended || []),
    ...(window._hfResults || []),
    ...local,
  ];
}

function modelFromPath(path) {
  const disk = status?.models?.find((m) => m.path === path);
  if (disk) {
    return {
      id: disk.path,
      name: disk.name,
      local_path: disk.path,
      downloaded: true,
      compressed: disk.compressed,
      compressed_path: disk.compressed_path,
      size_gb: (disk.size || 0) / (1024 ** 3),
    };
  }
  return { id: path, name: path.split("/").pop(), local_path: path, downloaded: true, size_gb: 2 };
}

function modelById(id) {
  refreshModelIndex();
  return window._allModels.find(
    (m) => m.id === id || m.local_path === id || m.path === id
  );
}

function isModelCompressed(path) {
  const row = status?.models?.find((m) => m.path === path);
  return Boolean(row?.compressed);
}

async function removeModelPath(path) {
  const name = path.split("/").pop();
  if (!confirm(`Remove ${name} and its compressed data from ~/.green?`)) return;
  await startJob({ action: "remove_model", path });
  const active = getActiveModel();
  if (active?.local_path === path || active?.path === path) {
    localStorage.removeItem(LS_ACTIVE);
  }
  await refreshStatus();
  renderActiveModel();
}

async function reinstallModel(m) {
  if (!m?.repo) {
    alert("Pick a catalog model (has Hugging Face repo) for fresh Q4 download.");
    return;
  }
  const path = m.local_path || m.path || "";
  const label = m.name || m.repo;
  if (
    path &&
    !confirm(
      `Remove ${path.split("/").pop()} and re-download ${label} (${m.file || "*Q4_K_M.gguf"})?`
    )
  ) {
    return;
  }
  if (!path && !confirm(`Download ${label} and compress?`)) return;
  setActiveModel(m);
  await startJob({
    action: "reinstall_model",
    path,
    repo: m.repo,
    file: m.file || "*Q4_K_M.gguf",
    auto_compress: true,
    restart_chat: false,
  });
}

async function useModelPath(path, opts = {}) {
  const m = modelFromPath(path);
  setActiveModel(m);
  const size = m.size_gb || 2;
  const needsCompress = opts.autoCompress !== false && !isModelCompressed(path);
  const needsRestart = opts.restartChat !== false;

  if (!needsCompress && needsRestart && !opts.pull) {
    await startChatServer(path);
    await refreshStatus();
    return;
  }

  const body = {
    action: "use_model",
    model: path,
    gpu_layers: resolveGpuLayers(size),
    auto_compress: needsCompress,
    restart_chat: needsRestart,
    mcp: false,
  };
  if (opts.pull) {
    body.repo = opts.repo || m.repo || "";
    body.file = opts.file || m.file || "*Q4_K_M.gguf";
  }
  await startJob(body);
}

async function activateModel(m, opts = {}) {
  if (!m) return;
  setActiveModel(m);
  if (!m.downloaded && !m.local_path) {
    if (opts.pull !== false && m.repo) {
      await startJob({
        action: "use_model",
        repo: m.repo,
        file: m.file || "*Q4_K_M.gguf",
        gpu_layers: resolveGpuLayers(m.size_gb || 2),
        auto_compress: true,
        restart_chat: opts.restartChat !== false,
        mcp: false,
      });
    }
    return;
  }
  const path = m.local_path || m.path;
  if (!path) return;
  if (opts.restartChat !== false || opts.autoCompress !== false) {
    await useModelPath(path, {
      autoCompress: opts.autoCompress !== false,
      restartChat: opts.restartChat !== false,
    });
  }
}

function resolveCtx(path) {
  const override = getChatCtxOverride();
  if (override > 0) return override;
  const hw = recommendations?.hardware || status?.hardware || {};
  const vram = hw.vram_gb || 0;
  const disk = status?.models?.find((m) => m.path === path);
  const size = disk?.size || 0;
  if (vram >= 12 && size > 0 && size < 6_000_000_000) return 16384;
  if (size > 2_000_000_000) return 8192;
  return 4096;
}

function renderChatRuntime() {
  const rt = status?.chat_runtime;
  const pill = $("#chat-server-pill");
  if (!pill) return;
  if (status?.servers?.chat?.up) {
    pill.textContent = gpuStatusLabel();
    pill.className = rt?.gpu_layers > 0 ? "pill ok" : rt?.loaded && (status?.hardware?.has_gpu || recommendations?.hardware?.has_gpu) ? "pill warn" : "pill ok";
    const gpu = rt?.gpu_layers > 0 ? `${rt.gpu_layers} GPU layers` : "CPU inference";
    pill.title = rt?.loaded
      ? `${rt.model_name || "model"} · ctx ${rt.ctx || "?"} · ${gpu}`
      : "Chat server running";
  }
}

function resolveGpuLayers(modelSizeGb) {
  const mode = getComputeMode();
  const hw = recommendations?.hardware || status?.hardware || {};
  const vram = hw.vram_gb || 0;
  const hasGpu = hw.has_gpu || hw.cuda_available;
  if (mode === "cpu") return 0;
  if (!hasGpu) return 0;
  if (mode === "gpu") return 99;
  // Shared GPU — partial offload so desktop/browser keeps VRAM headroom
  if (vram >= 12) return 20;
  if (vram >= 6) return 16;
  return 10;
}

function gpuLayersMatch(want, got) {
  if (want === 0) return got === 0;
  if (got <= 0) return false;
  if (getComputeMode() === "gpu") return got >= 20;
  return got > 0 && got <= want + 14;
}

function gpuStatusLabel() {
  const rt = status?.chat_runtime;
  const hw = recommendations?.hardware || status?.hardware || {};
  if (!status?.servers?.chat?.up) return "offline";
  const layers = rt?.gpu_layers ?? 0;
  const total = rt?.gpu_layers_total;
  if (layers > 0) {
    const mode = getComputeMode();
    const tag = mode === "gpu" ? "max" : "shared";
    const suffix = total ? `${layers}/${total}` : String(layers);
    return `GPU ${suffix} (${tag}) ✓`;
  }
  if (rt?.cuda_active && hw.has_gpu) return "GPU active ✓";
  if (hw.has_gpu && rt?.loaded) return "GPU (detecting…) ✓";
  if (hw.has_gpu) return "CPU only ⚠";
  return "server online ✓";
}

function setTopbarChatMode(on) {
  $("#topbar-chat-server")?.classList.toggle("hidden", !on);
}

// --- nav ---
$$(".nav-list button").forEach((btn) => {
  btn.addEventListener("click", () => {
    const tab = btn.dataset.tab;
    $$(".nav-list button").forEach((b) => b.classList.remove("active"));
    $$(".panel").forEach((p) => p.classList.remove("active"));
    btn.classList.add("active");
    $(`#panel-${tab}`).classList.add("active");
    $("#page-title").textContent = TAB_TITLES[tab] || tab;
    $("#main-content").classList.toggle("chat-mode", tab === "chat");
    setTopbarChatMode(tab === "chat");
  });
});

function switchTab(name) {
  $(`.nav-list button[data-tab="${name}"]`)?.click();
}

// --- API ---
async function api(path, opts = {}) {
  const res = await fetch(path, { headers: { "Content-Type": "application/json" }, ...opts });
  const text = await res.text();
  let data;
  try {
    data = JSON.parse(text);
  } catch {
    throw new Error(res.ok ? "Not JSON — wrong server on :8780?" : `HTTP ${res.status}`);
  }
  if (!res.ok && data.error) throw new Error(data.error);
  if (data.ok === false && data.error) throw new Error(data.error);
  return data;
}

async function startJob(body) {
  const data = await api("/api/jobs", { method: "POST", body: JSON.stringify(body) });
  if (data.job_id) {
    selectedJobId = data.job_id;
    switchTab("activity");
    pollJobs();
    tailLog();
  }
  return data;
}

async function serverCmd(service, cmd, extra = {}) {
  return api("/api/servers", { method: "POST", body: JSON.stringify({ service, cmd, ...extra }) });
}

function esc(s) {
  const d = document.createElement("div");
  d.textContent = String(s);
  return d.innerHTML;
}

function fmtBytes(n) {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + " GB";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + " MB";
  return Math.round(n / 1e3) + " KB";
}

function fmtGb(g) {
  return g < 1 ? `${(g * 1024).toFixed(0)} MB` : `${g.toFixed(1)} GB`;
}

function badgeClass(v) {
  if (v === "excellent" || v === "good") return "ok";
  if (v === "tight") return "warn";
  return "err";
}

// --- recommendations ---
async function loadRecommendations() {
  try {
    recommendations = await api(`/api/recommendations?compute=${getComputeMode()}`);
    renderRecommendations();
    renderHwSummary();
  } catch (e) {
    console.error(e);
  }
}

function renderHwSummary() {
  const hw = recommendations?.hardware;
  if (!hw) return;
  const parts = [`${hw.ram_gb} GB RAM`, `${hw.cores} cores`];
  if (hw.gpu_name) parts.push(`${hw.gpu_name} (${hw.vram_gb} GB VRAM)`);
  else parts.push("no GPU detected");
  const mode = getComputeMode();
  $("#hw-summary").textContent = `${parts.join(" · ")} · compute: ${mode.toUpperCase()}`;
}

function renderActiveModel() {
  const m = hydrateActiveModel(getActiveModel());
  const el = $("#active-model-bar");
  if (!m) {
    el.innerHTML = '<span class="hint">No model selected — click a card below.</span>';
    return;
  }
  const path = m.local_path || m.path;
  const id = m.id || path;
  el.innerHTML = `
    <div class="active-model-inner">
      <strong title="${esc(path || m.name)}">${esc(m.name || (path ? path.split("/").pop() : "?"))}</strong>
      <p class="hint">${m.compressed ? "Compressed on disk · chat still uses .gguf" : m.downloaded ? "GGUF on disk" : "Catalog only"}</p>
      <div class="btn-row">
        ${path ? `<button class="btn btn-sm primary" data-model-use="${esc(id)}">Use in chat</button>` : ""}
        ${m.repo ? `<button class="btn btn-sm" data-active-reinstall="${esc(id)}">Fresh Q4</button>` : ""}
        ${path ? `<button class="btn btn-sm danger" data-active-remove="${esc(path)}">Remove</button>` : ""}
      </div>
    </div>`;
  wireModelButtons(el);
  el.querySelectorAll("[data-active-remove]").forEach((btn) => {
    btn.addEventListener("click", () => removeModelPath(btn.dataset.activeRemove).catch(alertErr));
  });
  el.querySelectorAll("[data-active-reinstall]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const model = modelById(btn.dataset.activeReinstall) || m;
      reinstallModel(model).catch(alertErr);
    });
  });
}

function renderModelCard(m) {
  const useCases = (m.use_cases || []).slice(0, 3).map(
    (uc) => `<span class="tag">${esc(uc)}</span>`
  ).join("");
  const id = m.id || m.repo?.replace(/\//g, "--") || m.local_path;
  const repo = m.repo || "";
  const name = m.name || repo.split("/").pop() || "Model";
  const fit = m.verdict || (m.downloaded ? "on disk" : "?");
  return `
    <div class="card model-card">
      <div class="model-card-head">
        <h3 title="${esc(name)}">${esc(name)}</h3>
        <span class="badge ${badgeClass(m.verdict)}">${esc(fit)}</span>
      </div>
      <p class="model-card-meta">${fmtGb(m.size_gb || 1)} · ${esc(m.suggested_backend || "auto")} · ${m.suggested_gpu_layers ?? "?"} GPU layers</p>
      <div class="tag-row">${useCases}${m.compressed ? '<span class="badge ok">compressed</span>' : m.downloaded ? '<span class="badge warn">raw gguf</span>' : ""}</div>
      <div class="btn-row">
        <button class="btn btn-sm primary" data-model-use="${esc(id)}">Use</button>
        ${m.repo ? `<button class="btn btn-sm" data-model-reinstall="${esc(id)}">Fresh Q4</button>` : ""}
        ${m.downloaded && m.local_path ? `<button class="btn btn-sm danger" data-model-remove-path="${esc(m.local_path)}">Remove</button>` : !m.downloaded && m.repo ? `<button class="btn btn-sm" data-model-pull="${esc(id)}">Download</button>` : ""}
      </div>
    </div>`;
}

function wireModelButtons(root) {
  root.querySelectorAll("[data-model-pull]").forEach((btn) => {
    btn.addEventListener("click", () => pullModel(btn.dataset.modelPull));
  });
  root.querySelectorAll("[data-model-use], [data-model-chat]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const m = modelById(btn.dataset.modelUse || btn.dataset.modelChat);
      if (m) {
        activateModel(m, { restartChat: true, autoCompress: true }).catch(alertErr);
        switchTab("chat");
      }
    });
  });
  root.querySelectorAll("[data-model-compress]").forEach((btn) => {
    btn.addEventListener("click", () => compressModel(btn.dataset.modelCompress));
  });
  root.querySelectorAll("[data-model-select]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const m = modelById(btn.dataset.modelSelect);
      if (m) setActiveModel(m);
    });
  });
  root.querySelectorAll("[data-model-remove-path]").forEach((btn) => {
    btn.addEventListener("click", () => removeModelPath(btn.dataset.modelRemovePath).catch(alertErr));
  });
  root.querySelectorAll("[data-model-reinstall]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const m = modelById(btn.dataset.modelReinstall);
      if (m) reinstallModel(m).catch(alertErr);
    });
  });
}

function wireAllModelCards(root) {
  wireModelButtons(root);
}

async function runHfSearch() {
  const q = $("#hf-search-q").value.trim() || "llama";
  const fits = $("#filter-fits-only").checked ? "1" : "";
  const uc = $("#filter-use-case").value;
  const rel = $("#filter-reliability").value;
  $("#hf-search-results").innerHTML = '<p class="hint">Searching Hugging Face…</p>';
  try {
    const data = await api(
      `/api/hf/search?q=${encodeURIComponent(q)}&compute=${getComputeMode()}&fits_only=${fits}&use_case=${uc}&reliability=${rel}&limit=30`
    );
    const results = data.results || [];
    if (!results.length) {
      $("#hf-search-results").innerHTML = '<p class="hint">No GGUF models match — try another query or loosen filters.</p>';
      return;
    }
    $("#hf-search-results").innerHTML = results.map((m) => renderModelCard(m)).join("");
    wireAllModelCards($("#hf-search-results"));
    // stash HF results for lookup by id
    window._hfResults = results;
  } catch (e) {
    $("#hf-search-results").innerHTML = `<p class="hint">Search failed: ${esc(e.message)}</p>`;
  }
}

function renderRecommendations() {
  const recs = recommendations?.recommended || [];
  $("#model-recommendations").innerHTML = recs.length
    ? recs.map((m) => renderModelCard(m)).join("")
    : '<p class="hint">No curated models.</p>';
  wireAllModelCards($("#model-recommendations"));

  const local = status?.models || [];
  $("#downloaded-models").innerHTML = local.length
    ? local
        .map((m) => renderModelCard({
          id: m.path,
          name: m.name,
          local_path: m.path,
          downloaded: true,
          compressed: m.compressed,
          size_gb: (m.size || 0) / (1024 ** 3),
          verdict: m.compressed ? "compressed" : "on disk",
          suggested_backend: "local",
        }))
        .join("")
    : '<p class="hint">Nothing on disk — use Recommended or Search HF.</p>';

  wireAllModelCards($("#downloaded-models"));
}

async function pullModel(id) {
  const m = modelById(id);
  if (!m) return;
  setActiveModel(m);
  const file = m.file?.includes("*") ? m.file : m.file || "*Q4_K_M.gguf";
  await startJob({
    action: "use_model",
    repo: m.repo,
    file,
    auto_compress: true,
    restart_chat: false,
  });
}

async function setupMcpFor(id) {
  const m = modelById(id);
  if (!m) return;
  setActiveModel(m);
  await startJob({
    action: "setup_mcp",
    repo: m.repo,
    file: m.file || "*Q4_K_M.gguf",
  });
}

async function setupChatForActive() {
  const m = getActiveModel();
  if (!m?.repo && !m?.local_path) {
    alert("Select a model first.");
    return;
  }
  const size = m.size_gb || 2;
  await startJob({
    action: "setup_chat",
    repo: m.repo || "",
    file: m.file || "*Q4_K_M.gguf",
    model: m.local_path || "",
    gpu_layers: resolveGpuLayers(size),
    auto_compress: true,
    mcp: (m.use_cases || []).includes("mcp"),
  });
}

async function setupMcpForActive() {
  const m = getActiveModel();
  const repo = m?.repo || "bartowski/Llama-3.2-1B-Instruct-GGUF";
  const file = m?.file || "*Q4_K_M.gguf";
  if (m) setActiveModel(m);
  await startJob({ action: "setup_mcp", repo, file });
}

async function chatWithModel(id) {
  const m = modelById(id);
  if (!m) return;
  if (!m.downloaded) {
    await activateModel(m, { restartChat: true, autoCompress: true, pull: true });
    switchTab("activity");
    return;
  }
  await useModelPath(m.local_path, { autoCompress: true, restartChat: true });
  switchTab("chat");
}

async function compressModel(id) {
  const m = modelById(id);
  if (!m?.local_path) {
    alert("Pull the model first.");
    return;
  }
  compressPath(m.local_path);
}

function compressPath(path) {
  const base = path.split("/").pop().replace(/\.gguf$/i, "");
  startJob({
    action: "compress",
    gguf: path,
    out: `~/.green/compressed/${base}`,
    methods: "green_optimal,green_adaptive",
    layers: "0,1",
  }).catch(alertErr);
}

async function startChatServer(modelPath) {
  const path =
    modelPath || $("#chat-active-model")?.value || getActiveModel()?.local_path || getActiveModel()?.path;
  if (!path) {
    alert("Select a model from the dropdown or Models tab first.");
    return;
  }
  const disk = status?.models?.find((m) => m.path === path);
  const m = hydrateActiveModel(getActiveModel() || modelFromPath(path));
  const size = m?.size_gb || disk?.size / (1024 ** 3) || 2;
  const gpuLayers = resolveGpuLayers(size);
  const hw = recommendations?.hardware || status?.hardware || {};
  $("#chat-server-pill").textContent = "starting…";
  $("#chat-server-pill").className = "pill warn";
  if (gpuLayers === 0 && hw.has_gpu && getComputeMode() !== "cpu") {
    GenActivity.log("warn", `GPU detected (${hw.gpu_name || "CUDA"}) but 0 layers — check Compute bar (try GPU mode)`);
  }
  try {
    await serverCmd("chat", "start", {
      model: path,
      gpu_layers: gpuLayers,
      ctx: resolveCtx(path),
      mcp: false,
    });
    for (let i = 0; i < 24; i++) {
      await refreshStatus();
      if (status?.servers?.chat?.up) {
        const rt = status?.chat_runtime;
        const got = rt?.gpu_layers ?? 0;
        if (gpuLayers > 0 && got === 0) {
          GenActivity.log("err", "Server started on CPU — run: ge chat install (CUDA build) or pick GPU in Compute bar");
        } else if (got > 0) {
          const tag = getComputeMode() === "gpu" ? "max" : "shared";
          GenActivity.log("info", `Model on GPU ${got}${rt?.gpu_layers_total ? "/" + rt.gpu_layers_total : ""} layers (${tag}, ctx ${rt?.ctx || "?"})`);
        } else {
          GenActivity.log("info", `Model loaded on CPU (ctx ${rt?.ctx || "?"})`);
        }
        return;
      }
      await new Promise((r) => setTimeout(r, 500));
    }
    throw new Error("Server did not become ready — check Activity output or try a smaller Q4 model");
  } catch (e) {
    await refreshStatus();
    alertErr(e);
    throw e;
  }
}

// --- compute bar ---
function wireComputeButtons(sel) {
  document.querySelectorAll(`${sel} button`).forEach((btn) => {
    btn.addEventListener("click", () => setComputeMode(btn.dataset.mode));
  });
}
wireComputeButtons("#global-compute");
setComputeMode(getComputeMode());
loadSettings();

$("#btn-toggle-settings")?.addEventListener("click", () => {
  const d = $("#settings-drawer");
  if (d) d.hidden = !d.hidden;
});
$("#setting-max-tokens")?.addEventListener("change", saveSettings);
$("#setting-ctx")?.addEventListener("change", saveSettings);
$("#setting-auto-continue")?.addEventListener("change", saveSettings);
$("#setting-max-continue")?.addEventListener("change", saveSettings);
$("#setting-self-review")?.addEventListener("change", saveSettings);

$$(".model-subtabs button").forEach((btn) => {
  btn.addEventListener("click", () => {
    $$(".model-subtabs button").forEach((b) => b.classList.remove("active"));
    $$(".msub-panel").forEach((p) => p.classList.remove("active"));
    btn.classList.add("active");
    $(`#msub-${btn.dataset.msub}`).classList.add("active");
  });
});

$("#btn-hf-search").addEventListener("click", () => runHfSearch());
$("#hf-search-q").addEventListener("keydown", (e) => {
  if (e.key === "Enter") runHfSearch();
});
$("#filter-fits-only").addEventListener("change", () => { if ($("#hf-search-q").value.trim()) runHfSearch(); });
$("#filter-use-case").addEventListener("change", () => { if ($("#hf-search-q").value.trim()) runHfSearch(); });
$("#filter-reliability").addEventListener("change", () => { if ($("#hf-search-q").value.trim()) runHfSearch(); });
$("#btn-setup-mcp").addEventListener("click", () => setupMcpForActive().catch(alertErr));
$("#btn-setup-chat").addEventListener("click", () => setupChatForActive().catch(alertErr));

// --- status ---
function renderHealth() {
  if (!status) return;
  const s = status;
  $("#status-dot").className = "status-dot " + (s.ge_bin ? "ok" : "warn");
  $("#status-text").textContent = s.ge_bin ? "ready" : "setup";
  const hw = s.hardware || {};
  const hwStr = [hw.cores && `${hw.cores}c`, hw.ram_gb && `${hw.ram_gb}G RAM`, hw.gpu_name || "CPU"].filter(Boolean).join(" · ");
  $("#sidebar-hw").textContent = hwStr;
  if (recommendations?.hardware) renderHwSummary();
  else if ($("#hw-summary")) $("#hw-summary").textContent = hwStr || "Hardware unknown";

  const pill = $("#chat-server-pill");
  if (s.servers?.chat?.up) {
    renderChatRuntime();
  } else if (pill) {
    pill.textContent = "server offline";
    pill.className = "pill off";
  }

  $("#health-cards").innerHTML = `
    <div class="card"><h3>ge</h3><div class="stat-row"><span>CLI</span><span class="badge ${s.ge_bin ? "ok" : "err"}">${s.ge_bin ? "ok" : "missing"}</span></div></div>
    <div class="card"><h3>Compress</h3><div class="stat-row"><span>tool</span><span class="badge ${s.greencompress ? "ok" : "err"}">${s.greencompress ? "ok" : "missing"}</span></div></div>
    <div class="card"><h3>Models</h3><div class="stat-row"><span>count</span><span class="stat-value">${s.model_count || 0}</span></div></div>`;

  const sv = s.servers || {};
  $("#server-summary").innerHTML = ["embed", "chat", "translate"]
    .map((n) => `<div class="stat-row"><span>${n}</span><span class="badge ${sv[n]?.up ? "ok" : "off"}">${sv[n]?.up ? "up" : "down"}</span></div>`)
    .join("");
  const rt = status?.chat_runtime;
  if (rt?.loaded) {
    $("#server-summary").innerHTML += `<div class="stat-row"><span>chat model</span><span class="stat-value truncate" title="${esc(rt.model_path)}">${esc(rt.model_name)}</span></div>`;
  }
}

async function refreshStatus() {
  try {
    status = await api("/api/status");
    if (status.service !== "green-ui") {
      showPortWarn("Wrong service on :8780 — run: ge ui serve --kill-conflict");
      return;
    }
    hidePortWarn();
    syncActiveFromStatus();
    renderHealth();
    fillChatModelSelect();
    if (recommendations) renderRecommendations();
    renderActiveModel();
  } catch (e) {
    showPortWarn(e.message);
  }
}

function showPortWarn(msg) {
  const el = $("#port-warn");
  el.textContent = msg;
  el.classList.add("visible");
}

function hidePortWarn() {
  $("#port-warn").classList.remove("visible");
}

function wantsLongCodegen(messages) {
  const sess = currentSession();
  if (sess?.projectId) return true;
  return window.GeProject?.wantsCodegenProject(messages, { projectId: sess?.projectId }) ?? false;
}

function codegenDetectReason(messages) {
  const sess = currentSession();
  return window.GeProject?.explainCodegenDetect?.(messages, { projectId: sess?.projectId }) || "unknown";
}

const GE_DEBUG_KEY = "ge-ui-debug-log";

function geDebugPush(type, detail) {
  const row = { t: new Date().toISOString(), type, ...detail };
  try {
    const arr = JSON.parse(localStorage.getItem(GE_DEBUG_KEY) || "[]");
    arr.push(row);
    while (arr.length > 200) arr.shift();
    localStorage.setItem(GE_DEBUG_KEY, JSON.stringify(arr));
  } catch (_) {}
  if (typeof console !== "undefined") console.debug("[ge-ui]", type, detail);
}

function diagnoseGenerationOutput(full, baseMessages) {
  if (!wantsLongCodegen(baseMessages) || !window.GeProject?.analyzeGenerationOutput) return;
  const a = GeProject.analyzeGenerationOutput(full);
  if (a.phase === "empty") return;
  const now = Date.now();
  if (a.phase === lastDiagnosisPhase && now - lastDiagnosisAt < 8000) return;
  lastDiagnosisPhase = a.phase;
  lastDiagnosisAt = now;
  const level = a.phase === "prose" || a.phase === "markdown" ? "warn" : "info";
  GenActivity.log(level, a.hint);
  if (a.preview && (a.phase === "prose" || a.phase === "markdown")) {
    GenActivity.log("info", `Output starts: “${a.preview}${full.length > 100 ? "…" : ""}”`);
  }
}

function syncProjectFromGeneration(full, baseMessages, flush = true) {
  if (!projectWorkspace || !wantsLongCodegen(baseMessages)) return null;
  const run = () => {
    const before = new Set(Object.keys(projectWorkspace.files));
    projectWorkspace.ingestText(full, true, true);
    Object.keys(projectWorkspace.files).forEach((p) => {
      if (!before.has(p)) GenActivity.onFile(p);
    });
    if (full.length >= 80) diagnoseGenerationOutput(full, baseMessages);
    persistSessionProjectFiles();
    renderPersistedFilesList();
    return projectWorkspace.files;
  };
  if (!flush) {
    clearTimeout(syncProjectTimer);
    syncProjectTimer = setTimeout(run, 350);
    return projectWorkspace.files;
  }
  clearTimeout(syncProjectTimer);
  return run();
}

function projectStillIncomplete(full, baseMessages) {
  if (!window.GeProject || !wantsLongCodegen(baseMessages)) return false;
  return GeProject.projectIncomplete(
    projectWorkspace?.files || GeProject.parseFilesFromText(full),
    full,
    baseMessages
  );
}

function looksTruncated(content, finishReason, userMessages = []) {
  if (finishReason === "length") return true;
  if (!content) return false;
  if (projectStillIncomplete(content, userMessages)) return true;

  if (wantsLongCodegen(userMessages)) {
    const c = String(content);
    if (GeProject?.usesJsonFormat?.(c)) {
      if (GeProject.hasOpenGenerationTags?.(c)) return true;
      if (!GeProject.jsonGenerationDone?.(c) && Object.keys(GeProject.parseFilesFromText(c) || {}).length > 0)
        return true;
    }
    const openFiles = (c.match(/<file\b/gi) || []).length;
    const closeFiles = (c.match(/<\/file>/gi) || []).length;
    if (openFiles > closeFiles) return true;
    if (/<green_project\b/i.test(c) && !/<\/green_project>/i.test(c)) return true;
  }

  const ticks = (content.match(/```/g) || []).length;
  if (ticks % 2 === 1) return true;
  const tail = content.trim().slice(-160);
  if (/[=>(,\[{:]\s*$/.test(tail)) return true;
  if (/\\$/.test(tail)) return true;
  if (/\b(function|class|public|private|array|return)\s*\.?\s*$/i.test(tail)) return true;
  if (/initialize our class\.?\s*$/i.test(tail)) return true;
  if (/will include and initialize/i.test(tail)) return true;

  if (wantsLongCodegen(userMessages)) {
    const validCount = Object.keys(GeProject?.parseFilesFromText(content) || {}).length;
    if (validCount === 0 && content.length > 800) return true;
  }

  return false;
}

function continuationMessages(baseMessages, partial, contOpts) {
  const codegen = wantsLongCodegen(baseMessages);
  const files = projectWorkspace?.files || GeProject.parseFilesFromText(partial);
  let prompt = CHAT_CONTINUE_PROMPT;
  if (codegen && window.GeProject) {
    prompt = GeProject.buildContinuePrompt(files, partial, baseMessages, contOpts);
  }
  if (codegen) {
    const userRequest = baseMessages
      .filter((m) => m.role === "user")
      .map((m) => m.content)
      .join("\n\n");
    const paths = Object.keys(files).sort();
    return [
      { role: "user", content: userRequest },
      {
        role: "assistant",
        content:
          paths.length > 0
            ? `[${paths.length} files saved: ${paths.join(", ")}]`
            : "Starting NDJSON project output.",
      },
      { role: "user", content: prompt },
    ];
  }
  return [
    ...baseMessages,
    { role: "assistant", content: partial },
    { role: "user", content: prompt },
  ];
}

function prepareApiMessages(baseMessages, round, partial, opts = {}) {
  const sess = currentSession();
  const codegen = wantsLongCodegen(baseMessages);
  let msgs;

  if (opts.formatRetry) {
    msgs = [
      ...baseMessages.filter((m) => m.role !== "system"),
      { role: "assistant", content: opts.formatRetry.bad },
      { role: "user", content: window.GeProject?.FORMAT_FIX_PROMPT || "Output NDJSON only." },
    ];
  } else if (round === 0 && !partial) {
    msgs = baseMessages;
  } else {
    msgs = continuationMessages(baseMessages, partial, { doneOnly: opts.doneOnly });
  }

  const systemParts = [];
  const proj = sess?.projectId ? getProject(sess.projectId) : null;
  if (proj?.systemPrompt?.trim()) systemParts.push(proj.systemPrompt.trim());
  if (sess?.systemPrompt?.trim()) systemParts.push(sess.systemPrompt.trim());
  const mem = [proj?.memory, sess?.memory].filter((m) => m?.trim()).join("\n");
  if (mem.trim()) systemParts.push(`Persistent notes for this conversation:\n${mem.trim()}`);
  if (codegen) systemParts.push(window.GeProject?.CODEGEN_SYSTEM || CHAT_CONTINUE_PROMPT);

  if (systemParts.length) {
    const sysContent = systemParts.join("\n\n---\n\n");
    msgs = [{ role: "system", content: sysContent }, ...msgs.filter((m) => m.role !== "system")];
  }

  const usePrefill = false;

  geDebugPush("prepare-messages", {
    codegen,
    round,
    partial: Boolean(partial),
    formatRetry: Boolean(opts.formatRetry),
    prefill: usePrefill,
    systemChars: systemParts.join("").length,
    messageCount: msgs.length,
    reason: codegenDetectReason(baseMessages),
  });

  return { messages: msgs, codegen, prefill: usePrefill };
}

function updateChatModelWarn() {
  const el = $("#chat-model-warn");
  const path = $("#chat-active-model")?.value || "";
  if (!el) return;
  if (!path || isChatReadyPath(path)) {
    el.hidden = true;
    el.textContent = "";
    return;
  }
  el.hidden = false;
  el.textContent =
    "This file is an FP16 shard — chat will cut off or crash. Models tab → Fresh Q4 on Qwen2.5-7B-Instruct, then select the Q4 file.";
}

function isChatReadyPath(path) {
  const n = (path || "").toLowerCase();
  if (/\d{5}-of-\d{5}/.test(n)) return false;
  if ((n.includes("fp16") || n.includes("-f16")) && !n.includes("q4")) return false;
  return true;
}

function syncChatModelSelect() {
  const sel = $("#chat-active-model");
  if (!sel) return;
  const models = status?.models || [];
  const sess = currentSession();
  const prev = sess?.modelPath || sel.value;
  sel.innerHTML = models.length
    ? models
        .map((m) => {
          const ok = isChatReadyPath(m.path);
          const tag = ok ? (m.compressed ? " ✓" : "") : " ⚠ shard";
          return `<option value="${esc(m.path)}" ${ok ? "" : 'title="Not chat-ready — use Fresh Q4"'}>${esc(m.name)}${tag}</option>`;
        })
        .join("")
    : '<option value="">— pick a model —</option>';
  const target = prev || getActiveModel()?.local_path || status?.chat_model;
  if (target && [...sel.options].some((o) => o.value === target)) {
    sel.value = target;
  }
  updateChatModelWarn();
}

async function onComposerModelChange(path) {
  if (!path) return;
  const sess = currentSession();
  if (sess) {
    sess.modelPath = path;
    saveSessions();
  }
  updateChatModelWarn();
  const rt = status?.chat_runtime;
  if (status?.servers?.chat?.up && rt?.model_path && rt.model_path !== path) {
    const pill = $("#chat-server-pill");
    if (pill) {
      pill.textContent = "model changed";
      pill.className = "pill warn";
      pill.title = "Server restarts when you send";
    }
  }
}

function fillChatModelSelect() {
  syncChatModelSelect();
}

// --- chat sessions ---
function loadProjects() {
  try {
    chatProjects = JSON.parse(localStorage.getItem(LS_PROJECTS) || "[]");
  } catch {
    chatProjects = [];
  }
}

function saveProjects() {
  localStorage.setItem(LS_PROJECTS, JSON.stringify(chatProjects.slice(0, 30)));
}

function getProject(id) {
  return chatProjects.find((p) => p.id === id) || null;
}

function newProject() {
  const name = prompt("Project name:", "My project");
  if (!name?.trim()) return;
  const id = "p" + Date.now().toString(36);
  chatProjects.unshift({
    id,
    name: name.trim().slice(0, 48),
    systemPrompt: "",
    memory: "",
    files: {},
    created: Date.now(),
  });
  saveProjects();
  activeProjectId = id;
  renderProjects();
  newChatSession(true, { projectId: id });
}

function selectProject(id) {
  activeProjectId = activeProjectId === id ? null : id;
  renderProjects();
  renderSessions();
}

function renderProjects() {
  const ul = $("#chat-projects");
  if (!ul) return;
  ul.innerHTML = chatProjects.length
    ? chatProjects
        .map(
          (p) =>
            `<li class="${p.id === activeProjectId ? "active" : ""}" data-id="${esc(p.id)}">
              <span class="project-name">${esc(p.name)}</span>
              <span class="hint">${Object.keys(p.files || {}).length}f</span>
            </li>`
        )
        .join("")
    : '<li class="hint">No projects — click +</li>';
  ul.querySelectorAll("li[data-id]").forEach((li) => {
    li.addEventListener("click", (e) => {
      if (e.detail === 2) {
        editProjectSettings(li.dataset.id);
        return;
      }
      selectProject(li.dataset.id);
    });
  });
}

function editProjectSettings(id) {
  const p = getProject(id);
  if (!p) return;
  const sys = prompt("Project system prompt (applies to all chats in project):", p.systemPrompt || "");
  if (sys === null) return;
  p.systemPrompt = sys;
  const mem = prompt("Project memory / notes:", p.memory || "");
  if (mem === null) return;
  p.memory = mem;
  saveProjects();
  renderProjects();
  loadSessionContext();
}

function persistSessionProjectFiles() {
  const sess = currentSession();
  if (!sess || !projectWorkspace) return;
  const files = { ...projectWorkspace.files };
  if (!Object.keys(files).length) return;
  sess.projectFiles = files;
  if (sess.projectId) {
    const p = getProject(sess.projectId);
    if (p) {
      p.files = { ...p.files, ...files };
      saveProjects();
      renderProjects();
    }
  }
  saveSessions();
}

function renderPersistedFilesList() {
  const sess = currentSession();
  const ul = $("#chat-files-list");
  const summary = $("#chat-files-summary");
  if (!ul || !summary) return;
  const files = {
    ...(sess?.projectId ? getProject(sess.projectId)?.files : {}),
    ...(sess?.projectFiles || {}),
    ...(projectWorkspace?.files || {}),
  };
  const paths = Object.keys(files).sort();
  summary.textContent = paths.length
    ? `${paths.length} saved file${paths.length !== 1 ? "s" : ""} (chat + project)`
    : "No saved files yet — generate a multi-file project or edit here.";
  ul.innerHTML = paths.map((p) => `<li>${esc(p)}</li>`).join("");
}

function loadSessionContext() {
  const sess = currentSession();
  if (!sess) return;
  const proj = sess.projectId ? getProject(sess.projectId) : null;
  if ($("#chat-system-prompt")) {
    $("#chat-system-prompt").value = sess.systemPrompt ?? proj?.systemPrompt ?? "";
  }
  if ($("#chat-memory")) {
    $("#chat-memory").value = sess.memory ?? proj?.memory ?? "";
  }
  syncChatModelSelect();
  if (sess.modelPath && $("#chat-active-model")) {
    const sel = $("#chat-active-model");
    if ([...sel.options].some((o) => o.value === sess.modelPath)) sel.value = sess.modelPath;
  }
  if (sess.projectFiles && projectWorkspace && Object.keys(sess.projectFiles).length) {
    projectWorkspace.files = { ...sess.projectFiles };
    if (sess.projectId && getProject(sess.projectId)?.files) {
      projectWorkspace.files = { ...getProject(sess.projectId).files, ...projectWorkspace.files };
    }
    projectWorkspace.render?.();
    if (Object.keys(projectWorkspace.files).length) projectWorkspace.show();
  }
  renderPersistedFilesList();
}

function saveSessionContext() {
  const sess = currentSession();
  if (!sess) return;
  sess.systemPrompt = $("#chat-system-prompt")?.value || "";
  sess.memory = $("#chat-memory")?.value || "";
  sess.modelPath = $("#chat-active-model")?.value || sess.modelPath || null;
  saveSessions();
}

function loadSessions() {
  try {
    chatSessions = JSON.parse(localStorage.getItem(LS_SESSIONS) || "[]");
  } catch {
    chatSessions = [];
  }
  chatSessions.forEach(migrateSessionActivity);
  currentSessionId = localStorage.getItem(LS_CURRENT);
  if (!currentSessionId || !chatSessions.find((s) => s.id === currentSessionId)) {
    newChatSession(false);
  }
}

function saveSessions() {
  localStorage.setItem(LS_SESSIONS, JSON.stringify(chatSessions.slice(0, 40)));
  if (currentSessionId) localStorage.setItem(LS_CURRENT, currentSessionId);
}

function deleteSession(id) {
  const sess = chatSessions.find((s) => s.id === id);
  if (!sess) return;
  if (!confirm(`Delete "${sess.title}"?`)) return;
  chatSessions = chatSessions.filter((s) => s.id !== id);
  if (currentSessionId === id) {
    currentSessionId = chatSessions[0]?.id || null;
    if (!currentSessionId) {
      newChatSession(false);
      return;
    }
  }
  saveSessions();
  renderSessions();
  renderChatMessages();
}

function clearAllSessions() {
  if (!chatSessions.length) return;
  if (!confirm("Delete all chat history?")) return;
  chatSessions = [];
  newChatSession(false);
}

function newChatSession(focus = true, opts = {}) {
  const id = "c" + Date.now().toString(36);
  const projId = opts.projectId ?? activeProjectId ?? null;
  const proj = projId ? getProject(projId) : null;
  chatSessions.unshift({
    id,
    title: "New chat",
    messages: [],
    created: Date.now(),
    projectId: projId,
    modelPath: $("#chat-active-model")?.value || getActiveModel()?.local_path || null,
    systemPrompt: proj?.systemPrompt || "",
    memory: proj?.memory || "",
    projectFiles: proj?.files ? { ...proj.files } : {},
  });
  currentSessionId = id;
  if (projectWorkspace) {
    projectWorkspace.files = proj?.files ? { ...proj.files } : {};
    projectWorkspace.validation = {};
    if (Object.keys(projectWorkspace.files).length) projectWorkspace.show();
    else projectWorkspace.hide();
  }
  resetActivityTracking();
  saveSessions();
  renderSessions();
  renderChatMessages();
  loadSessionContext();
  if (focus) $("#chat-input")?.focus();
}

function currentSession() {
  return chatSessions.find((s) => s.id === currentSessionId);
}

function renderSessions() {
  const visible = activeProjectId
    ? chatSessions.filter((s) => s.projectId === activeProjectId)
    : chatSessions;
  $("#chat-sessions").innerHTML = visible
    .map(
      (s) =>
        `<li class="${s.id === currentSessionId ? "active" : ""}" data-id="${esc(s.id)}">
          <span class="session-title">${esc(s.title)}</span>
          <button type="button" class="btn-del-chat" data-id="${esc(s.id)}" title="Delete chat">×</button>
        </li>`
    )
    .join("");
}

function wireSessionList() {
  const list = $("#chat-sessions");
  if (!list || list.dataset.wired) return;
  list.dataset.wired = "1";
  list.addEventListener("click", (e) => {
    const del = e.target.closest(".btn-del-chat");
    if (del) {
      e.stopPropagation();
      deleteSession(del.dataset.id);
      return;
    }
    const li = e.target.closest("li[data-id]");
    if (!li) return;
    currentSessionId = li.dataset.id;
    saveSessions();
    renderSessions();
    renderChatMessages();
    loadSessionContext();
  });
}

function scrollChatIfPinned() {
  const box = $("#chat-messages");
  if (!box) return;
  const gap = box.scrollHeight - box.scrollTop - box.clientHeight;
  if (gap < 100) box.scrollTop = box.scrollHeight;
}

function stripApiMessages(messages) {
  return messages.map((m) => ({ role: m.role, content: m.content }));
}

function getLastAutoResumeIndex() {
  const sess = currentSession();
  if (!sess) return -1;
  for (let i = sess.messages.length - 1; i >= 0; i--) {
    if (sess.messages[i].role === "assistant" && sess.messages[i].incomplete) return i;
  }
  return -1;
}

function getLastIncompleteIndex() {
  const sess = currentSession();
  if (!sess) return -1;
  for (let i = sess.messages.length - 1; i >= 0; i--) {
    const m = sess.messages[i];
    if (m.role !== "assistant") continue;
    const base = stripApiMessages(sess.messages.slice(0, i));
    if (m.incomplete) return i;
    if (wantsLongCodegen(base) && projectStillIncomplete(m.content, base)) return i;
    break;
  }
  return -1;
}

function updateContinueToolbar() {
  const idx = getLastIncompleteIndex();
  const btn = $("#btn-chat-continue");
  if (btn) {
    btn.hidden = idx < 0 || chatBusy;
    btn.dataset.continueIndex = idx >= 0 ? String(idx) : "";
  }
}

function setChatGeneratingUI(active) {
  const stopBtn = $("#btn-chat-stop-gen");
  const sendBtn = $("#btn-chat-send");
  if (stopBtn) stopBtn.hidden = !active;
  if (sendBtn) {
    sendBtn.hidden = active;
    sendBtn.disabled = active;
  }
  if (!active) stopGenLiveStatus();
  updateContinueToolbar();
}

function assistantBubbleBody(msg, index) {
  const sess = currentSession();
  const userMsgs = sess
    ? sess.messages.slice(0, index).filter((m) => m.role === "user")
    : [];
  if (msg.role === "assistant" && window.GeProject?.wantsCodegenProject(userMsgs)) {
    const files = GeProject.parseFilesFromText(msg.content || "");
    const n = Object.keys(files).length;
    const summary = n
      ? `**Project workspace** — ${n} file${n !== 1 ? "s" : ""}. Use the **Project** panel above to edit · **Check syntax** · **Download ZIP**.`
      : "**Project workspace** — generating files (JSON lines)…";
    return window.GeMarkdown ? GeMarkdown.render(summary) : esc(summary);
  }
  if (msg.role === "assistant" && window.GeMarkdown) return GeMarkdown.render(msg.content);
  return esc(msg.content);
}

function buildMessageBubble(msg, index) {
  const div = document.createElement("div");
  const sess = currentSession();
  const userMsgs = sess
    ? sess.messages.slice(0, index).filter((m) => m.role === "user")
    : [];
  const isCodegen =
    msg.role === "assistant" && window.GeProject?.wantsCodegenProject(userMsgs);
  const hasCode = msg.role === "assistant" && msg.content && msg.content.includes("```");
  div.className = `chat-bubble ${msg.role}${msg.incomplete ? " incomplete" : ""}${hasCode ? " has-code" : ""}${isCodegen ? " project-summary" : ""}`;
  div.dataset.msgIndex = String(index);
  const inner = assistantBubbleBody(msg, index);
  div.innerHTML = `<div class="bubble-body">${inner}</div>`;
  if (msg.role === "assistant" && (msg.activityLog?.length || msg.incomplete)) {
    const wrap = document.createElement("div");
    wrap.innerHTML = msgActivityPanelHtml(msg, index, Boolean(msg.incomplete));
    div.appendChild(wrap.firstElementChild);
  }
  if (msg.role === "assistant" && msg.review) {
    const rw = document.createElement("div");
    rw.innerHTML = msgReviewPanelHtml(msg.review, index);
    if (rw.firstElementChild) div.appendChild(rw.firstElementChild);
  }
  if (msg.incomplete && msg.role === "assistant" && !getAutoContinue()) {
    const parts = msg.continuedParts || 1;
    const bar = document.createElement("div");
    bar.className = "chat-continue-bar";
    bar.innerHTML = `<span class="hint">Stopped here · ${parts} part${parts > 1 ? "s" : ""} saved</span>
      <button type="button" class="btn btn-sm primary" data-continue-msg="${index}">Continue</button>`;
    div.appendChild(bar);
  } else if (msg.incomplete && msg.role === "assistant" && getAutoContinue()) {
    const bar = document.createElement("div");
    bar.className = "chat-continue-bar";
    bar.innerHTML = `<span class="hint">Incomplete — Continue to finish remaining files.</span>
      <button type="button" class="btn btn-sm primary" data-continue-msg="${index}">Continue</button>`;
    div.appendChild(bar);
  }
  attachCopyButtons(div);
  return div;
}

function restoreProjectFromSession() {
  if (!projectWorkspace || chatBusy) return;
  const sess = currentSession();
  if (!sess?.messages?.length) {
    projectWorkspace.hide();
    return;
  }
  migrateSessionActivity(sess);
  const userMsgs = sess.messages.filter((m) => m.role === "user");
  if (!window.GeProject?.wantsCodegenProject(userMsgs)) {
    projectWorkspace.hide();
    return;
  }
  let lastAssistant = "";
  for (let i = sess.messages.length - 1; i >= 0; i--) {
    if (sess.messages[i].role === "assistant") {
      lastAssistant = sess.messages[i].content || "";
      break;
    }
  }
  if (!lastAssistant) return;
  projectWorkspace.ingestText(lastAssistant, false);
}

function renderChatMessages() {
  const box = $("#chat-messages");
  const sess = currentSession();
  if (!sess || !sess.messages.length) {
    box.innerHTML = `<div class="chat-welcome"><h3>Green Chat</h3><p>Ask anything — <strong>markdown</strong>, <code>code</code>, and blocks render below.</p></div>`;
    projectWorkspace?.hide();
    updateContinueToolbar();
    return;
  }
  box.innerHTML = "";
  sess.messages.forEach((msg, i) => box.appendChild(buildMessageBubble(msg, i)));
  box.querySelectorAll(".msg-activity").forEach(wireMsgActivityPanel);
  box.querySelectorAll(".msg-review").forEach(wireMsgReviewPanel);
  scrollChatIfPinned();
  wireContinueButtons(box);
  restoreProjectFromSession();
  updateContinueToolbar();
}

function wireContinueButtons(root) {
  root.querySelectorAll("[data-continue-msg]").forEach((btn) => {
    btn.addEventListener("click", () => {
      continueAssistantMessage(parseInt(btn.dataset.continueMsg, 10)).catch(alertErr);
    });
  });
}

function appendChatBubble(role, content, save = true) {
  const box = $("#chat-messages");
  const welcome = box.querySelector(".chat-welcome");
  if (welcome) welcome.remove();

  const sess = currentSession();
  const index = sess?.messages?.length ?? 0;
  const msg = { role, content };
  const div = buildMessageBubble(msg, index);
  box.appendChild(div);
  box.scrollTop = box.scrollHeight;

  if (save && sess) {
    sess.messages.push(msg);
    if (role === "user" && sess.title === "New chat") {
      sess.title = content.slice(0, 42) + (content.length > 42 ? "…" : "");
    }
    saveSessions();
    renderSessions();
    updateContinueToolbar();
  }
}

function attachCopyButtons(root) {
  root.querySelectorAll(".md-copy").forEach((btn) => {
    const pre = btn.closest(".md-code");
    const code = pre?.querySelector("code")?.textContent || "";
    btn.dataset.code = code;
    btn.onclick = () => {
      navigator.clipboard.writeText(code);
      btn.textContent = "Copied";
      setTimeout(() => { btn.textContent = "Copy"; }, 1500);
    };
  });
}

function beginAssistantBubble(msgIndex) {
  const box = $("#chat-messages");
  const welcome = box.querySelector(".chat-welcome");
  if (welcome) welcome.remove();
  const div = document.createElement("div");
  div.className = "chat-bubble assistant streaming";
  div.dataset.msgIndex = String(msgIndex);
  div.innerHTML =
    '<div class="bubble-body" data-char-count="0"><span class="gen-placeholder">Waiting for model…</span></div>';
  div.insertAdjacentHTML(
    "beforeend",
    msgActivityPanelHtml({ activityLog: [], activityStatus: "Thinking…" }, msgIndex, true)
  );
  wireMsgActivityPanel(div.querySelector(".msg-activity"));
  box.appendChild(div);
  scrollChatIfPinned();
  return div;
}

function setAssistantBubble(div, content, streaming, baseMessages) {
  const body = div.querySelector(".bubble-body");
  if (body) body.dataset.charCount = String((content || "").length);
  const projectMode = baseMessages && wantsLongCodegen(baseMessages);
  const render = () => {
    if (!content && streaming) {
      body.innerHTML = '<span class="gen-placeholder">Waiting for model output…</span>';
      div.classList.toggle("streaming", true);
      scrollChatIfPinned();
      return;
    }
    if (projectMode && projectWorkspace) {
      syncProjectFromGeneration(content, baseMessages);
      const summary = projectWorkspace.summaryHtml(content);
      body.innerHTML = summary ? GeMarkdown.render(summary) : esc("Building project…");
      div.classList.add("project-summary");
    } else if (window.GeMarkdown) {
      body.innerHTML = GeMarkdown.render(content);
      attachCopyButtons(div);
      div.classList.remove("project-summary");
    } else {
      body.textContent = content;
    }
    scrollChatIfPinned();
  };
  if (streaming) {
    clearTimeout(bubbleRenderTimer);
    if (!content) render();
    else bubbleRenderTimer = setTimeout(render, 80);
  } else {
    clearTimeout(bubbleRenderTimer);
    render();
  }
  div.classList.toggle("streaming", Boolean(streaming));
}

function finalizeAssistantBubble(div, content, meta = {}, baseMessages = null) {
  setAssistantBubble(div, content, false, baseMessages);
  const sess = currentSession();
  if (sess) {
    sess.messages.push({
      role: "assistant",
      content,
      incomplete: Boolean(meta.incomplete),
      continuedParts: meta.continuedParts || 1,
    });
    saveSessions();
    div.classList.toggle("incomplete", Boolean(meta.incomplete));
    if (meta.incomplete && !getAutoContinue()) {
      const idx = sess.messages.length - 1;
      const bar = document.createElement("div");
      bar.className = "chat-continue-bar";
      const parts = meta.continuedParts || 1;
      bar.innerHTML = `<span class="hint">Stopped here · ${parts} part${parts > 1 ? "s" : ""} saved</span>
        <button type="button" class="btn btn-sm primary" data-continue-msg="${idx}">Continue</button>`;
      div.appendChild(bar);
      wireContinueButtons(div);
    } else if (meta.incomplete && getAutoContinue()) {
      const idx = sess.messages.length - 1;
      const bar = document.createElement("div");
      bar.className = "chat-continue-bar";
      bar.innerHTML = `<span class="hint">Hit continuation limit — click Continue to finish.</span>
        <button type="button" class="btn btn-sm primary" data-continue-msg="${idx}">Continue</button>`;
      div.appendChild(bar);
      wireContinueButtons(div);
    }
    updateContinueToolbar();
  }
}

function updateAssistantMessageAt(index, content, meta = {}) {
  const sess = currentSession();
  if (!sess?.messages[index]) return;
  const msg = sess.messages[index];
  msg.content = content;
  msg.incomplete = Boolean(meta.incomplete);
  msg.continuedParts = meta.continuedParts ?? msg.continuedParts ?? 1;
  saveSessions();
  const box = $("#chat-messages");
  const old = box.querySelector(`[data-msg-index="${index}"]`);
  if (old) {
    const next = buildMessageBubble(msg, index);
    old.replaceWith(next);
    wireContinueButtons(box);
  }
  updateContinueToolbar();
}

function checkpointAssistantMessage(index, content, continuedParts) {
  const sess = currentSession();
  if (!sess?.messages[index]) return;
  sess.messages[index].content = content;
  sess.messages[index].incomplete = true;
  sess.messages[index].continuedParts = continuedParts;
  saveSessions();
}

async function streamChatCompletion(messages, onDelta, signal, onActivity, opts = {}) {
  const codegen = Boolean(opts.codegen);
  const prefillStallMs = codegen ? 300000 : 180000;
  const streamStallMs = codegen ? 300000 : 120000;
  const res = await fetch("/api/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      messages,
      max_tokens: getMaxTokens(),
      stream: true,
      codegen,
      temperature: codegen ? 0.12 : 0.7,
      debug_tag: codegen ? "codegen-stream" : "chat-stream",
    }),
    signal,
  });
  const ctype = res.headers.get("content-type") || "";
  if (!res.ok || !ctype.includes("text/event-stream")) {
    let err = `HTTP ${res.status}`;
    try {
      const data = await res.json();
      if (data.error) err = data.error;
    } catch (_) {}
    throw new Error(err);
  }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  let finishReason = null;
  let lastDeltaAt = Date.now();
  let gotContent = false;
  geDebugPush("stream-open", { codegen, prefillStallMs, streamStallMs, messages: messages.length });

  while (true) {
    const limit = gotContent ? streamStallMs : prefillStallMs;
    const waitMs = limit - (Date.now() - lastDeltaAt);
    if (waitMs <= 0) {
      reader.cancel().catch(() => {});
      throw new Error(`Stream stalled (no data for ${Math.round(limit / 1000)}s)`);
    }
    const timeout = new Promise((_, reject) =>
      setTimeout(() => reject(new Error(`Stream stalled (no data for ${Math.round(limit / 1000)}s)`)), waitMs)
    );
    let chunk;
    try {
      chunk = await Promise.race([reader.read(), timeout]);
    } catch (e) {
      reader.cancel().catch(() => {});
      throw e;
    }
    const { done, value } = chunk;
    if (done) break;
    lastDeltaAt = Date.now();
    onActivity?.();
    buf += dec.decode(value, { stream: true });
    const lines = buf.split("\n");
    buf = lines.pop() || "";
    for (const line of lines) {
      if (!line.startsWith("data:")) continue;
      const payload = line.slice(5).trim();
      if (!payload || payload === "[DONE]") continue;
      try {
        const j = JSON.parse(payload);
        const deltaObj = j.choices?.[0]?.delta || {};
        const delta = deltaObj.content || "";
        if (delta) {
          gotContent = true;
          onDelta(delta);
        }
        const reason = j.choices?.[0]?.finish_reason;
        if (reason) finishReason = reason;
      } catch (_) {}
    }
  }
  return { finish_reason: finishReason };
}

async function chatCompletionOnce(messages, opts = {}) {
  const codegen = Boolean(opts.codegen);
  const data = await api("/api/chat", {
    method: "POST",
    body: JSON.stringify({
      messages,
      max_tokens: getMaxTokens(),
      codegen,
      temperature: codegen ? 0.12 : 0.7,
      debug_tag: codegen ? "codegen-once" : "chat-once",
    }),
  });
  if (data.error && !data.choices) throw new Error(data.error);
  const choice = data.choices?.[0];
  return {
    content: choice?.message?.content || "",
    finish_reason: choice?.finish_reason || null,
    usage: data.usage,
  };
}

async function completeChatWithContinuation(baseMessages, bubble, onMeta, opts = {}) {
  let full = opts.initialContent || "";
  let rounds = 0;
  const priorParts = opts.priorParts || 0;
  let lastUsage = null;
  let useStream = true;
  const isCodegen = wantsLongCodegen(baseMessages);
  const autoContinue = isCodegen ? true : opts.autoContinue !== false && getAutoContinue();
  const signal = opts.signal;
  let lastCheckpoint = 0;
  const detectReason = codegenDetectReason(baseMessages);
  const maxRounds = getMaxContinueRounds(isCodegen);
  let formatRetries = 0;
  let pendingFormatRetry = null;
  let lastFileCount = 0;
  let stagnantRounds = 0;
  if (opts.messageIndex != null && !opts.skipActivityStart) {
    if (!opts.initialContent) {
      GenActivity.onStart(isCodegen, opts.messageIndex, detectReason);
      geDebugPush("generation-start", { isCodegen, reason: detectReason, user: baseMessages.filter((m) => m.role === "user").pop()?.content?.slice(0, 120) });
    } else {
      attachActivityToMessage(opts.messageIndex, true);
      GenActivity.log("info", `Resuming part ${(opts.priorParts || 0) + 1} (${opts.initialContent.length} chars saved)`);
      GenActivity.setStatus("Continuing…");
      startGenLiveStatus(isCodegen, (opts.priorParts || 0) + 1);
    }
  }

  while (rounds < maxRounds) {
    if (signal?.aborted) {
      syncProjectFromGeneration(full, baseMessages);
      GenActivity.log("warn", "Stopped by user");
      GenActivity.onDone(true, Object.keys(projectWorkspace?.files || {}).length);
      return { content: full, rounds: priorParts + rounds, usage: lastUsage, truncated: true, aborted: true };
    }
    const prepOpts = pendingFormatRetry
      ? { formatRetry: pendingFormatRetry }
      : stagnantRounds >= 3
        ? { doneOnly: true }
        : {};
    pendingFormatRetry = null;
    const prepared = prepareApiMessages(baseMessages, rounds, full, prepOpts);
    const apiMessages = prepared.messages;
    const roundStart = full.length;
    if (onMeta) onMeta(rounds, full.length);
    if (isCodegen) GenActivity.onRound(rounds + 1, full.length);

    let finishReason = null;
    let gotDeltaThisRound = false;
    let lastOutputAt = Date.now();
    let lastSseAt = Date.now();
    let prefillSlowLogged = false;
    let pauseWarned = false;
    const streamAbort = new AbortController();
    const streamSignals = [streamAbort.signal];
    if (signal) streamSignals.push(signal);
    const fetchSignal =
      typeof AbortSignal !== "undefined" && AbortSignal.any
        ? AbortSignal.any(streamSignals)
        : streamAbort.signal;

    const stallWatch = setInterval(() => {
      const idleSse = Date.now() - lastSseAt;
      const idleOut = Date.now() - lastOutputAt;
      const fileCount = Object.keys(projectWorkspace?.files || {}).length;
      const prefillLimit = isCodegen ? 300000 : 180000;
      const streamPauseWarn = isCodegen ? 180000 : 90000;
      const streamPauseAbort = isCodegen ? 300000 : 120000;
      const midFileAbort = isCodegen ? 420000 : 240000;
      if (!gotDeltaThisRound && idleSse >= 30000 && !prefillSlowLogged) {
        prefillSlowLogged = true;
        GenActivity.log("warn", `Prefill ${Math.round(idleSse / 1000)}s (${gpuStatusLabel()}) — large prompt or first token`);
      }
      if (!gotDeltaThisRound && idleSse >= prefillLimit) {
        GenActivity.log("err", `No response in ${Math.round(prefillLimit / 1000)}s — retrying (${gpuStatusLabel()})`);
        streamAbort.abort();
      }
      if (gotDeltaThisRound && idleOut >= streamPauseWarn && idleOut < midFileAbort && !pauseWarned) {
        pauseWarned = true;
        const parsed = Object.keys(GeProject?.parseFilesFromText(full, true) || {}).length;
        GenActivity.log("warn", `Slow tokens ${Math.round(idleOut / 1000)}s · ${full.length} chars · ${parsed} files (large JSON lines are normal)`);
      }
      if (gotDeltaThisRound && idleOut >= midFileAbort && GeProject?.hasOpenGenerationTags?.(full)) {
        GenActivity.log("err", `Stalled ${Math.round(midFileAbort / 1000)}s mid-file — retrying next part`);
        streamAbort.abort();
      } else if (gotDeltaThisRound && idleOut >= streamPauseAbort && !GeProject?.hasOpenGenerationTags?.(full)) {
        GenActivity.log("err", `No output for ${Math.round(streamPauseAbort / 1000)}s — retrying`);
        streamAbort.abort();
      }
    }, 5000);

    if (useStream && rounds > 0 && isCodegen) {
      useStream = false;
    }

    if (useStream) {
      try {
        const streamResult = await streamChatCompletion(
          apiMessages,
          (delta) => {
            full += delta;
            gotDeltaThisRound = true;
            lastOutputAt = Date.now();
            pauseWarned = false;
            setAssistantBubble(bubble, full, true, baseMessages);
            syncProjectFromGeneration(full, baseMessages, false);
            updateGenLiveStatus(full.length, Object.keys(projectWorkspace?.files || {}).length, rounds + 1);
            if (
              isCodegen &&
              formatRetries < 2 &&
              full.length >= 200 &&
              full.length - roundStart >= 120
            ) {
              const parsed = Object.keys(GeProject?.parseFilesFromText(full) || {}).length;
              if (parsed === 0) {
                const a = GeProject.analyzeGenerationOutput(full);
                if (a.phase === "prose" || a.phase === "markdown") {
                  geDebugPush("format-abort-early", { phase: a.phase, preview: a.preview, len: full.length });
                  GenActivity.log("warn", `Prose/markdown detected — aborting stream to retry NDJSON`);
                  streamAbort.abort();
                }
              }
            }
            if (opts.messageIndex != null && full.length - lastCheckpoint > 400) {
              checkpointAssistantMessage(opts.messageIndex, full, priorParts + rounds + 1);
              lastCheckpoint = full.length;
            }
          },
          fetchSignal,
          () => {
            lastSseAt = Date.now();
          },
          { codegen: isCodegen }
        );
        finishReason = streamResult.finish_reason;
      } catch (e) {
        if (signal?.aborted && !streamAbort.signal.aborted) {
          syncProjectFromGeneration(full, baseMessages);
          GenActivity.log("warn", "Stopped by user");
          GenActivity.onDone(true, Object.keys(projectWorkspace?.files || {}).length);
          clearInterval(stallWatch);
          return { content: full, rounds: priorParts + rounds, usage: lastUsage, truncated: true, aborted: true };
        }
        const errMsg = e?.message || String(e);
        const timedOut = streamAbort.signal.aborted;
        if (timedOut || full.length > roundStart) {
          const stallSec = isCodegen ? 300 : 120;
          GenActivity.log(
            "warn",
            `${timedOut ? `Stream paused >${stallSec}s` : "Stream lost"}: ${errMsg} — keeping ${full.length} chars${isCodegen ? " (codegen uses 5min timeout)" : ""}`
          );
          scheduleActivityPersist();
          finishReason = "length";
        } else {
          GenActivity.log("err", `Request failed: ${errMsg}`);
          useStream = false;
          try {
            GenActivity.log("info", "Trying non-stream request…");
            const once = await chatCompletionOnce(apiMessages, { codegen: isCodegen });
            full = GeProject.appendGenerationChunk(full, once.content);
            finishReason = once.finish_reason;
            lastUsage = once.usage;
            setAssistantBubble(bubble, full, true, baseMessages);
            GenActivity.log("info", `Non-stream ok (${full.length} chars, ${Object.keys(projectWorkspace?.files || {}).length} files)`);
          } catch (e2) {
            GenActivity.log("err", `Non-stream failed: ${e2.message}`);
            clearInterval(stallWatch);
            throw e2;
          }
        }
      } finally {
        clearInterval(stallWatch);
      }
    } else {
      clearInterval(stallWatch);
      const once = await chatCompletionOnce(apiMessages, { codegen: isCodegen });
      full = GeProject.appendGenerationChunk(full, once.content);
      finishReason = once.finish_reason;
      lastUsage = once.usage;
      setAssistantBubble(bubble, full, true, baseMessages);
    }

    syncProjectFromGeneration(full, baseMessages);
    const fileCount = Object.keys(projectWorkspace?.files || {}).length;
    if (fileCount === lastFileCount) stagnantRounds += 1;
    else {
      stagnantRounds = 0;
      lastFileCount = fileCount;
    }
    const analysis = window.GeProject?.analyzeGenerationOutput?.(full);
    GenActivity.log(
      finishReason === "length" ? "warn" : "info",
      `Part ${rounds + 1} finished (${finishReason || "done"}): ${full.length} chars, ${fileCount} files${analysis ? " — " + analysis.hint : ""}`
    );

    if (
      isCodegen &&
      stagnantRounds >= 5 &&
      finishReason === "stop" &&
      !GeProject?.jsonGenerationDone?.(full) &&
      fileCount > 0
    ) {
      full = `${full.replace(/\s+$/, "")}\n{"done":true}`;
      GenActivity.log("warn", "No new files in 5 rounds — marking complete (Continue to add more)");
      geDebugPush("auto-done", { fileCount, stagnantRounds });
    }

    if (isCodegen && formatRetries < 2 && fileCount === 0 && full.length > 60) {
      const phase = analysis?.phase;
      const badFormat = phase === "prose" || phase === "markdown" || !GeProject?.usesJsonFormat?.(full);
      if (badFormat) {
        formatRetries += 1;
        geDebugPush("format-retry", { phase, preview: analysis.preview, attempt: formatRetries, chars: full.length });
        GenActivity.log("warn", `Format retry ${formatRetries}/2 — re-prompting for NDJSON (was ${phase})`);
        pendingFormatRetry = { bad: full.slice(0, 1500) };
        full = "";
        setAssistantBubble(bubble, "…retrying NDJSON…", true, baseMessages);
        continue;
      }
    }

    rounds += 1;

    if (isCodegen && !signal?.aborted && rounds < maxRounds) {
      const stillNeed = projectStillIncomplete(full, baseMessages);
      if (stillNeed) {
        GenActivity.log(
          "info",
          `Auto-continue → part ${rounds + 1} (${fileCount} file${fileCount !== 1 ? "s" : ""}, ${full.length} chars)`
        );
        geDebugPush("auto-continue", { part: rounds + 1, fileCount, chars: full.length });
        if (opts.messageIndex != null) {
          checkpointAssistantMessage(opts.messageIndex, full, priorParts + rounds);
          lastCheckpoint = full.length;
        }
        continue;
      }
      geDebugPush("continue-stop", {
        reason: "project complete",
        files: fileCount,
        jsonDone: GeProject?.jsonGenerationDone?.(full),
      });
    }

    const truncated =
      finishReason === "length" ||
      looksTruncated(full, finishReason, baseMessages) ||
      (isCodegen && projectStillIncomplete(full, baseMessages));

    if (!truncated) {
      if (projectWorkspace && isCodegen) {
        const vdata = await projectWorkspace.validateAll();
        GenActivity.onValidationResults(vdata.results);
      }
      GenActivity.onDone(false, fileCount);
      return { content: full, rounds: priorParts + rounds, usage: lastUsage, truncated: false, finishReason };
    }
    if (!autoContinue) {
      GenActivity.onDone(true, fileCount);
      return {
        content: full,
        rounds: priorParts + rounds,
        usage: lastUsage,
        truncated: true,
        finishReason: finishReason || "length",
      };
    }
    if (opts.messageIndex != null) {
      checkpointAssistantMessage(opts.messageIndex, full, priorParts + rounds);
      lastCheckpoint = full.length;
    }
  }

  syncProjectFromGeneration(full, baseMessages);
  if (isCodegen && projectStillIncomplete(full, baseMessages)) {
    GenActivity.log(
      "warn",
      `Reached max continue rounds (${maxRounds}) — project still incomplete. Increase Settings → Max continue rounds or click Continue.`
    );
  }
  GenActivity.onDone(true, Object.keys(projectWorkspace?.files || {}).length);
  return {
    content: full,
    rounds: priorParts + rounds,
    usage: lastUsage,
    truncated: true,
    finishReason: "length",
  };
}

async function ensureChatServer() {
  const sess = currentSession();
  const path =
    sess?.modelPath || $("#chat-active-model")?.value || getActiveModel()?.local_path;
  if (!path) throw new Error("Pick a model in the top bar, then send");
  if ($("#chat-active-model")) $("#chat-active-model").value = path;
  if (sess && !sess.modelPath) {
    sess.modelPath = path;
    saveSessions();
  }
  const disk = status?.models?.find((m) => m.path === path);
  const size = disk?.size / (1024 ** 3) || 4.5;
  const wantGpu = resolveGpuLayers(size);
  const rt = status?.chat_runtime;
  const gotGpu = rt?.gpu_layers || 0;
  const sameModel = status?.servers?.chat?.up && rt?.model_path === path;
  if (sameModel && gpuLayersMatch(wantGpu, gotGpu)) return;
  if (sameModel && wantGpu > 0 && gotGpu === 0) {
    GenActivity.log("warn", "Server on CPU — restarting with GPU…");
  } else if (sameModel && wantGpu > 0 && gotGpu > wantGpu + 14) {
    GenActivity.log("info", `Reducing GPU offload ${gotGpu}→${wantGpu} layers (shared mode)…`);
  }
  await startChatServer(path);
}

async function runSelfReview(baseMessages, files) {
  if (!window.GeProject || !getSelfReview()) return null;
  const userRequest = baseMessages.filter((m) => m.role === "user").map((m) => m.content).join("\n");
  const prompt = GeProject.buildReviewPrompt(userRequest, files);
  const messages = [
    { role: "system", content: GeProject.REVIEW_SYSTEM },
    { role: "user", content: prompt },
  ];
  const data = await api("/api/chat", {
    method: "POST",
    body: JSON.stringify({ messages, max_tokens: Math.min(getMaxTokens(), 4096), stream: false }),
  });
  if (data.error && !data.choices) throw new Error(data.error);
  return data.choices?.[0]?.message?.content || "";
}

function attachReviewToMessage(index, review) {
  const sess = currentSession();
  if (!sess?.messages[index] || !review?.trim()) return;
  sess.messages[index].review = review.trim();
  saveSessions();
  const bubble = document.querySelector(`#chat-messages .chat-bubble[data-msg-index="${index}"]`);
  if (!bubble || bubble.querySelector(".msg-review")) return;
  bubble.insertAdjacentHTML("beforeend", msgReviewPanelHtml(review, index));
  wireMsgReviewPanel(bubble.querySelector(".msg-review"));
}

function formatChatMeta(result, ms) {
  const tok = result.usage?.completion_tokens;
  const rt = status?.chat_runtime;
  const run = rt?.loaded ? `${rt.model_name} ctx${rt.ctx}` : getComputeMode().toUpperCase();
  const parts = result.rounds > 1 ? ` · ${result.rounds} parts` : "";
  const stopped = result.aborted ? " · stopped" : result.truncated ? " · saved (continue)" : "";
  return `${run} · ${tok || "~" + Math.round(result.content.length / 4)} tok · max ${getMaxTokens()}${parts}${stopped} · ${ms} ms`;
}

async function runAssistantGeneration({
  baseMessages,
  bubble,
  messageIndex = null,
  initialContent = "",
  priorParts = 0,
}) {
  const t0 = performance.now();
  if (!status?.servers?.chat?.up) {
    GenActivity.log("info", "Chat server offline — starting model…");
  }
  try {
    await ensureChatServer();
  } catch (e) {
    GenActivity.log("err", `Server start failed: ${e.message}`);
    throw e;
  }
  if (status?.servers?.chat?.up) {
    const rt = status?.chat_runtime;
    const gpu = rt?.gpu_layers > 0 ? `GPU ${rt.gpu_layers} layers` : "CPU";
    GenActivity.log("info", rt?.loaded ? `Ready: ${rt.model_name} · ${gpu}` : `Server up · ${gpu}`);
  }

  const result = await completeChatWithContinuation(baseMessages, bubble, null, {
    initialContent,
    priorParts,
    autoContinue: wantsLongCodegen(baseMessages) ? true : getAutoContinue(),
    signal: chatAbortController?.signal,
    messageIndex,
  });

  let chain = 0;
  let final = result;
  while (
    chain < 256 &&
    !final.aborted &&
    getAutoContinue() &&
    wantsLongCodegen(baseMessages) &&
    projectStillIncomplete(final.content, baseMessages)
  ) {
    chain += 1;
    GenActivity.log("info", `Auto-chain continue ${chain}…`);
    final = await completeChatWithContinuation(baseMessages, bubble, null, {
      initialContent: final.content,
      priorParts: final.rounds,
      autoContinue: getAutoContinue(),
      signal: chatAbortController?.signal,
      messageIndex,
      skipActivityStart: true,
    });
  }

  const meta = {
    incomplete: projectStillIncomplete(final.content, baseMessages),
    continuedParts: final.rounds,
  };

  if (messageIndex != null) {
    updateAssistantMessageAt(messageIndex, final.content, meta);
    setAssistantBubble(bubble, final.content, false, baseMessages);
    if (meta.incomplete && !getAutoContinue()) {
      const bar = document.createElement("div");
      bar.className = "chat-continue-bar";
      const parts = meta.continuedParts || 1;
      bar.innerHTML = `<span class="hint">Stopped here · ${parts} part${parts > 1 ? "s" : ""} saved</span>
        <button type="button" class="btn btn-sm primary" data-continue-msg="${messageIndex}">Continue</button>`;
      bubble.appendChild(bar);
      wireContinueButtons(bubble);
    } else if (meta.incomplete && getAutoContinue()) {
      const note = document.createElement("div");
      note.className = "chat-continue-bar";
      note.innerHTML = `<span class="hint">Hit continuation limit — click Continue or send a follow-up.</span>
        <button type="button" class="btn btn-sm primary" data-continue-msg="${messageIndex}">Continue</button>`;
      bubble.appendChild(note);
      wireContinueButtons(bubble);
    }
  } else {
    finalizeAssistantBubble(bubble, final.content, meta, baseMessages);
  }

  const ms = Math.round(performance.now() - t0);
  if (!final.truncated && !final.aborted) {
    GenActivity.log("info", formatChatMeta(final, ms));
    const fileCount = Object.keys(projectWorkspace?.files || {}).length;
    if (
      messageIndex != null &&
      wantsLongCodegen(baseMessages) &&
      fileCount > 0 &&
      getSelfReview()
    ) {
      try {
        GenActivity.log("info", "Self-review: checking completeness, security, performance…");
        const review = await runSelfReview(baseMessages, projectWorkspace.files);
        attachReviewToMessage(messageIndex, review);
        GenActivity.log("ok", "Self-review complete — expand Review below");
      } catch (e) {
        GenActivity.log("err", `Self-review failed: ${e.message}`);
      }
    }
  }
  await refreshStatus();
  return final;
}

async function continueAssistantMessage(msgIndex) {
  if (chatBusy) return;
  const sess = currentSession();
  const msg = sess?.messages[msgIndex];
  if (!msg || msg.role !== "assistant") return;

  const baseMessages = stripApiMessages(sess.messages.slice(0, msgIndex));
  const box = $("#chat-messages");
  let bubble = box.querySelector(`[data-msg-index="${msgIndex}"]`);
  if (!bubble) {
    renderChatMessages();
    bubble = box.querySelector(`[data-msg-index="${msgIndex}"]`);
  }
  if (!bubble) return;

  chatBusy = true;
  window.__geChatBusy = true;
  chatAbortController = new AbortController();
  setChatGeneratingUI(true);
  bubble.classList.add("streaming");
  const bar = bubble.querySelector(".chat-continue-bar");
  if (bar) bar.remove();

  try {
    await runAssistantGeneration({
      baseMessages,
      bubble,
      messageIndex: msgIndex,
      initialContent: msg.content,
      priorParts: msg.continuedParts || 0,
    });
  } catch (e) {
    if (chatAbortController?.signal.aborted && msg.content) {
      updateAssistantMessageAt(msgIndex, bubble.querySelector(".bubble-body")?.textContent || msg.content, {
        incomplete: true,
        continuedParts: msg.continuedParts || 1,
      });
    } else {
      appendChatBubble("assistant", `**Error:** ${e.message}`);
    }
  } finally {
    window.__geChatBusy = false;
    chatAbortController = null;
    bubble.classList.remove("streaming");
    projectWorkspace?.scheduleAutoValidate?.();

    const sess = currentSession();
    const msg = sess?.messages[msgIndex];
    const base = stripApiMessages(sess?.messages.slice(0, msgIndex) || []);
    const content = msg?.content || bubble.querySelector(".bubble-body")?.textContent || "";
    const needMore =
      msg && wantsLongCodegen(base) && projectStillIncomplete(content, base) && getAutoContinue();

    if (needMore) {
      GenActivity.log("info", "Scheduling next file…");
      setChatGeneratingUI(true);
      continueAssistantMessage(msgIndex)
        .catch((e) => GenActivity.log("err", `Continue failed: ${e.message}`))
        .finally(() => {
          chatBusy = false;
          setChatGeneratingUI(false);
        });
    } else {
      chatBusy = false;
      setChatGeneratingUI(false);
    }
  }
}

async function tryResumeGeneration() {
  if (chatBusy || !getAutoContinue()) return;
  const idx = getLastAutoResumeIndex();
  if (idx < 0) return;

  attachActivityToMessage(idx, true);
  if (!GenActivity.entries.some((e) => e.msg.includes("Resuming"))) {
    GenActivity.log("info", "Resuming generation in background…");
  }
  scheduleActivityPersist();

  await refreshStatus();
  if (!status?.servers?.chat?.up) {
    const path = $("#chat-active-model")?.value || getActiveModel()?.local_path;
    if (path) {
      try {
        await startChatServer(path);
      } catch (_) {}
    }
  }

  const sess = currentSession();
  const msg = sess?.messages[idx];
  if (msg && !msg.incomplete) {
    msg.incomplete = true;
    saveSessions();
  }

  continueAssistantMessage(idx).catch((e) => {
    GenActivity.log("err", `Resume failed: ${e.message}`);
    scheduleActivityPersist();
  });
}

async function sendChat() {
  if (chatBusy) return;
  const input = $("#chat-input");
  const text = input.value.trim();
  if (!text) return;

  saveSessionContext();

  const modelPath = $("#chat-active-model")?.value || "";
  if (!modelPath) {
    alert("Pick a model in the top bar first.");
    return;
  }
  if (modelPath && !isChatReadyPath(modelPath)) {
    alert(
      "This model file is an FP16 shard and cannot finish long replies.\n\nGo to Models → Qwen2.5-7B-Instruct → Fresh Q4, then select the Q4_K_M file."
    );
    return;
  }

  const resumeIdx = getLastIncompleteIndex();
  if (/^continue\.?$/i.test(text) && resumeIdx >= 0) {
    input.value = "";
    return continueAssistantMessage(resumeIdx);
  }

  input.value = "";
  appendChatBubble("user", text);
  chatBusy = true;
  window.__geChatBusy = true;
  chatAbortController = new AbortController();
  setChatGeneratingUI(true);

  const sess = currentSession();
  const messages = stripApiMessages(sess?.messages || []);
  if (wantsLongCodegen(messages) && projectWorkspace) {
    projectWorkspace.files = {};
    projectWorkspace.validation = {};
    projectWorkspace.hide();
  }

  sess.messages.push({
    role: "assistant",
    content: "",
    incomplete: true,
    continuedParts: 0,
    activityLog: [],
    activityStatus: "Thinking…",
  });
  const draftIndex = sess.messages.length - 1;
  saveSessions();
  const bubble = beginAssistantBubble(draftIndex);

  try {
    const result = await runAssistantGeneration({
      baseMessages: messages,
      bubble,
      messageIndex: draftIndex,
      initialContent: "",
      priorParts: 0,
    });
    if (result.aborted && result.content) {
      GenActivity.log("warn", "Stopped — progress saved");
    }
  } catch (e) {
    const partial = bubble.querySelector(".bubble-body")?.textContent || "";
    if (partial && chatAbortController?.signal.aborted) {
      updateAssistantMessageAt(draftIndex, partial, { incomplete: true, continuedParts: 1 });
    } else {
      sess.messages.pop();
      saveSessions();
      bubble.remove();
      appendChatBubble("assistant", `**Error:** ${e.message}`);
    }
  } finally {
    window.__geChatBusy = false;
    chatAbortController = null;
    bubble.classList.remove("streaming");
    projectWorkspace?.scheduleAutoValidate?.();

    const msg = sess?.messages[draftIndex];
    const base = stripApiMessages(sess?.messages.slice(0, draftIndex) || []);
    const content = msg?.content || bubble.querySelector(".bubble-body")?.textContent || "";
    const needMore =
      msg && wantsLongCodegen(base) && projectStillIncomplete(content, base) && getAutoContinue();

    if (needMore) {
      GenActivity.log("info", "Scheduling next file…");
      setChatGeneratingUI(true);
      continueAssistantMessage(draftIndex)
        .catch((e) => GenActivity.log("err", `Continue failed: ${e.message}`))
        .finally(() => {
          chatBusy = false;
          setChatGeneratingUI(false);
        });
    } else {
      chatBusy = false;
      setChatGeneratingUI(false);
    }
  }
}

$("#btn-new-chat").addEventListener("click", () => newChatSession());
$("#btn-clear-chats")?.addEventListener("click", clearAllSessions);
$("#btn-chat-send").addEventListener("click", sendChat);
$("#btn-chat-continue")?.addEventListener("click", () => {
  const idx = parseInt($("#btn-chat-continue")?.dataset.continueIndex || "-1", 10);
  if (idx >= 0) continueAssistantMessage(idx).catch(alertErr);
});
$("#btn-chat-stop-gen")?.addEventListener("click", () => {
  chatAbortController?.abort();
});
$("#chat-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    sendChat();
  }
});
$("#btn-start-chat-server").addEventListener("click", () => {
  const path = $("#chat-active-model").value;
  if (!path) return alert("Pick a model from the dropdown.");
  startChatServer(path).catch(() => {});
});
$("#btn-stop-chat-server").addEventListener("click", () => {
  serverCmd("chat", "stop").then(refreshStatus).catch(alertErr);
});
$("#chat-active-model").addEventListener("change", (e) => {
  const path = e.target.value;
  updateChatModelWarn();
  if (path) onComposerModelChange(path);
});

function toggleContextPanel(id) {
  const panel = $(id);
  if (!panel) return;
  const other = id === "#chat-context-panel" ? "#chat-files-panel" : "#chat-context-panel";
  $(other)?.classList.add("hidden");
  panel.classList.toggle("hidden");
}

$("#btn-chat-context")?.addEventListener("click", () => toggleContextPanel("#chat-context-panel"));
$("#btn-chat-files")?.addEventListener("click", () => {
  renderPersistedFilesList();
  toggleContextPanel("#chat-files-panel");
});
$("#chat-system-prompt")?.addEventListener("change", saveSessionContext);
$("#chat-memory")?.addEventListener("change", saveSessionContext);
$("#btn-new-project")?.addEventListener("click", newProject);
$("#btn-servers-start-chat").addEventListener("click", () => {
  const path = $("#chat-active-model")?.value || getActiveModel()?.local_path;
  startChatServer(path).catch(() => {});
});

// --- misc actions ---
const ACTIONS = {
  stack_setup: () => startJob({ action: "stack_setup" }),
  install: () => startJob({ action: "install" }),
  chat_install: () => startJob({ action: "chat_install" }),
  start_mcp: () => serverCmd("mcp_stack", "start").then(refreshStatus),
  stop_mcp: () => serverCmd("mcp_stack", "stop").then(refreshStatus),
};

$$("[data-action]").forEach((el) => {
  el.addEventListener("click", () => ACTIONS[el.dataset.action]?.().catch(alertErr));
});

$("#btn-bench").addEventListener("click", () => startJob({ action: "bench", name: $("#bench-name").value }).catch(alertErr));

$$(".btn-server-stop").forEach((btn) => {
  btn.addEventListener("click", () => serverCmd(btn.dataset.service, "stop").then(refreshStatus).catch(alertErr));
});

$$(".btn-server-start").forEach((btn) => {
  btn.addEventListener("click", () => serverCmd(btn.dataset.service, "start", { mcp: true }).then(refreshStatus).catch(alertErr));
});

$("#btn-copy-preset").addEventListener("click", () => {
  navigator.clipboard.writeText(JSON.stringify({ base_url: "http://127.0.0.1:8767/v1", api_key: "local", model: "green-local" }, null, 2));
});

function renderExternalCards() {
  $("#external-cards").innerHTML = EXTERNAL_UIS.map(
    (u) => `<div class="card"><h3>${esc(u.name)}</h3><p class="hint">${esc(u.blurb)}</p><a class="btn link" href="${esc(u.url)}" target="_blank">Open ↗</a></div>`
  ).join("");
}

async function pollJobs() {
  try {
    const data = await api("/api/jobs");
    $("#job-list").innerHTML = (data.jobs || [])
      .map((j) => `<li data-id="${esc(j.id)}" class="${j.id === selectedJobId ? "selected" : ""}"><span>${esc(j.action)}</span><span class="badge ${j.state === "done" ? "ok" : j.state === "failed" ? "err" : "warn"}">${esc(j.state)}</span></li>`)
      .join("");
    $("#job-list").querySelectorAll("li").forEach((li) => {
      li.onclick = () => { selectedJobId = li.dataset.id; pollJobs(); tailLog(); };
    });
  } catch (_) {}
}

async function tailLog() {
  if (!selectedJobId) return;
  try {
    const res = await fetch(`/api/jobs/${selectedJobId}/log`);
    const box = $("#log-box");
    box.textContent = await res.text();
    box.scrollTop = box.scrollHeight;
  } catch (_) {}
}

function alertErr(e) {
  alert(e.message || String(e));
}

// --- boot ---
projectWorkspace?.bind();
if (projectWorkspace) {
  projectWorkspace.onValidateResults = (results) => {
    if (currentActivityMsgIndex >= 0 && !chatBusy && !window.__geChatBusy) {
      GenActivity.onValidationResults(results);
    }
  };
}
setTopbarChatMode(true);
wireSessionList();
loadProjects();
loadSessions();
renderProjects();
renderSessions();
renderChatMessages();
loadSessionContext();
renderExternalCards();
renderActiveModel();
loadRecommendations();
refreshStatus().then(() => setTimeout(() => tryResumeGeneration(), 600));
pollJobs();
setInterval(() => { refreshStatus(); pollJobs(); }, 4000);
setInterval(tailLog, 1500);
