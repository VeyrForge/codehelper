package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLineForHotPath_ExpressApplication(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "application.js")
	src := `/*!
 * express
 * MIT Licensed
 */

'use strict';

/**
 * Module dependencies.
 */

var debug = require('debug')('express:application');
var Router = require('router');

/**
 * Application prototype.
 */

var app = exports = module.exports = {};

/**
 * Initialize the server.
 */

app.init = function init() {
  var router = null;
}
`
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LineForHotPath(abs)
	if got < 20 {
		t.Fatalf("expected app.init function line (>20), got %d", got)
	}
}

func TestLibraryPerfGuidance_NeedleAnchoredHotPaths(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib")
	_ = os.MkdirAll(lib, 0o755)
	appJS := `/*! express */
'use strict';
var app = exports = module.exports = {};
app.init = function init() {}
app.handle = function handle(req, res, callback) {
  var router = this._router;
}
app.use = function use(fn) {}
`
	resJS := `/*! response */
'use strict';
var res = Object.create({});
res.status = function status(code) { return this; }
res.send = function send(body) { return this; }
`
	_ = os.WriteFile(filepath.Join(lib, "application.js"), []byte(appJS), 0o644)
	_ = os.WriteFile(filepath.Join(lib, "response.js"), []byte(resJS), 0o644)
	// Layout probe (lib/application.js) resolves express hints even when basename ≠ express.
	got := LibraryPerfGuidance(dir, ShapeFrameworkCore, 6)
	if len(got) == 0 {
		t.Fatal("expected library perf guidance via layout probe")
	}
	foundHandle := false
	foundSend := false
	for _, f := range got {
		if f.File == "lib/application.js" && f.Line > 1 {
			body, _ := os.ReadFile(filepath.Join(dir, "lib", "application.js"))
			lines := strings.Split(string(body), "\n")
			if f.Line <= len(lines) && strings.Contains(lines[f.Line-1], "app.handle") {
				foundHandle = true
			}
		}
		if f.File == "lib/response.js" && f.Line > 1 {
			body, _ := os.ReadFile(filepath.Join(dir, "lib", "response.js"))
			lines := strings.Split(string(body), "\n")
			if f.Line <= len(lines) && strings.Contains(lines[f.Line-1], "res.send") {
				foundSend = true
			}
		}
	}
	if !foundHandle {
		t.Fatalf("expected app.handle cite, got %+v", got)
	}
	if !foundSend {
		t.Fatalf("expected res.send cite, got %+v", got)
	}
	pref := PreferLibraryHotPathLine(dir, "lib/application.js", 4)
	if pref <= 4 {
		t.Fatalf("PreferLibraryHotPathLine should upgrade weak line to app.handle, got %d", pref)
	}
}

func TestSkeletonPerfGuidance_NeedleRouteGet(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "routes"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "routes", "web.php"), []byte("<?php\n\nuse Illuminate\\Support\\Facades\\Route;\n\nRoute::get('/', function () {\n    return view('welcome');\n});\n"), 0o644)
	got := SkeletonPerfGuidance(dir, 4)
	if len(got) == 0 {
		t.Fatal("expected skeleton guidance")
	}
	found := false
	for _, f := range got {
		if f.File == "routes/web.php" && f.Line >= 5 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Route::get line >=5, got %+v", got)
	}
}

func TestLineForHotPath_CFunction(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "acl.c")
	src := `/*
 * Copyright
 */

#include "server.h"
#include "cluster.h"

rax *Users;

user *DefaultUser;

struct ACLCategoryItem {
    char *name;
};

void ACLInit(void) {
    Users = raxNew();
}
`
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LineForHotPath(abs)
	if got < 10 {
		t.Fatalf("expected ACLInit line, got %d", got)
	}
}

