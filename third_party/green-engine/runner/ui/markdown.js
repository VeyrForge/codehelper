/** Markdown → HTML with code blocks, syntax highlighting, and unclosed-fence repair. */
(function (global) {
  function esc(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function closeOpenFences(text) {
    const ticks = (text.match(/```/g) || []).length;
    if (ticks % 2 === 1) return text + "\n```";
    return text;
  }

  const HIGHLIGHTERS = {
    php(src) {
      let h = src;
      h = h.replace(/(&lt;\?php|&lt;\?|\?&gt;)/g, '<span class="hl-meta">$1</span>');
      h = h.replace(
        /\b(function|class|public|private|protected|static|return|if|else|elseif|foreach|array|new|require|require_once|defined|add_action|register_activation_hook|global|namespace|use|const|true|false|null)\b/g,
        '<span class="hl-kw">$1</span>'
      );
      h = h.replace(/(\$[a-zA-Z_]\w*)/g, '<span class="hl-var">$1</span>');
      h = h.replace(/('([^'\\]|\\.)*')/g, '<span class="hl-str">$1</span>');
      h = h.replace(/(&quot;([^&quot;\\]|\\.)*&quot;)/g, '<span class="hl-str">$1</span>');
      h = h.replace(/(\/\/[^\n]*)/g, '<span class="hl-cmt">$1</span>');
      h = h.replace(/(#\s[^\n]*)/g, '<span class="hl-cmt">$1</span>');
      return h;
    },
    javascript(src) {
      let h = src;
      h = h.replace(
        /\b(function|const|let|var|return|if|else|class|import|export|from|async|await|new|true|false|null|undefined)\b/g,
        '<span class="hl-kw">$1</span>'
      );
      h = h.replace(/('([^'\\]|\\.)*'|&quot;([^&quot;\\]|\\.)*&quot;|`([^`\\]|\\.)*`)/g, '<span class="hl-str">$1</span>');
      h = h.replace(/(\/\/[^\n]*)/g, '<span class="hl-cmt">$1</span>');
      return h;
    },
    js(src) {
      return HIGHLIGHTERS.javascript(src);
    },
    python(src) {
      let h = src;
      h = h.replace(
        /\b(def|class|return|if|elif|else|import|from|as|with|for|while|True|False|None|pass|raise)\b/g,
        '<span class="hl-kw">$1</span>'
      );
      h = h.replace(/('([^'\\]|\\.)*'|&quot;([^&quot;\\]|\\.)*&quot;)/g, '<span class="hl-str">$1</span>');
      h = h.replace(/(#[^\n]*)/g, '<span class="hl-cmt">$1</span>');
      return h;
    },
    py(src) {
      return HIGHLIGHTERS.python(src);
    },
    bash(src) {
      let h = src;
      h = h.replace(/^(#.*)$/gm, '<span class="hl-cmt">$1</span>');
      h = h.replace(/\b(if|then|else|fi|for|do|done|echo|export|cd)\b/g, '<span class="hl-kw">$1</span>');
      return h;
    },
    sh(src) {
      return HIGHLIGHTERS.bash(src);
    },
    css(src) {
      let h = src;
      h = h.replace(/([.#][a-zA-Z0-9_-]+)/g, '<span class="hl-var">$1</span>');
      h = h.replace(/([a-z-]+)(\s*:)/g, '<span class="hl-kw">$1</span>$2');
      h = h.replace(/('([^'\\]|\\.)*'|&quot;([^&quot;\\]|\\.)*&quot;)/g, '<span class="hl-str">$1</span>');
      return h;
    },
    html(src) {
      let h = src;
      h = h.replace(/(&lt;\/?[a-zA-Z][^&gt;]*&gt;)/g, '<span class="hl-meta">$1</span>');
      h = h.replace(/('([^'\\]|\\.)*'|&quot;([^&quot;\\]|\\.)*&quot;)/g, '<span class="hl-str">$1</span>');
      return h;
    },
    json(src) {
      let h = src;
      h = h.replace(/(&quot;[^&quot;]+&quot;)(\s*:)/g, '<span class="hl-var">$1</span>$2');
      h = h.replace(/:\s*(&quot;[^&quot;]*&quot;)/g, ': <span class="hl-str">$1</span>');
      h = h.replace(/\b(true|false|null)\b/g, '<span class="hl-kw">$1</span>');
      return h;
    },
    sql(src) {
      let h = src;
      h = h.replace(
        /\b(SELECT|FROM|WHERE|INSERT|INTO|VALUES|UPDATE|SET|DELETE|CREATE|TABLE|IF|NOT|EXISTS|PRIMARY|KEY|AUTO_INCREMENT|INT|VARCHAR)\b/gi,
        '<span class="hl-kw">$1</span>'
      );
      h = h.replace(/('([^'\\]|\\.)*')/g, '<span class="hl-str">$1</span>');
      return h;
    },
  };

  function highlightCode(lang, raw) {
    const code = esc(raw);
    const key = (lang || "").toLowerCase();
    const fn = HIGHLIGHTERS[key];
    return fn ? fn(code) : code;
  }

  function codeBlock(lang, code) {
    const label = lang || "code";
    const inner = highlightCode(lang, code.replace(/\n$/, ""));
    return `<pre class="md-code"><div class="md-code-head"><span class="md-lang">${esc(label)}</span><button type="button" class="md-copy" title="Copy">Copy</button></div><code>${inner}</code></pre>`;
  }

  function renderMarkdown(src) {
    if (!src) return "";
    let text = closeOpenFences(String(src));
    const blocks = [];

    text = text.replace(/```(\w*)\n?([\s\S]*?)```/g, (_, lang, code) => {
      const i = blocks.length;
      blocks.push(codeBlock(lang.trim(), code));
      return `\x00BLOCK${i}\x00`;
    });

    text = esc(text);
    text = text.replace(/^### (.+)$/gm, "<h4 class='md-h'>$1</h4>");
    text = text.replace(/^## (.+)$/gm, "<h3 class='md-h'>$1</h3>");
    text = text.replace(/^# (.+)$/gm, "<h2 class='md-h'>$1</h2>");
    text = text.replace(/^([A-Za-z][\w .-]+:)\s*$/gm, "<div class='md-label'>$1</div>");
    text = text.replace(
      /^([a-zA-Z0-9_./-]+\.(php|js|ts|css|json|md|txt|html))\s*$/gm,
      "<div class='md-filename'>$1</div>"
    );
    text = text.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
    text = text.replace(/\*(.+?)\*/g, "<em>$1</em>");
    text = text.replace(/`([^`]+)`/g, "<code class='md-inline'>$1</code>");
    text = text.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
    text = text.replace(/^\s*[-*] (.+)$/gm, "<li>$1</li>");
    text = text.replace(/((?:<li>[\s\S]*?<\/li>\s*)+)/g, (m) => `<ul class="md-list">${m}</ul>`);
    text = text.replace(/\n\n+/g, "</p><p class='md-p'>");
    text = text.replace(/\n/g, "<br>");
    text = `<div class="md-doc"><p class="md-p">${text}</p></div>`;
    text = text.replace(/\x00BLOCK(\d+)\x00/g, (_, i) => blocks[Number(i)]);
    text = text.replace(/<p class='md-p'><\/p>/g, "");
    text = text.replace(/<div class="md-doc"><\/div>/g, "");
    return text;
  }

  global.GeMarkdown = { render: renderMarkdown, esc, closeOpenFences };
})(typeof window !== "undefined" ? window : globalThis);
