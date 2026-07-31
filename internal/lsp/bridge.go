// Package lsp provides an optional, pragmatic language-server bridge for
// hover / definition / references. It shells to gopls, typescript-language-server
// (or typescript/lib/tsserver via that wrapper), or pyright when those binaries
// exist. Default codehelper graph tools stay untouched — callers opt in via the
// MCP `lsp` tool, and missing servers fall back gracefully.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Action is a one-shot LSP request kind.
type Action string

const (
	ActionHover           Action = "hover"
	ActionDefinition      Action = "definition"
	ActionReferences      Action = "references"
	ActionRename          Action = "rename"
	ActionImplementations Action = "implementations"
)

// Position is a 1-based file location (agent-friendly).
type Position struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	NewName string `json:"new_name,omitempty"` // rename action only
}

// Location is a file span returned by definition/references.
type Location struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Col     int    `json:"col,omitempty"`
	EndLine int    `json:"end_line,omitempty"`
	EndCol  int    `json:"end_col,omitempty"`
}

// Result is the structured outcome of a one-shot LSP query.
type Result struct {
	Action    Action     `json:"action"`
	Server    string     `json:"server,omitempty"`
	Language  string     `json:"language,omitempty"`
	Hover     string     `json:"hover,omitempty"`
	Locations []Location `json:"locations,omitempty"`
	OK        bool       `json:"ok"`
	Fallback  bool       `json:"fallback,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// Enabled reports whether the LSP bridge should attempt to run. CODEHELPER_LSP=0
// disables; =1 forces; unset/other = auto (try when a server binary exists).
func Enabled() bool {
	v := strings.TrimSpace(os.Getenv("CODEHELPER_LSP"))
	if v == "0" || strings.EqualFold(v, "off") || strings.EqualFold(v, "false") {
		return false
	}
	return true
}

// DetectServer picks a language server binary for path under repoRoot.
// Returns server label, argv, and language id.
func DetectServer(repoRoot, relPath string) (label string, argv []string, lang string, err error) {
	ext := strings.ToLower(filepath.Ext(relPath))
	switch ext {
	case ".go":
		if p, e := exec.LookPath("gopls"); e == nil {
			return "gopls", []string{p}, "go", nil
		}
		return "", nil, "go", errors.New("gopls not found on PATH")
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
		lang = "typescript"
		if ext == ".js" || ext == ".jsx" {
			lang = "javascript"
		}
		if p, e := exec.LookPath("typescript-language-server"); e == nil {
			return "typescript-language-server", []string{p, "--stdio"}, lang, nil
		}
		// Local node_modules/.bin (project-installed).
		if fileExists(filepath.Join(repoRoot, "node_modules", ".bin", "typescript-language-server")) ||
			fileExists(filepath.Join(repoRoot, "node_modules", ".bin", "typescript-language-server.cmd")) {
			bin := filepath.Join(repoRoot, "node_modules", ".bin", "typescript-language-server")
			if fileExists(bin + ".cmd") {
				bin = bin + ".cmd"
			}
			return "typescript-language-server", []string{bin, "--stdio"}, lang, nil
		}
		// Sibling of npx (npm global bin often on PATH for npx but not every shim).
		if bin := siblingOfNpx("typescript-language-server"); bin != "" {
			return "typescript-language-server", []string{bin, "--stdio"}, lang, nil
		}
		if npxArgs := npxServerArgs("typescript-language-server", "--stdio"); len(npxArgs) > 0 {
			return "typescript-language-server", npxArgs, lang, nil
		}
		return "", nil, lang, errors.New("typescript-language-server not found (npm i -g typescript-language-server typescript)")
	case ".py":
		if p, e := exec.LookPath("pyright-langserver"); e == nil {
			return "pyright-langserver", []string{p, "--stdio"}, "python", nil
		}
		if p, e := exec.LookPath("basedpyright-langserver"); e == nil {
			return "basedpyright-langserver", []string{p, "--stdio"}, "python", nil
		}
		// pyright binary supports --stdio via `pyright-langserver` shim; some
		// installs expose `pyright` only — try node module.
		if p, e := exec.LookPath("pyright"); e == nil {
			return "pyright", []string{p, "--stdio"}, "python", nil
		}
		if fileExists(filepath.Join(repoRoot, "node_modules", ".bin", "pyright-langserver")) ||
			fileExists(filepath.Join(repoRoot, "node_modules", ".bin", "pyright-langserver.cmd")) {
			bin := filepath.Join(repoRoot, "node_modules", ".bin", "pyright-langserver")
			if fileExists(bin + ".cmd") {
				bin = bin + ".cmd"
			}
			return "pyright-langserver", []string{bin, "--stdio"}, "python", nil
		}
		if bin := siblingOfNpx("pyright-langserver"); bin != "" {
			return "pyright-langserver", []string{bin, "--stdio"}, "python", nil
		}
		if bin := siblingOfNpx("pyright"); bin != "" {
			return "pyright", []string{bin, "--stdio"}, "python", nil
		}
		if npxArgs := npxServerArgs("pyright-langserver", "--stdio"); len(npxArgs) > 0 {
			return "pyright-langserver", npxArgs, "python", nil
		}
		return "", nil, "python", errors.New("pyright-langserver not found on PATH")
	default:
		return "", nil, "", fmt.Errorf("no LSP bridge for extension %q (supported: .go .ts/.tsx/.js/.jsx .py)", ext)
	}
}

// Query runs a one-shot hover/definition/references/rename/implementations
// request. It starts the language server, opens the file, issues the request,
// then shuts down.
func Query(ctx context.Context, repoRoot string, pos Position, action Action) (*Result, error) {
	switch action {
	case ActionHover, ActionDefinition, ActionReferences, ActionRename, ActionImplementations:
	default:
		return nil, fmt.Errorf("unknown action %q (use hover|definition|references|rename|implementations)", action)
	}
	if !Enabled() {
		return &Result{
			Action: action, Fallback: true, OK: false,
			Note: "LSP bridge disabled (CODEHELPER_LSP=0). Use context/query/trace on the graph instead.",
		}, nil
	}
	rel := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(pos.Path), "/"))
	abs := filepath.Join(repoRoot, filepath.FromSlash(rel))
	if !fileExists(abs) {
		return nil, fmt.Errorf("file not found: %s", rel)
	}
	label, argv, lang, err := DetectServer(repoRoot, rel)
	if err != nil {
		return &Result{
			Action: action, Language: lang, Fallback: true, OK: false,
			Note: err.Error() + " — falling back to graph tools (context/query/trace).",
		}, nil
	}
	if pos.Line < 1 {
		pos.Line = 1
	}
	if pos.Col < 1 {
		pos.Col = 1
	}

	body, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}

	timeout := 12 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 && rem < timeout {
			timeout = rem
		}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = repoRoot
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return &Result{
			Action: action, Server: label, Language: lang, Fallback: true, OK: false,
			Note: "failed to start " + label + ": " + err.Error(),
		}, nil
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	cli := &client{w: stdin, r: bufio.NewReader(stdout)}
	rootURI := pathToURI(repoRoot)
	fileURI := pathToURI(abs)

	if err := cli.call(cctx, "initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"hover":          map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"definition":     map[string]any{},
				"references":     map[string]any{},
				"rename":         map[string]any{"prepareSupport": false},
				"implementation": map[string]any{},
			},
		},
	}, nil); err != nil {
		return &Result{
			Action: action, Server: label, Language: lang, Fallback: true, OK: false,
			Note: "LSP initialize failed: " + err.Error(),
		}, nil
	}
	_ = cli.notify("initialized", map[string]any{})
	_ = cli.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        fileURI,
			"languageId": lang,
			"version":    1,
			"text":       string(body),
		},
	})

	params := map[string]any{
		"textDocument": map[string]any{"uri": fileURI},
		"position": map[string]any{
			"line":      pos.Line - 1,
			"character": pos.Col - 1,
		},
	}
	res := &Result{Action: action, Server: label, Language: lang}

	switch action {
	case ActionHover:
		var raw json.RawMessage
		if err := cli.call(cctx, "textDocument/hover", params, &raw); err != nil {
			res.Note = err.Error()
			res.Fallback = true
			return res, nil
		}
		res.Hover = extractHover(raw)
		res.OK = res.Hover != ""
		if !res.OK {
			res.Note = "hover returned empty — use graph_symbols / context name=… / query (LSP hover often empty without project-accurate position)"
			res.Fallback = true
		}
	case ActionDefinition:
		var raw json.RawMessage
		if err := cli.call(cctx, "textDocument/definition", params, &raw); err != nil {
			res.Note = err.Error()
			res.Fallback = true
			return res, nil
		}
		res.Locations = parseLocations(raw, repoRoot)
		res.OK = len(res.Locations) > 0
		if !res.OK {
			res.Note = "definition returned no locations — use graph_symbols / context / query / find_implementations"
			res.Fallback = true
		}
	case ActionReferences:
		params["context"] = map[string]any{"includeDeclaration": true}
		var raw json.RawMessage
		if err := cli.call(cctx, "textDocument/references", params, &raw); err != nil {
			res.Note = err.Error()
			res.Fallback = true
			return res, nil
		}
		res.Locations = parseLocations(raw, repoRoot)
		res.OK = len(res.Locations) > 0
		if !res.OK {
			res.Note = "references returned no locations — use graph callers via context / impact / trace"
			res.Fallback = true
		}
	case ActionRename:
		// Prefer textDocument/rename; if the server rejects it, fall back to
		// references so agents still get type-precise sites for a graph rename.
		newName := strings.TrimSpace(pos.NewName)
		if newName == "" {
			newName = "NewName"
		}
		renameParams := map[string]any{
			"textDocument": params["textDocument"],
			"position":     params["position"],
			"newName":      newName,
		}
		var raw json.RawMessage
		if err := cli.call(cctx, "textDocument/rename", renameParams, &raw); err != nil {
			// Fall through to references — rename is optional on many servers.
			params["context"] = map[string]any{"includeDeclaration": true}
			var refRaw json.RawMessage
			if rerr := cli.call(cctx, "textDocument/references", params, &refRaw); rerr != nil {
				res.Note = "rename unavailable (" + err.Error() + "); references also failed: " + rerr.Error() +
					" — use rename_symbol (graph) / context / impact"
				res.Fallback = true
				return res, nil
			}
			res.Locations = parseLocations(refRaw, repoRoot)
			res.OK = len(res.Locations) > 0
			res.Note = "LSP rename unsupported; returned references instead — merge with rename_symbol graph plan"
			if !res.OK {
				res.Fallback = true
				res.Note = "rename+references empty — use rename_symbol (graph heuristic)"
			}
			_ = cli.notify("shutdown", nil)
			return res, nil
		}
		res.Locations = parseWorkspaceEditLocations(raw, repoRoot)
		res.OK = len(res.Locations) > 0
		if !res.OK {
			res.Note = "rename returned no edits — use rename_symbol (graph) / references"
			res.Fallback = true
		} else {
			res.Note = "LSP rename WorkspaceEdit sites (preview locations; apply via editor or rename_symbol)"
		}
	case ActionImplementations:
		var raw json.RawMessage
		if err := cli.call(cctx, "textDocument/implementation", params, &raw); err != nil {
			res.Note = err.Error() + " — use find_implementations (Go heuristic) / query"
			res.Fallback = true
			return res, nil
		}
		res.Locations = parseLocations(raw, repoRoot)
		res.OK = len(res.Locations) > 0
		if !res.OK {
			res.Note = "implementation returned no locations — use find_implementations / query / context"
			res.Fallback = true
		}
	default:
		return nil, fmt.Errorf("unknown action %q (use hover|definition|references|rename|implementations)", action)
	}

	_ = cli.notify("shutdown", nil)
	return res, nil
}

type client struct {
	w       io.WriteCloser
	r       *bufio.Reader
	mu      sync.Mutex
	id      int
	pending map[int]chan rpcResult
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

func (c *client) nextID() int {
	c.id++
	return c.id
}

func (c *client) notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.write(msg)
}

func (c *client) call(ctx context.Context, method string, params any, out *json.RawMessage) error {
	c.mu.Lock()
	if c.pending == nil {
		c.pending = map[int]chan rpcResult{}
	}
	id := c.nextID()
	ch := make(chan rpcResult, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := c.write(msg); err != nil {
		return err
	}

	// Read until our response arrives (or ctx done). Servers may send
	// notifications interleaved — skip those.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		raw, err := c.read()
		if err != nil {
			return err
		}
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
			Method string `json:"method"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			continue
		}
		if envelope.Method != "" && len(envelope.ID) == 0 {
			continue // notification
		}
		var respID int
		if json.Unmarshal(envelope.ID, &respID) != nil {
			continue
		}
		c.mu.Lock()
		ch2, ok := c.pending[respID]
		if ok {
			delete(c.pending, respID)
		}
		c.mu.Unlock()
		if !ok {
			continue
		}
		if envelope.Error != nil {
			ch2 <- rpcResult{err: errors.New(envelope.Error.Message)}
		} else {
			ch2 <- rpcResult{result: envelope.Result}
		}
		if respID == id {
			r := <-ch
			if r.err != nil {
				return r.err
			}
			if out != nil {
				*out = r.result
			}
			return nil
		}
	}
}