func TestLineForHotPath_GoPastImports(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "context.go")
	var b strings.Builder
	b.WriteString("// Copyright\n\npackage gin\n\nimport (\n")
	for i := 0; i < 30; i++ {
		b.WriteString("\t\"fmt\"\n")
	}
	b.WriteString(")\n\nconst MIMEJSON = \"application/json\"\n\ntype Context struct {\n\tRequest *http.Request\n}\n")
	if err := os.WriteFile(abs, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LineForHotPath(abs)
	if got <= 1 {
		t.Fatalf("expected type Context line >1, got %d", got)
	}
}

func TestLineForHotPath_JavaPackagePrivateClass(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "OwnerController.java")
	src := `/*
 * Copyright
 */
package org.springframework.samples.petclinic.owner;

import java.util.List;

@Controller
class OwnerController {

	public OwnerController(OwnerRepository owners) {}
}
`
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LineForHotPath(abs)
	if got <= 1 {
		t.Fatalf("expected class OwnerController line >1, got %d", got)
	}
}

func TestLineForSymbolDef_RubyClassPastMagicComment(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "users_controller.rb")
	src := `# frozen_string_literal: true

module Api
  class UsersController < ApplicationController
    def index
    end
  end
end
`
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// Graph often cites line=1 (file header) for Rails class symbols.
	got := LineForSymbolDef(abs, "UsersController", "class", 1)
	if got <= 1 {
		t.Fatalf("expected class UsersController line >1, got %d", got)
	}
	if got != 4 {
		t.Fatalf("expected class UsersController at line 4, got %d", got)
	}
	// Non-type kinds must not be rewritten (avoid moving function cites).
	if keep := LineForSymbolDef(abs, "index", "method", 1); keep != 1 {
		t.Fatalf("method line=1 must stay 1 without inventing, got %d", keep)
	}
	if keep := LineForSymbolDef(abs, "UsersController", "class", 4); keep != 4 {
		t.Fatalf("already-good line must stay, got %d", keep)
	}
}

func TestLineForSymbolDef_DjangoClassPastImports(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "views.py")
	src := `"""Views."""
from django.db import models
from django.shortcuts import get_object_or_404

class ArticleListView:
    def get(self):
        return []
`
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LineForSymbolDef(abs, "ArticleListView", "class", 1)
	if got < 5 {
		t.Fatalf("expected ArticleListView past imports, got %d", got)
	}
}

func TestSkeletonPerfGuidance_NotInventedHotspot(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "app", "Http", "Controllers"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "app", "Http", "Controllers", "Controller.php"),
		[]byte("<?php\nnamespace App\\Http\\Controllers;\n\nclass Controller\n{\n}\n"), 0o644)
	got := SkeletonPerfGuidance(dir, 4)
	if len(got) == 0 {
		t.Fatal("expected skeleton guidance")
	}
	if got[0].Line <= 0 {
		t.Fatal("expected real line")
	}
	if got[0].Severity != "low" || got[0].Confidence != "low" {
		t.Fatalf("skeleton must stay low/low, got sev=%s conf=%s", got[0].Severity, got[0].Confidence)
	}
	if !strings.Contains(strings.ToLower(got[0].Hint), "not a measured") &&
		!strings.Contains(strings.ToLower(got[0].Hint), "skeleton-not-hotspot") {
		t.Fatalf("hint must deny measured hotspot claim: %s", got[0].Hint)
	}
}

func TestAppPerfSmells_LaravelNPlusOne(t *testing.T) {
	dir := t.TempDir()
	ctrl := filepath.Join(dir, "app", "Http", "Controllers")
	_ = os.MkdirAll(ctrl, 0o755)
	_ = os.WriteFile(filepath.Join(ctrl, "UserController.php"), []byte(`<?php
namespace App\Http\Controllers;
class UserController {
    public function index() {
        $users = User::all();
        foreach ($users as $user) {
            $profile = User::find($user->id);
            echo $profile->name;
        }
    }
}
`), 0o644)
	got := AppPerfSmells(dir, ShapeApp, 8)
	if len(got) == 0 {
		t.Fatal("expected N+1 smell")
	}
	found := false
	for _, f := range got {
		if f.Rule == "n-plus-one-loop" && f.Line > 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected n-plus-one-loop with real line, got %+v", got)
	}
}

