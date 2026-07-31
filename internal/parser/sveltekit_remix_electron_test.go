package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseTypeScript_SvelteKitLoadRole(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { greet, healthPayload } from "$lib/greet";

export async function load() {
  return {
    message: greet("sveltekit"),
    health: healthPayload(),
  };
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/routes/+page.server.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var loadID string
	for _, s := range res.Symbols {
		if s.Name == "load" {
			loadID = s.ID
			if !strings.Contains(s.Signature, "sveltekit") {
				t.Errorf("load signature=%q want sveltekit", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=loader") {
				t.Errorf("load signature=%q want role=loader", s.Signature)
			}
		}
	}
	if loadID == "" {
		t.Fatalf("missing load; symbols=%#v", res.Symbols)
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != loadID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"greet", "healthPayload"} {
		if !targets[want] {
			t.Errorf("missing load→%q call; got %#v", want, targets)
		}
	}
}

func TestParseSvelte_SvelteKitPageRole(t *testing.T) {
	t.Parallel()
	src := []byte(`
<script lang="ts">
  export let data: { message: string };
</script>
<h1>{data.message}</h1>
`)
	res, err := ParseSvelte(context.Background(), "repo", "src/routes/+page.svelte", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, s := range res.Symbols {
		if s.Name == "+page" {
			found = true
			if !strings.Contains(s.Signature, "sveltekit") {
				t.Errorf("+page signature=%q want sveltekit", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=page") {
				t.Errorf("+page signature=%q want role=page", s.Signature)
			}
		}
	}
	if !found {
		t.Fatalf("missing +page component; symbols=%#v", res.Symbols)
	}
}

func TestParseTypeScript_RemixLoaderAction(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { greet, saveGreeting } from "../lib/greet";

export async function loader() {
  return { message: greet("remix") };
}

export async function action() {
  return { saved: saveGreeting("remix") };
}

export default function Index() {
  return null;
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "app/routes/_index.tsx", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byName := map[string]types.Symbol{}
	for _, s := range res.Symbols {
		byName[s.Name] = s
	}
	loader, ok := byName["loader"]
	if !ok {
		t.Fatalf("missing loader; symbols=%#v", byName)
	}
	if !strings.Contains(loader.Signature, "remix") || !strings.Contains(loader.Signature, "role=loader") {
		t.Errorf("loader signature=%q want remix+loader", loader.Signature)
	}
	action, ok := byName["action"]
	if !ok {
		t.Fatal("missing action")
	}
	if !strings.Contains(action.Signature, "role=action") {
		t.Errorf("action signature=%q want role=action", action.Signature)
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != loader.ID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	if !targets["greet"] {
		t.Errorf("missing loader→greet; got %#v", targets)
	}
	actionTargets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != action.ID {
			continue
		}
		actionTargets[symrefName(e.TargetID)] = true
	}
	if !actionTargets["saveGreeting"] {
		t.Errorf("missing action→saveGreeting; got %#v", actionTargets)
	}
}

func TestParseTypeScript_ElectronIPCRoles(t *testing.T) {
	t.Parallel()
	mainSrc := []byte(`
const { app, BrowserWindow, ipcMain } = require("electron");

function greet(name) {
  return "hello " + name;
}

function handleGreet(_event, name) {
  return greet(name || "electron");
}

function createWindow() {}

app.whenReady().then(() => {
  ipcMain.handle("greet", handleGreet);
  createWindow();
});
`)
	main, err := ParseTypeScript(context.Background(), "repo", "src/main/main.js", mainSrc)
	if err != nil {
		t.Fatalf("parse main: %v", err)
	}
	foundIPC := false
	var handleGreetID string
	for _, s := range main.Symbols {
		if s.Name == "handleGreet" {
			handleGreetID = s.ID
			if !strings.Contains(s.Signature, "electron") {
				t.Errorf("handleGreet signature=%q want electron", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=main") {
				t.Errorf("handleGreet signature=%q want role=main", s.Signature)
			}
		}
		if strings.HasPrefix(s.Name, "electron_ipc_") {
			foundIPC = true
			if !strings.Contains(s.Signature, "role=main") {
				t.Errorf("ipc site signature=%q want role=main", s.Signature)
			}
		}
		if s.Name == "createWindow" && !strings.Contains(s.Signature, "role=main") {
			t.Errorf("createWindow signature=%q want role=main", s.Signature)
		}
	}
	if !foundIPC {
		t.Fatalf("missing electron_ipc_* site; symbols=%#v", main.Symbols)
	}
	if handleGreetID == "" {
		t.Fatal("missing handleGreet")
	}
	// ipcMain.handle site should call handleGreet
	called := false
	for _, e := range main.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		if strings.Contains(e.SourceID, "electron_ipc_") && symrefName(e.TargetID) == "handleGreet" {
			called = true
		}
	}
	if !called {
		t.Errorf("missing ipc site→handleGreet call; edges=%#v", main.Edges)
	}

	preloadSrc := []byte(`
const { contextBridge, ipcRenderer } = require("electron");

function greet(name) {
  return ipcRenderer.invoke("greet", name);
}

contextBridge.exposeInMainWorld("api", { greet });
`)
	preload, err := ParseTypeScript(context.Background(), "repo", "src/preload/preload.js", preloadSrc)
	if err != nil {
		t.Fatalf("parse preload: %v", err)
	}
	foundPreloadIPC := false
	for _, s := range preload.Symbols {
		if strings.HasPrefix(s.Name, "electron_ipc_") {
			foundPreloadIPC = true
			if !strings.Contains(s.Signature, "role=preload") {
				t.Errorf("preload ipc signature=%q want role=preload", s.Signature)
			}
		}
		if s.Name == "greet" && !strings.Contains(s.Signature, "role=preload") {
			t.Errorf("preload greet signature=%q want role=preload", s.Signature)
		}
	}
	if !foundPreloadIPC {
		t.Fatalf("missing preload electron_ipc_*; symbols=%#v", preload.Symbols)
	}

	rendererSrc := []byte(`
async function sayHello() {
  const msg = await window.api.greet("renderer");
  return msg;
}
`)
	renderer, err := ParseTypeScript(context.Background(), "repo", "src/renderer/renderer.js", rendererSrc)
	if err != nil {
		t.Fatalf("parse renderer: %v", err)
	}
	foundSay := false
	for _, s := range renderer.Symbols {
		if s.Name == "sayHello" {
			foundSay = true
			if !strings.Contains(s.Signature, "electron") || !strings.Contains(s.Signature, "role=renderer") {
				t.Errorf("sayHello signature=%q want electron+renderer", s.Signature)
			}
		}
	}
	if !foundSay {
		t.Fatalf("missing sayHello; symbols=%#v", renderer.Symbols)
	}
}

func TestDetectFrameworkPacks_SvelteKitRemixElectron(t *testing.T) {
	t.Parallel()
	sk := DetectFrameworkPacks("src/routes/+page.server.ts", nil,
		"import type { PageServerLoad } from './$types';\nexport async function load(){ return {} }")
	if !containsFramework(sk, "sveltekit") {
		t.Fatalf("want sveltekit, got %v", sk)
	}
	remix := DetectFrameworkPacks("app/routes/_index.tsx", nil,
		"import { json } from '@remix-run/node';\nexport async function loader(){ return json({}) }\nexport default function Index(){ return null }")
	if !containsFramework(remix, "remix") {
		t.Fatalf("want remix, got %v", remix)
	}
	if containsFramework(remix, "laravel") {
		t.Fatalf("remix app/routes must not be laravel, got %v", remix)
	}
	el := DetectFrameworkPacks("src/main/main.js", nil,
		"const { ipcMain, app } = require('electron');\nipcMain.handle('greet', handleGreet);\napp.whenReady();")
	if !containsFramework(el, "electron") {
		t.Fatalf("want electron, got %v", el)
	}
}

func TestSvelteKitTestbed_Load(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "sveltekit", "src", "routes")
	src, err := os.ReadFile(filepath.Join(root, "+page.server.ts"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseTypeScript(context.Background(), "sk", "src/routes/+page.server.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range res.Symbols {
		if s.Name == "load" && strings.Contains(s.Signature, "role=loader") {
			found = true
		}
	}
	if !found {
		t.Fatalf("sveltekit bed missing load loader; symbols=%#v", res.Symbols)
	}
}

func TestRemixTestbed_Loader(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "remix", "app", "routes")
	src, err := os.ReadFile(filepath.Join(root, "_index.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseTypeScript(context.Background(), "r", "app/routes/_index.tsx", src)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range res.Symbols {
		if s.Name == "loader" && strings.Contains(s.Signature, "remix") {
			found = true
		}
	}
	if !found {
		t.Fatalf("remix bed missing loader; symbols=%#v", res.Symbols)
	}
}

func TestElectronTestbed_MainIPC(t *testing.T) {
	root := filepath.Join(testbedRoot(t), "electron", "src", "main")
	src, err := os.ReadFile(filepath.Join(root, "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseTypeScript(context.Background(), "e", "src/main/main.js", src)
	if err != nil {
		t.Fatal(err)
	}
	foundIPC, foundHandle := false, false
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "electron_ipc_") {
			foundIPC = true
		}
		if s.Name == "handleGreet" {
			foundHandle = true
		}
	}
	if !foundIPC || !foundHandle {
		t.Fatalf("electron bed missing ipc/handleGreet; symbols=%#v", res.Symbols)
	}
}
