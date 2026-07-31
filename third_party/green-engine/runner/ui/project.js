/**
 * Project workspace — XML + strict Markdown parsing for multi-file LLM output.
 * Based on: XML artifact pattern (Bolt-Vibe), llm-code-format header detection,
 * fenced-block extraction with backreference-safe matching.
 */
(function (global) {
  const LANG_EXT = {
    php: "php", javascript: "js", js: "js", typescript: "ts", ts: "ts",
    python: "py", py: "py", rust: "rs", rs: "rs", go: "go", java: "java",
    css: "css", html: "html", json: "json", bash: "sh", sh: "sh", shell: "sh",
    sql: "sql", xml: "xml", yaml: "yml", yml: "yml", markdown: "md", md: "md",
  };

  const PATH_RE = /^[a-zA-Z0-9][a-zA-Z0-9_./-]*\.[a-zA-Z0-9]{1,12}$/;

  function normalizePath(p) {
    return String(p || "")
      .trim()
      .replace(/^['"`]+|['"`]+$/g, "")
      .replace(/^\.\//, "")
      .replace(/\\/g, "/")
      .replace(/\/+/g, "/");
  }

  function isPlaceholderPath(p) {
    const n = normalizePath(p).toLowerCase();
    if (!n || n.includes("..")) return true;
    if (/^path\/to\//.test(n)) return true;
    if (/^filename\.[a-z]+$/.test(n)) return true;
    if (/^file_[a-z0-9]+\.[a-z]+$/.test(n)) return true;
    if (/^your[-_]/.test(n)) return true;
    if (/^example\.[a-z]+$/.test(n)) return true;
    return false;
  }

  function isValidPath(p) {
    const n = normalizePath(p);
    if (!n || isPlaceholderPath(n)) return false;
    if (!PATH_RE.test(n.split("/").pop() || "")) return false;
    return true;
  }

  function looksLikeDirectoryTree(content) {
    const c = String(content || "").trim();
    if (!c) return true;
    if (/^[\s│├└──./\w-]+(\n[\s│├└─])+/m.test(c) && !/[;{}()=<>]/.test(c.slice(0, 200))) return true;
    if (/^(wordpress-|my-|bulk-)[\w-]+\/\s*\n\s*[├└│─]/m.test(c)) return true;
    return false;
  }

  function isTextFilePath(p) {
    const base = normalizePath(p).split("/").pop().toLowerCase();
    const ext = base.split(".").pop() || "";
    return (
      ext === "md" ||
      ext === "txt" ||
      ext === "rst" ||
      base === "license" ||
      base.startsWith("readme") ||
      base === "changelog"
    );
  }

  function looksLikeRealCode(content, path) {
    const c = String(content || "").trim();
    if (c.length < 8) return false;
    if (path && isTextFilePath(path)) return true;
    if (looksLikeDirectoryTree(c)) return false;
    if (/^#+\s/.test(c) && !/^#!/.test(c)) return false;
    return /[;{}()=<>]|^\s*(import |from |class |function |def |const |let |var |<?php|package |use |module\.)/m.test(c);
  }

  function stripGenerationWrappers(text) {
    let c = String(text || "").trim();
    c = c.replace(/^```(?:xml|markdown|md|php|json)?\s*\n?/i, "");
    c = c.replace(/\n```\s*$/g, "");
    c = c.replace(/```(?:json)?/gi, "");
    return c.trim();
  }

  function extFromLang(lang) {
    return LANG_EXT[(lang || "").toLowerCase()] || null;
  }

  function pathFromHeaderLine(line) {
    const raw = line.trim();
    let m =
      raw.match(/^\*\*([a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+)\*\*\s*:?\s*$/) ||
      raw.match(/^#{1,4}\s+`?([a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+)`?\s*:?\s*$/) ||
      raw.match(/^#{1,4}\s+\*\*([a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+)\*\*\s*:?\s*$/) ||
      raw.match(/^(?:File|Filename):\s*`*([a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+)`*\s*:?\s*$/i) ||
      raw.match(/^([a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+)\s*:\s*$/) ||
      raw.match(/^([a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+)\s*$/);
    return m ? normalizePath(m[1]) : "";
  }

  function pathFromComment(code) {
    for (const line of code.split("\n").slice(0, 6)) {
      const m =
        line.match(/^\s*(?:\/\/|#|<!--)\s*(?:file:\s*)?([a-zA-Z0-9_./-]+\.[a-z0-9]+)\s*(?:-->)?\s*$/i) ||
        line.match(/^\s*(?:\/\/|#)\s*([a-zA-Z0-9_./-]+\.[a-z0-9]+)\s*$/);
      if (m && isValidPath(m[1])) return normalizePath(m[1]);
    }
    return "";
  }

  function addFile(files, path, content) {
    const p = normalizePath(path);
    const code = String(content || "").replace(/\n$/, "");
    if (!isValidPath(p) || !looksLikeRealCode(code, p)) return;
    if (!files[p] || code.length > files[p].length) files[p] = code;
  }

  function ingestJsonObject(j, files) {
    if (!j || typeof j !== "object") return;
    if (j.path != null && j.content != null) {
      addFile(files, j.path, typeof j.content === "string" ? j.content : String(j.content));
      return;
    }
    if (Array.isArray(j.files)) {
      for (const f of j.files) {
        if (f?.path != null && f?.content != null) addFile(files, f.path, f.content);
      }
      return;
    }
    if (j.files && typeof j.files === "object" && !Array.isArray(j.files)) {
      for (const [p, c] of Object.entries(j.files)) {
        addFile(files, p, typeof c === "string" ? c : c?.content ?? "");
      }
    }
  }

  function salvageJsonObject(line) {
    const pm = line.match(/"path"\s*:\s*"((?:[^"\\]|\\.)*)"/);
    if (!pm) return null;
    let path;
    try {
      path = JSON.parse(`"${pm[1]}"`);
    } catch {
      path = pm[1];
    }
    const cm = line.match(/"content"\s*:\s*"((?:[^"\\]|\\.)*)"/);
    let content = "";
    if (cm) {
      try {
        content = JSON.parse(`"${cm[1]}"`);
      } catch {
        content = cm[1].replace(/\\n/g, "\n").replace(/\\"/g, '"').replace(/\\\\/g, "\\");
      }
    } else {
      const partial = line.match(/"content"\s*:\s*"([\s\S]*)$/);
      if (partial) {
        content = partial[1].replace(/\\n/g, "\n").replace(/\\"/g, '"').replace(/\\\\/g, "\\");
      }
    }
    if (!content || content.length < 8) return null;
    return { path, content };
  }

  function appendGenerationChunk(full, chunk) {
    const base = String(full || "");
    const add = String(chunk || "");
    if (!add) return base;
    if (!base) return add;
    if (base.endsWith("\n") || add.startsWith("\n")) return base + add;
    return `${base}\n${add}`;
  }

  function parseJsonFiles(text) {
    const files = {};
    const src = String(text || "").trim();
    if (!src) return files;

    const lines = src.split("\n");
    for (const line of lines) {
      const t = line.trim();
      if (!t || t === "{" || t === "}" || t === "[" || t === "]") continue;
      let j;
      try {
        j = JSON.parse(t);
      } catch {
        j = salvageJsonObject(t);
      }
      if (j) ingestJsonObject(j, files);
    }

    if (!Object.keys(files).length) {
      for (const chunk of src.split(/(?<=\})\s*(?=\{)/)) {
        const t = chunk.trim();
        if (!t) continue;
        let j;
        try {
          j = JSON.parse(t);
        } catch {
          j = salvageJsonObject(t);
        }
        if (j) ingestJsonObject(j, files);
      }
    }

    if (Object.keys(files).length) return files;

    const blobStart = src.indexOf("{");
    if (blobStart >= 0) {
      try {
        const j = JSON.parse(src.slice(blobStart));
        ingestJsonObject(j, files);
      } catch {
        const m = src.match(/\{[\s\S]*"files"\s*:\s*\[[\s\S]*\]\s*\}/);
        if (m) {
          try {
            ingestJsonObject(JSON.parse(m[0]), files);
          } catch (_) {}
        }
      }
    }
    return files;
  }

  function usesJsonFormat(text) {
    const c = stripGenerationWrappers(text).trim();
    if (!c) return false;
    if (/^\s*\{/.test(c) && !/<(?:green_project|file)\b/i.test(c)) return true;
    if (/"path"\s*:\s*"/.test(c) && !/<file\b/i.test(c)) return true;
    return false;
  }

  function jsonGenerationDone(text) {
    return /\{"done"\s*:\s*true\}/.test(String(text || ""));
  }

  function hasOpenJsonGeneration(text) {
    const c = stripGenerationWrappers(text);
    if (!usesJsonFormat(c)) return false;
    if (jsonGenerationDone(c)) return false;
    const lines = c.trim().split("\n").filter((l) => l.trim());
    const last = (lines[lines.length - 1] || "").trim();
    if (!last) return false;
    try {
      JSON.parse(last);
      return false;
    } catch {
      return true;
    }
  }

  function hasOpenGenerationTags(rawText) {
    if (hasOpenJsonGeneration(rawText)) return true;
    return hasOpenXmlTags(rawText);
  }

  function parseXmlFiles(text, includePartial) {
    const files = {};
    const src = stripGenerationWrappers(text);
    const re = /<file\s+(?:path|name)=["']([^"']+)["']\s*>([\s\S]*?)<\/file>/gi;
    let m;
    while ((m = re.exec(src)) !== null) {
      addFile(files, m[1], m[2]);
    }
    if (includePartial) {
      const openRe = /<file\s+(?:path|name)=["']([^"']+)["']\s*>([\s\S]*)$/i;
      const om = openRe.exec(src);
      if (om && isValidPath(om[1])) {
        const p = normalizePath(om[1]);
        const code = String(om[2] || "").trim();
        if (code.length >= 8 && (!files[p] || code.length > files[p].length)) {
          files[p] = code;
        }
      }
    }
    return files;
  }

  /** Non-greedy fences; optional path on line immediately before opening fence. */
  function parseMarkdownFiles(text) {
    const files = {};
    const src = String(text || "");
    let pendingPath = "";

    const lines = src.split("\n");
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      const headerPath = pathFromHeaderLine(line);
      if (headerPath && isValidPath(headerPath)) pendingPath = headerPath;

      const pathBeforeFence = line.match(/^([a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+)\s*$/);
      if (pathBeforeFence && isValidPath(pathBeforeFence[1])) pendingPath = normalizePath(pathBeforeFence[1]);

      const open = line.match(/^(`{3,})(\w*)\s*$/);
      if (open) {
        const fence = open[1];
        const lang = open[2];
        const buf = [];
        i += 1;
        while (i < lines.length && !lines[i].startsWith(fence)) {
          buf.push(lines[i]);
          i += 1;
        }
        const code = buf.join("\n");
        let path = pendingPath || pathFromComment(code);
        if (!path && lang) {
          const ext = extFromLang(lang);
          if (ext && pendingPath) path = pendingPath;
        }
        if (path) addFile(files, path, code);
        pendingPath = "";
        i += 1;
        continue;
      }
      i += 1;
    }

    const headerFence =
      /(?:^|\n)(?:#{1,4}\s+|File:\s*|filename:\s*)\*?([a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+)\*?\s*\n```[^\n]*\n([\s\S]*?)```/gi;
    let m;
    while ((m = headerFence.exec(src)) !== null) {
      addFile(files, m[1], m[2]);
    }

    return files;
  }

  function parseFilesFromText(text, includePartial) {
    const src = stripGenerationWrappers(text);
    const json = parseJsonFiles(src);
    const xml = parseXmlFiles(src, includePartial);
    const md = parseMarkdownFiles(src);
    return mergeFileMaps(mergeFileMaps(json, xml), md);
  }

  function extractExpectedPaths(text) {
    const paths = new Set();
    const src = String(text || "");

    const treeSection = src.match(
      /(?:directory structure|file structure|project structure|files?\s*(?:included)?:?)\s*\n+([\s\S]{0,3000}?)(?:\n\n#{1,3}\s|<file|<green_project|```\w)/i
    );
    const block = treeSection ? treeSection[1] : "";
    const scan = block || src.slice(0, 4000);
    const re = /(?:^|\n)\s*(?:[│├└─\s]+)?([a-zA-Z0-9][a-zA-Z0-9_./-]*\.[a-zA-Z0-9]{1,12})\s*(?:\n|$)/g;
    let m;
    while ((m = re.exec(scan)) !== null) {
      const p = normalizePath(m[1]);
      if (isValidPath(p)) paths.add(p);
    }

    Object.keys(parseFilesFromText(src)).forEach((p) => paths.add(p));
    return [...paths];
  }

  /** Paths listed in preamble/tree — never scan generated file bodies. */
  function extractPlannedPaths(text) {
    const paths = new Set();
    const src = String(text || "");
    const preamble = src.split(/<file\b/i)[0];
    const treeSection = preamble.match(
      /(?:directory structure|file structure|project structure|files?\s*(?:included)?:?)\s*\n+([\s\S]{0,3000}?)(?:\n\n#{1,3}\s|<green_project|```\w)/i
    );
    const block = treeSection ? treeSection[1] : "";
    const scan = block || preamble.slice(0, 2500);
    const re = /(?:^|\n)\s*(?:[│├└─\s]+)?([a-zA-Z0-9][a-zA-Z0-9_./-]*\.[a-zA-Z0-9]{1,12})\s*(?:\n|$)/g;
    let m;
    while ((m = re.exec(scan)) !== null) {
      const p = normalizePath(m[1]);
      if (isValidPath(p)) paths.add(p);
    }
    const jsonPathRe = /"path"\s*:\s*"((?:[^"\\]|\\.)*)"/g;
    while ((m = jsonPathRe.exec(src.slice(0, 8000))) !== null) {
      try {
        const p = normalizePath(JSON.parse(`"${m[1]}"`));
        if (isValidPath(p)) paths.add(p);
      } catch (_) {}
    }
    return [...paths];
  }

  function userRequestText(messages) {
    return (messages || [])
      .filter((x) => x.role === "user")
      .map((x) => x.content)
      .join("\n");
  }

  function wantsCodegenProject(messages, ctx) {
    if (ctx?.projectId) return true;
    const text = userRequestText(messages).toLowerCase();
    if (
      /\b(all files|multiple files|full project|complete project|every file|project structure|scaffold|entire|from scratch|full-featured|full featured|multi-file|multi file)\b/.test(
        text
      )
    ) {
      return true;
    }
    if (/\b(wordpress|woocommerce|wp-)\b/.test(text) && /\b(plugin|theme|extension|mu-plugin)\b/.test(text)) {
      return true;
    }
    const action =
      /\b(creat|writ|build|generat|mak|develop|implement|cod|design|scaffold|add|produc)\w*\b/.test(text);
    if (action && /\b(plugin|theme|extension|widget|app|api|service|module|package|library|website)\b/.test(text)) {
      return true;
    }
    return false;
  }

  function explainCodegenDetect(messages, ctx) {
    if (ctx?.projectId) return "project session (New Project)";
    const text = userRequestText(messages).toLowerCase();
    const bits = [];
    if (
      /\b(all files|multiple files|full project|complete project|every file|from scratch|full-featured|multi-file)\b/.test(
        text
      )
    ) {
      bits.push("multi-file keywords");
    }
    if (/\b(wordpress|woocommerce)\b/.test(text) && /\b(plugin|theme)\b/.test(text)) bits.push("wordpress plugin/theme");
    const action =
      /\b(creat|writ|build|generat|mak|develop|implement|cod|design|scaffold|add)\w*\b/.test(text);
    if (action && /\b(plugin|app|api|theme|module|package)\b/.test(text)) bits.push("create/build + plugin/app");
    if (!bits.length) {
      const preview = userRequestText(messages).replace(/\s+/g, " ").trim().slice(0, 80);
      return `no match — message: “${preview}${preview.length >= 80 ? "…" : ""}” (need e.g. “create wordpress plugin” or use + New Project)`;
    }
    return bits.join(", ");
  }

  function collectPlannedPaths(rawText, userMessages) {
    const paths = new Set();
    extractPlannedPaths(rawText).forEach((p) => paths.add(p));
    const userText = (userMessages || [])
      .filter((m) => m.role === "user")
      .map((m) => m.content)
      .join("\n");
    extractPlannedPaths(userText).forEach((p) => paths.add(p));
    return [...paths];
  }

  function hasOpenXmlTags(rawText) {
    const c = String(rawText || "");
    const openFiles = (c.match(/<file\b/gi) || []).length;
    const closeFiles = (c.match(/<\/file>/gi) || []).length;
    if (openFiles > closeFiles) return true;
    if (/<green_project\b/i.test(c) && !/<\/green_project>/i.test(c)) return true;
    return false;
  }

  function minPathContentLength(p) {
    if (p && isTextFilePath(p)) return 8;
    if (p && /\.php$/i.test(p)) return 40;
    return 30;
  }

  function pathPresent(files, p) {
    const keys = Object.keys(files);
    const base = p.split("/").pop();
    const found = keys.find(
      (k) => k === p || k.endsWith("/" + p) || k.split("/").pop() === base
    );
    const minLen = minPathContentLength(found || p);
    return found && (files[found] || "").trim().length >= minLen ? found : null;
  }

  function wordpressHasMainPhp(files) {
    return Object.keys(files).some(
      (k) => /\.php$/i.test(k) && (files[k] || "").trim().length >= 80
    );
  }

  function isWordpressPluginRequest(userMessages) {
    const text = userRequestText(userMessages).toLowerCase();
    return /\bwordpress\b/.test(text) && /\bplugin\b/.test(text);
  }

  function wordpressNeedsMainPhp(files, userMessages) {
    if (!isWordpressPluginRequest(userMessages)) return false;
    return !wordpressHasMainPhp(files);
  }

  function wordpressNeedsReadme(files, userMessages) {
    if (!isWordpressPluginRequest(userMessages)) return false;
    if (!wordpressHasMainPhp(files)) return false;
    return !Object.keys(files).some(
      (k) => isTextFilePath(k) && (files[k] || "").trim().length >= 8
    );
  }

  function wordpressEssentialsComplete(files, userMessages) {
    if (!isWordpressPluginRequest(userMessages)) return false;
    if (!wordpressHasMainPhp(files)) return false;
    const valid = Object.keys(files).filter((p) => looksLikeRealCode(files[p], p));
    const hasReadme = valid.some((k) => isTextFilePath(k));
    return hasReadme || valid.length >= 2;
  }

  function projectIncomplete(files, rawText, userMessages) {
    if (!wantsCodegenProject(userMessages)) return false;
    const valid = Object.keys(files).filter((p) => looksLikeRealCode(files[p], p));
    if (!valid.length) return true;

    if (hasOpenXmlTags(rawText)) return true;

    const planned = collectPlannedPaths(rawText, userMessages);
    for (const p of planned) {
      if (!pathPresent(files, p)) return true;
    }

    const raw = String(rawText || "");
    if (usesJsonFormat(raw)) {
      if (hasOpenJsonGeneration(raw)) return true;
      const planned = collectPlannedPaths(rawText, userMessages);
      for (const p of planned) {
        if (!pathPresent(files, p)) return true;
      }
      if (wordpressNeedsMainPhp(files, userMessages)) return true;
      if (wordpressEssentialsComplete(files, userMessages)) return false;
      return !jsonGenerationDone(raw);
    }

    const usesXml = /<(?:green_project|file)\b/i.test(raw);

    if (usesXml) {
      if (hasOpenXmlTags(raw)) return true;
      if (/<green_project\b/i.test(raw)) {
        return !/<\/green_project>/i.test(raw);
      }
      return false;
    }

    const userText = userMessages.map((m) => m.content).join(" ");
    const wantsFull = /\b(all files|full project|complete project|every file|fully working|detailed)\b/i.test(
      userText
    );

    if (wantsFull || planned.length > 0) {
      return true;
    }

    return false;
  }

  function mergeFileMaps(a, b) {
    const out = { ...a };
    for (const [p, c] of Object.entries(b)) {
      if (!isValidPath(p) || !looksLikeRealCode(c, p)) continue;
      if (!out[p] || c.length > out[p].length) out[p] = c;
    }
    return out;
  }

  function missingPaths(files, rawText, userMessages) {
    const expected = collectPlannedPaths(rawText, userMessages || []);
    const keys = Object.keys(files);
    return expected.filter((p) => {
      const base = p.split("/").pop();
      const found = keys.find((k) => k === p || k.endsWith("/" + p) || k.split("/").pop() === base);
      const minLen = minPathContentLength(found || p);
      return !found || (files[found] || "").length < minLen;
    });
  }

  function projectNameFromRaw(rawText) {
    const m = String(rawText || "").match(/^\s*\{"project"\s*:\s*"((?:[^"\\]|\\.)*)"/m);
    if (m) {
      try {
        return JSON.parse(`"${m[1]}"`);
      } catch {
        return m[1];
      }
    }
    return "";
  }

  function suggestReadmePath(files, rawText) {
    const keys = Object.keys(files);
    const existing = keys.find((k) => isTextFilePath(k));
    if (existing) return existing;
    const prefix = keys.find((k) => k.includes("/"))?.split("/")[0];
    return prefix ? `${prefix}/readme.txt` : "readme.txt";
  }

  function suggestMainPhpPath(files, rawText, userMessages) {
    const keys = Object.keys(files);
    const existing = keys.find((k) => /\.php$/i.test(k));
    if (existing) return existing;
    const project = projectNameFromRaw(rawText);
    const slug = (project || "plugin")
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "");
    const base = slug.includes("plugin") ? slug : `${slug}-plugin`;
    const prefix = keys.find((k) => k.includes("/"))?.split("/")[0];
    return prefix ? `${prefix}/${base}.php` : `${base}.php`;
  }

  const CODEGEN_PREFILL = '{"project":';

  const CODEGEN_SYSTEM = `You are a code generator. Output ONLY newline-delimited JSON (NDJSON). Never markdown. Never numbered steps. Never explanations.

Line 1: {"project":"name"}
Then ONE file per turn: {"path":"relative/path.ext","content":"full source; escape quotes, use \\n for newlines"}
Do NOT output {"done":true} until the main plugin .php AND supporting files exist.

WordPress plugin minimum: main plugin.php (with plugin header), readme.txt, includes/ classes, admin/ if needed.
Never send {"done":true} after only readme.txt.

Forbidden: markdown fences, "Step 1", prose, XML.`;

  const FORMAT_FIX_PROMPT = `STOP — wrong format. You sent markdown/prose instead of NDJSON.

Output ONLY raw JSON lines starting NOW (no text before the first {):
{"project":"project-name"}
{"path":"path/to/file.ext","content":"..."}
{"done":true}

No steps. No explanations. Begin with {"project": on the next line.`;

  function analyzeGenerationOutput(text) {
    const c = stripGenerationWrappers(String(text || ""));
    const len = c.length;
    const parsed = Object.keys(parseFilesFromText(c, true)).length;
    if (!c.trim()) {
      return { phase: "empty", hint: "No output yet — waiting for model" };
    }
    const preview = c.replace(/\s+/g, " ").trim().slice(0, 100);

    if (usesJsonFormat(c)) {
      const lines = c.split("\n").filter((l) => l.trim()).length;
      if (jsonGenerationDone(c)) {
        return { phase: "json_done", hint: `${parsed} files · JSON complete (${len} chars)`, preview };
      }
      if (parsed > 0) {
        return {
          phase: "json_partial",
          hint: `${parsed} files parsed · ${lines} JSON lines (${len} chars) — waiting for {"done":true}`,
          preview,
        };
      }
      return {
        phase: "json_open",
        hint: `JSON started (${len} chars) — expect {"path":"…","content":"…"} lines`,
        preview,
      };
    }

    const fileOpen = (c.match(/<file\b/gi) || []).length;
    const fileClose = (c.match(/<\/file>/gi) || []).length;

    if (/<green_project\b/i.test(c) || fileOpen > 0) {
      if (parsed > 0) {
        return {
          phase: "xml_partial",
          hint: `${parsed} file${parsed !== 1 ? "s" : ""} in workspace · XML ${fileClose}/${fileOpen} closed (${len} chars)`,
          preview,
        };
      }
      if (fileOpen > fileClose) {
        return {
          phase: "xml_partial",
          hint: `XML in progress — ${fileClose}/${fileOpen} files closed (${len} chars)`,
          preview,
        };
      }
      if (/<green_project\b/i.test(c) && !/<\/green_project>/i.test(c)) {
        return {
          phase: "xml_open",
          hint: `XML started, no complete files yet (${len} chars)`,
          preview,
        };
      }
      return { phase: "xml_done", hint: `XML complete (${fileClose} files, ${len} chars)`, preview };
    }
    if (/```/.test(c)) {
      return {
        phase: "markdown",
        hint: `Markdown fences detected (${len} chars) — parsing fenced blocks as files`,
        preview,
      };
    }
    if (/<file\s+name=/i.test(c) && !/<file\s+path=/i.test(c)) {
      return {
        phase: "xml_name_attr",
        hint: `XML uses name= not path= (${len} chars) — accepted, prefer path=`,
        preview,
      };
    }
    return {
      phase: "prose",
      hint: `Not JSON yet (${len} chars) — need {"path":"…","content":"…"} lines, will re-prompt`,
      preview,
    };
  }

  const REVIEW_SYSTEM = `You are a senior code reviewer. Critically review the generated project against the user's request.

Check and report:
1. **Completeness** — missing files, features, or requirements from the request
2. **Security** — SQL injection, XSS, CSRF/nonces, auth, capability checks, sanitization, file uploads, open redirects
3. **Performance** — N+1 queries, uncached loops, autoload bloat, missing indexes
4. **Correctness** — bugs, edge cases, error handling, broken hooks/APIs
5. **Improvements** — concrete suggestions ranked by impact

Be specific (file + issue). If something is good, say so briefly. End with: **Verdict:** PASS / NEEDS WORK / INCOMPLETE and one sentence why.`;

  function buildReviewPrompt(userRequest, files) {
    const paths = Object.keys(files || {}).sort();
    const bodyCap = 12000;
    let used = 0;
    const parts = [];
    for (const p of paths) {
      const chunk = `### ${p}\n\`\`\`\n${files[p]}\n\`\`\`\n`;
      if (used + chunk.length > bodyCap) {
        parts.push(`### … (${paths.length - parts.length} more files omitted for context limit)`);
        break;
      }
      parts.push(chunk);
      used += chunk.length;
    }
    return `Original request:\n${userRequest}\n\nGenerated files (${paths.length}):\n${parts.join("\n")}\n\nReview this work. Did we miss anything? Security or performance issues? What should be improved?`;
  }

  function buildContinuePrompt(files, rawText, userMessages, opts) {
    const missing = missingPaths(files, rawText, userMessages || []);
    const json = usesJsonFormat(rawText) || !/<file\b/i.test(String(rawText || ""));
    const have = Object.keys(files).filter((p) => looksLikeRealCode(files[p], p));
    if (json) {
      if (opts?.doneOnly) {
        return 'If every required file exists, output ONLY this line: {"done":true}. If one file is still missing, output ONLY that file as one {"path":"…","content":"…"} line. No prose.';
      }
      const needPhp =
        wordpressNeedsMainPhp(files, userMessages || []) &&
        !have.some((p) => /\.php$/i.test(p) && (files[p] || "").trim().length >= 40);
      if (needPhp) {
        const phpPath = suggestMainPhpPath(files, rawText, userMessages);
        return `Output ONE NDJSON line — main plugin PHP only (plugin header + one hook, escaped JSON, under 800 chars):
{"path":"${phpPath}","content":"<?php\\n/** Plugin Name: ... */\\n..."}
Already have: ${have.join(", ") || "none"}.
Do NOT repeat those paths. No readme. No {"done":true}. No prose.`;
      }
      if (wordpressNeedsReadme(files, userMessages || [])) {
        const readmePath = suggestReadmePath(files, rawText);
        return `Output ONE NDJSON line — WordPress readme.txt only (short plugin description):
{"path":"${readmePath}","content":"..."}
Already have: ${have.join(", ")}. No PHP. No {"done":true}. No prose.`;
      }
      if (wordpressEssentialsComplete(files, userMessages || [])) {
        return 'Output ONLY this line: {"done":true}. No other files. No prose.';
      }
      const missingNew = missing.filter((p) => !pathPresent(files, p));
      if (missingNew.length) {
        const next = missingNew[0];
        return `Output ONLY this ONE file as a single NDJSON line (complete source, escaped JSON):
{"path":"${next}","content":"..."}
Already have: ${have.join(", ") || "none"}.
After this file, stop — do not output other files yet. No prose.`;
      }
      if (have.length && !jsonGenerationDone(rawText)) {
        return `Output exactly ONE new file as one NDJSON line: {"path":"relative/path.ext","content":"full source"}.
Already have (${have.length}): ${have.join(", ")}.
Do NOT repeat those paths. Do NOT send {"done":true} until main .php and all plugin files exist. No prose.`;
      }
      return 'Output ONE {"path":"…","content":"…"} line for the next file, or {"done":true} if finished. No prose.';
    }
    if (missing.length) {
      return `Output ONLY <green_project> XML with these missing files (complete contents):\n${missing.map((p) => `- ${p}`).join("\n")}\nDo not repeat files already sent. No prose. Close </green_project> when ALL files are done.`;
    }
    return "Output ONLY remaining <file> entries in <green_project> XML. No prose. Close </green_project> when every file is complete.";
  }

  class ProjectWorkspace {
    constructor() {
      this.files = {};
      this.validation = {};
      this.activePath = null;
      this.projectName = "green-project";
      this.onChange = null;
      this.onValidateResults = null;
      this._validateTimer = null;
      this._els = {};
    }

    bind() {
      this._els.root = document.getElementById("project-workspace");
      this._els.list = document.getElementById("project-files");
      this._els.editor = document.getElementById("project-editor");
      this._els.path = document.getElementById("project-editor-path");
      this._els.errors = document.getElementById("project-editor-errors");
      this._els.status = document.getElementById("project-status");
      if (!this._els.root) return;
      document.getElementById("btn-validate-project")?.addEventListener("click", () => this.validateAll());
      document.getElementById("btn-download-project")?.addEventListener("click", () => this.downloadZip());
      document.getElementById("btn-close-project")?.addEventListener("click", () => this.hide());
      this._els.editor?.addEventListener("input", () => {
        if (this.activePath) {
          this.files[this.activePath] = this._els.editor.value;
          this.validation[this.activePath] = null;
          this.renderFileList();
          this.scheduleAutoValidate();
          this.onChange?.(this.files);
        }
      });
    }

    show() {
      this._els.root?.classList.remove("hidden");
      document.getElementById("main-content")?.classList.add("project-mode");
    }

    hide() {
      this._els.root?.classList.add("hidden");
      document.getElementById("main-content")?.classList.remove("project-mode");
    }

    ingestText(text, merge = true, streaming = false) {
      const nameMatch =
        String(text).match(/^\s*\{"project"\s*:\s*"([^"]+)"/m) ||
        String(text).match(/<green_project\s+name=["']([^"']+)["']/i);
      if (nameMatch) this.projectName = nameMatch[1].replace(/[^\w.-]/g, "-") || "green-project";

      const parsed = parseFilesFromText(text, merge && streaming);
      this.files = merge ? mergeFileMaps(this.files, parsed) : parsed;
      const keys = Object.keys(this.files);
      if (!this.activePath || !this.files[this.activePath]) {
        this.activePath = keys.sort()[0] || null;
      }
      if (keys.length) {
        this.show();
        this.scheduleAutoValidate();
      } else if (!merge) this.hide();
      this.render();
      return this.files;
    }

    scheduleAutoValidate() {
      if (typeof window !== "undefined" && window.__geChatBusy) return;
      clearTimeout(this._validateTimer);
      this._validateTimer = setTimeout(() => this.validateAll(), 900);
    }

    render() {
      this.renderFileList();
      this.renderEditor();
      this.renderStatus();
    }

    renderStatus() {
      if (!this._els.status) return;
      const n = Object.keys(this.files).length;
      const bad = Object.values(this.validation).filter((v) => v && !v.ok).length;
      const ok = Object.values(this.validation).filter((v) => v && v.ok).length;
      let s = `${n} file${n !== 1 ? "s" : ""}`;
      if (ok) s += ` · ${ok} ok`;
      if (bad) s += ` · ${bad} error${bad !== 1 ? "s" : ""}`;
      this._els.status.textContent = s;
    }

    renderFileList() {
      if (!this._els.list) return;
      const paths = Object.keys(this.files).sort();
      this._els.list.innerHTML = paths.length
        ? paths
            .map((p) => {
              const v = this.validation[p];
              const badge = v ? (v.ok ? '<span class="badge ok">ok</span>' : '<span class="badge err">!</span>') : "";
              const active = p === this.activePath ? " active" : "";
              return `<li class="${active}" data-path="${encodeURIComponent(p)}"><span class="truncate">${p}</span>${badge}</li>`;
            })
            .join("")
        : '<li class="hint">No valid files parsed yet…</li>';
      this._els.list.querySelectorAll("li[data-path]").forEach((li) => {
        li.addEventListener("click", () => {
          this.activePath = decodeURIComponent(li.dataset.path);
          this.renderEditor();
          this.renderFileList();
        });
      });
    }

    renderEditor() {
      if (!this._els.editor || !this.activePath) return;
      this._els.path.textContent = this.activePath;
      this._els.editor.value = this.files[this.activePath] || "";
      const v = this.validation[this.activePath];
      this._els.errors.textContent = v && !v.ok ? (v.error || "Syntax error").split("\n")[0] : "";
      this._els.errors.classList.toggle("err", Boolean(v && !v.ok));
    }

    async validateAll() {
      const paths = Object.keys(this.files);
      if (!paths.length) return {};
      try {
        const res = await fetch("/api/validate", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ files: this.files }),
        });
        const data = await res.json();
        if (data.results) {
          this.validation = data.results;
          this.onValidateResults?.(data.results);
          this.render();
        }
        return data;
      } catch (e) {
        this._els.errors.textContent = e.message;
        return {};
      }
    }

    async downloadZip(name) {
      const paths = Object.keys(this.files);
      if (!paths.length) {
        alert("No valid files to download.");
        return;
      }
      if (typeof JSZip === "undefined") {
        alert("ZIP library not loaded — refresh the page.");
        return;
      }
      const zip = new JSZip();
      paths.forEach((p) => zip.file(p, this.files[p]));
      const blob = await zip.generateAsync({ type: "blob" });
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = (name || this.projectName || "green-project") + ".zip";
      a.click();
      URL.revokeObjectURL(a.href);
    }

    summaryHtml(rawText) {
      const n = Object.keys(this.files).length;
      if (!n) {
        const a = analyzeGenerationOutput(rawText || "");
        return `**Project workspace** — ${a.hint}`;
      }
      const bad = Object.values(this.validation).filter((v) => v && !v.ok).length;
      return `**Project workspace** — ${n} file${n !== 1 ? "s" : ""}${bad ? ` · ${bad} syntax error${bad !== 1 ? "s" : ""}` : ""}. Edit above · **Check syntax** · **Download ZIP**.`;
    }
  }

  global.GeProject = {
    ProjectWorkspace,
    appendGenerationChunk,
    parseFilesFromText,
    extractExpectedPaths,
    extractPlannedPaths,
    collectPlannedPaths,
    hasOpenXmlTags,
    hasOpenGenerationTags,
    usesJsonFormat,
    jsonGenerationDone,
    missingPaths,
    wantsCodegenProject,
    explainCodegenDetect,
    userRequestText,
    projectIncomplete,
    mergeFileMaps,
    analyzeGenerationOutput,
    buildReviewPrompt,
    REVIEW_SYSTEM,
    buildContinuePrompt,
    CODEGEN_SYSTEM,
    CODEGEN_PREFILL,
    FORMAT_FIX_PROMPT,
    isValidPath,
    looksLikeRealCode,
  };
})(typeof window !== "undefined" ? window : globalThis);