func TestAppPerfSmells_DjangoNPlusOne(t *testing.T) {
	dir := t.TempDir()
	views := filepath.Join(dir, "myapp", "views")
	_ = os.MkdirAll(views, 0o755)
	_ = os.WriteFile(filepath.Join(views, "users.py"), []byte(`
def list_users(request):
    users = User.objects.all()
    for u in users:
        profile = Profile.objects.get(user_id=u.id)
        print(profile.name)
`), 0o644)
	got := AppPerfSmells(dir, ShapeApp, 8)
	if len(got) == 0 {
		t.Fatal("expected django N+1 smell")
	}
}

func TestAppPerfSmells_SkipsLibrary(t *testing.T) {
	dir := t.TempDir()
	if got := AppPerfSmells(dir, ShapeFrameworkCore, 4); got != nil {
		t.Fatalf("library shape must skip app smells, got %+v", got)
	}
}

func TestAppPerfSmells_IgnoresJavaUtilObjectsImport(t *testing.T) {
	dir := t.TempDir()
	ctrl := filepath.Join(dir, "src", "main", "java", "demo", "controller")
	_ = os.MkdirAll(ctrl, 0o755)
	_ = os.WriteFile(filepath.Join(ctrl, "Fmt.java"), []byte(`
package demo.controller;
import java.util.Objects;
import java.util.Locale;
public class Fmt {
  public void run(java.util.List<String> xs) {
    for (String s : xs) {
      System.out.println(Objects.toString(s));
    }
  }
}
`), 0o644)
	got := AppPerfSmells(dir, ShapeApp, 8)
	for _, f := range got {
		if f.Rule == "n-plus-one-loop" {
			t.Fatalf("java.util.Objects must not be N+1, got %+v", f)
		}
	}
}

func TestAppPerfSmells_GoStoreNPlusOne(t *testing.T) {
	dir := t.TempDir()
	api := filepath.Join(dir, "backend", "internal", "api")
	_ = os.MkdirAll(api, 0o755)
	_ = os.WriteFile(filepath.Join(api, "routes.go"), []byte(`
package api
func handleGuilds(store Store) {
	for _, g := range guilds {
		gid := parseUint64(g.ID)
		if isTeam, _ := store.IsTeamMember(gid, viewUID); isTeam {
			ids = append(ids, gid)
		}
	}
}
`), 0o644)
	got := AppPerfSmells(dir, ShapeApp, 8)
	found := false
	for _, f := range got {
		if f.Rule == "n-plus-one-loop" && f.Line > 1 && strings.Contains(f.File, "routes.go") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected Go store.IsTeamMember N+1, got %+v", got)
	}
}

func TestAppPerfSmells_SpringUnboundedFindAll(t *testing.T) {
	dir := t.TempDir()
	ctrl := filepath.Join(dir, "src", "main", "java", "demo", "vet")
	_ = os.MkdirAll(ctrl, 0o755)
	_ = os.WriteFile(filepath.Join(ctrl, "VetController.java"), []byte(`
package demo.vet;
@Controller
class VetController {
  @GetMapping({ "/vets" })
  public @ResponseBody Vets showResourcesVetList() {
    Vets vets = new Vets();
    vets.getVetList().addAll(this.vetRepository.findAll());
    return vets;
  }
}
`), 0o644)
	got := AppPerfSmells(dir, ShapeApp, 8)
	found := false
	for _, f := range got {
		if f.Rule == "sync-alloc-hot-path" && f.Line > 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected unbounded findAll sync-alloc, got %+v", got)
	}
}

func TestLineForHotPath_ExportFunction(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "api.js")
	_ = os.WriteFile(abs, []byte(`const API_URL = "http://localhost";

export function apiUrl(path) {
  return API_URL + path;
}
`), 0o644)
	got := LineForHotPath(abs)
	if got < 3 {
		t.Fatalf("expected export function line >=3, got %d", got)
	}
}