func (c *client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	hdr := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(b))
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := io.WriteString(c.w, hdr); err != nil {
		return err
	}
	_, err = c.w.Write(b)
	return err
}

func (c *client) read() ([]byte, error) {
	var contentLen int
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			n := strings.TrimSpace(line[len("Content-Length:"):])
			contentLen, _ = strconv.Atoi(n)
		}
	}
	if contentLen <= 0 || contentLen > 8<<20 {
		return nil, fmt.Errorf("invalid Content-Length %d", contentLen)
	}
	buf := make([]byte, contentLen)
	_, err := io.ReadFull(c.r, buf)
	return buf, err
}

func extractHover(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return strings.TrimSpace(string(raw))
	}
	// contents may be string | MarkedString | MarkedString[] | MarkupContent
	var s string
	if json.Unmarshal(h.Contents, &s) == nil {
		return strings.TrimSpace(s)
	}
	var mc struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if json.Unmarshal(h.Contents, &mc) == nil && mc.Value != "" {
		return strings.TrimSpace(mc.Value)
	}
	var arr []json.RawMessage
	if json.Unmarshal(h.Contents, &arr) == nil {
		var parts []string
		for _, a := range arr {
			if json.Unmarshal(a, &s) == nil && s != "" {
				parts = append(parts, s)
				continue
			}
			if json.Unmarshal(a, &mc) == nil && mc.Value != "" {
				parts = append(parts, mc.Value)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

func parseLocations(raw json.RawMessage, repoRoot string) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	type lspPos struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	}
	type lspRange struct {
		Start lspPos `json:"start"`
		End   lspPos `json:"end"`
	}
	type lspLoc struct {
		URI   string   `json:"uri"`
		Range lspRange `json:"range"`
	}
	var one lspLoc
	var many []lspLoc
	if json.Unmarshal(raw, &many) != nil {
		if json.Unmarshal(raw, &one) == nil && one.URI != "" {
			many = []lspLoc{one}
		} else {
			// LocationLink[]
			var links []struct {
				TargetURI   string   `json:"targetUri"`
				TargetRange lspRange `json:"targetRange"`
			}
			if json.Unmarshal(raw, &links) == nil {
				for _, l := range links {
					many = append(many, lspLoc{URI: l.TargetURI, Range: l.TargetRange})
				}
			}
		}
	}
	var out []Location
	for _, l := range many {
		p := uriToPath(l.URI)
		if rel, err := filepath.Rel(repoRoot, p); err == nil && !strings.HasPrefix(rel, "..") {
			p = filepath.ToSlash(rel)
		} else {
			p = filepath.ToSlash(p)
		}
		out = append(out, Location{
			Path:    p,
			Line:    l.Range.Start.Line + 1,
			Col:     l.Range.Start.Character + 1,
			EndLine: l.Range.End.Line + 1,
			EndCol:  l.Range.End.Character + 1,
		})
	}
	return out
}

// parseWorkspaceEditLocations flattens an LSP WorkspaceEdit into file locations
// (rename preview). Text content is omitted — agents apply via rename_symbol or
// the editor.
func parseWorkspaceEditLocations(raw json.RawMessage, repoRoot string) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	type lspPos struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	}
	type lspRange struct {
		Start lspPos `json:"start"`
		End   lspPos `json:"end"`
	}
	type textEdit struct {
		Range lspRange `json:"range"`
	}
	var edit struct {
		Changes         map[string][]textEdit `json:"changes"`
		DocumentChanges []json.RawMessage     `json:"documentChanges"`
	}
	if json.Unmarshal(raw, &edit) != nil {
		return nil
	}
	var out []Location
	add := func(uri string, r lspRange) {
		p := uriToPath(uri)
		if rel, err := filepath.Rel(repoRoot, p); err == nil && !strings.HasPrefix(rel, "..") {
			p = filepath.ToSlash(rel)
		} else {
			p = filepath.ToSlash(p)
		}
		out = append(out, Location{
			Path: p, Line: r.Start.Line + 1, Col: r.Start.Character + 1,
			EndLine: r.End.Line + 1, EndCol: r.End.Character + 1,
		})
	}
	for uri, edits := range edit.Changes {
		for _, e := range edits {
			add(uri, e.Range)
		}
	}
	for _, dc := range edit.DocumentChanges {
		var tde struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []textEdit `json:"edits"`
		}
		if json.Unmarshal(dc, &tde) == nil && tde.TextDocument.URI != "" {
			for _, e := range tde.Edits {
				add(tde.TextDocument.URI, e.Range)
			}
		}
	}
	return out
}

func pathToURI(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		// Windows: C:/foo → file:///C:/foo
		return "file:///" + abs
	}
	return "file://" + abs
}

func uriToPath(uri string) string {
	u := strings.TrimPrefix(uri, "file://")
	u = strings.TrimPrefix(u, "/")
	// Windows file:///C:/… → C:/…
	if len(u) >= 2 && u[1] == ':' {
		return filepath.FromSlash(u)
	}
	if strings.HasPrefix(uri, "file:///") {
		rest := strings.TrimPrefix(uri, "file://")
		return filepath.FromSlash(rest)
	}
	return filepath.FromSlash(u)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// siblingOfNpx finds an npm-global shim next to npx when LookPath(pkg) fails
// (PATH often includes node/npx but not every globally installed binary).
func siblingOfNpx(pkg string) string {
	npx, err := exec.LookPath("npx")
	if err != nil {
		npx, err = exec.LookPath("npx.cmd")
	}
	if err != nil {
		return ""
	}
	dir := filepath.Dir(npx)
	for _, cand := range []string{
		filepath.Join(dir, pkg),
		filepath.Join(dir, pkg+".cmd"),
		filepath.Join(dir, pkg+".exe"),
	} {
		if fileExists(cand) {
			return cand
		}
	}
	return ""
}

// npxServerArgs builds `npx --no-install <pkg> …` so we never block on a
// download during hover/def/refs. Returns nil when npx is unavailable.
func npxServerArgs(pkg string, extra ...string) []string {
	npx, err := exec.LookPath("npx")
	if err != nil {
		npx, err = exec.LookPath("npx.cmd")
	}
	if err != nil {
		return nil
	}
	args := []string{npx, "--no-install", pkg}
	args = append(args, extra...)
	return args
}
