package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseRuby_SelfRecvType(t *testing.T) {
	t.Parallel()
	src := []byte(`
class User
  def id
    self.load
  end
  def load
    1
  end
end
`)
	res, err := ParseRuby(context.Background(), "r", "user.rb", src)
	if err != nil {
		t.Fatal(err)
	}
	var idID string
	for _, s := range res.Symbols {
		if s.Name == "id" {
			idID = s.ID
			if s.ParentID != "User" {
				t.Fatalf("ParentID=%q", s.ParentID)
			}
		}
	}
	if idID == "" {
		t.Fatal("missing id")
	}
	saw := false
	for _, e := range res.Edges {
		if e.SourceID == idID && e.Kind == types.RefKindCalls && symrefName(e.TargetID) == "User.load" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected self.load → User.load; edges=%#v", res.Edges)
	}
}

func TestParseRubyRequireAndCalls(t *testing.T) {
	src := []byte(`
require 'sinatra/base'
require_relative './helpers'

module Sinatra
  class Base
    def get(path)
      route(path)
    end
  end
end
`)
	res, err := ParseRuby(context.Background(), "r", "lib/app.rb", src)
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindImports {
			continue
		}
		id := e.TargetID
		if i := strings.LastIndex(id, ":"); i >= 0 {
			imports[id[i+1:]] = true
		}
	}
	for _, want := range []string{"sinatra/base", "./helpers"} {
		if !imports[want] {
			t.Errorf("missing require import %q; got %#v", want, imports)
		}
	}
	var sawGet bool
	for _, s := range res.Symbols {
		if s.Name == "get" {
			sawGet = true
			if s.ParentID != "Base" {
				t.Errorf("get ParentID=%q want Base", s.ParentID)
			}
		}
	}
	if !sawGet {
		t.Fatal("expected get method symbol")
	}
	var sawCall bool
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			sawCall = true
			break
		}
	}
	if !sawCall {
		t.Fatal("expected at least one calls edge from get body")
	}
}

func TestParseRuby_SinatraDSLEntrypoints(t *testing.T) {
	src := []byte(`
require 'sinatra'

get '/' do
  'hi'
end

post '/users' do
  status 201
end
`)
	res, err := ParseRuby(context.Background(), "r", "app.rb", src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !hasPrefixName(names, "sinatra_get_") || !hasPrefixName(names, "sinatra_post_") {
		t.Fatalf("expected sinatra DSL entrypoints, got %#v", names)
	}
	var sawGet, sawRoute bool
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		if strings.HasSuffix(e.TargetID, ":get") {
			sawGet = true
		}
		if strings.HasSuffix(e.TargetID, ":route") {
			sawRoute = true
		}
	}
	if !sawGet || !sawRoute {
		t.Fatalf("expected DSL→get and DSL→route calls; get=%v route=%v", sawGet, sawRoute)
	}
}

func TestParseRuby_RailsDSLEntrypoints(t *testing.T) {
	t.Parallel()
	routes := []byte(`
Rails.application.routes.draw do
  get "/users/:id", to: "users#show"
  resources :posts
end
`)
	res, err := ParseRuby(context.Background(), "r", "config/routes.rb", routes)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if hasPrefixName(names, "sinatra_") {
		t.Fatalf("Rails routes must not emit sinatra_* symbols; got %#v", names)
	}
	if !hasPrefixName(names, "rails_get_users_show_") && !hasPrefixName(names, "rails_get_") {
		t.Fatalf("expected rails_get_* route entrypoint, got %#v", names)
	}
	if !hasPrefixName(names, "rails_resources_posts_") {
		t.Fatalf("expected rails_resources_posts_*, got %#v", names)
	}
	calls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"UsersController", "show", "PostsController", "index", "create", "destroy"} {
		if !calls[want] {
			t.Errorf("route missing call to %q; got %#v", want, calls)
		}
	}

	ctrl := []byte(`
class UsersController < ApplicationController
  before_action :set_user
  def show
    @user
  end
  def set_user
    @user = User.find(1)
  end
end
`)
	cres, err := ParseRuby(context.Background(), "r", "app/controllers/users_controller.rb", ctrl)
	if err != nil {
		t.Fatal(err)
	}
	cnames := map[string]bool{}
	for _, s := range cres.Symbols {
		cnames[s.Name] = true
	}
	if !cnames["UsersController"] || !cnames["show"] || !cnames["set_user"] {
		t.Fatalf("expected controller symbols, got %#v", cnames)
	}
	if !hasPrefixName(cnames, "rails_before_action_set_user_") {
		t.Fatalf("expected before_action site, got %#v", cnames)
	}
	var sawSetUser bool
	for _, e := range cres.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":set_user") {
			sawSetUser = true
		}
	}
	if !sawSetUser {
		t.Fatal("expected before_action → set_user call")
	}
	var sawInherits bool
	for _, e := range cres.Edges {
		if e.Kind == types.RefKindInherits && strings.HasSuffix(e.TargetID, ":ApplicationController") {
			sawInherits = true
		}
	}
	if !sawInherits {
		t.Fatal("expected UsersController inherits ApplicationController")
	}

	model := []byte(`
class User < ActiveRecord::Base
  has_many :posts
  belongs_to :account
end
`)
	mres, err := ParseRuby(context.Background(), "r", "app/models/user.rb", model)
	if err != nil {
		t.Fatal(err)
	}
	mcalls := map[string]bool{}
	for _, e := range mres.Edges {
		if e.Kind == types.RefKindCalls {
			mcalls[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"Post", "Account", "has_many", "belongs_to"} {
		if !mcalls[want] {
			t.Errorf("model missing call to %q; got %#v", want, mcalls)
		}
	}
}

func TestParseRuby_RailsDSLDensify(t *testing.T) {
	t.Parallel()
	routes := []byte(`
Rails.application.routes.draw do
	root to: "home#index"
	get "/admin/users/:id", to: "admin/users#show"
	namespace :api do
	resources :accounts do
		member do
		get :preview
		end
		collection do
		get :search
		end
	end
	end
end
`)
	res, err := ParseRuby(context.Background(), "r", "config/routes.rb", routes)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range res.Symbols {
		names[s.Name] = true
	}
	if !hasPrefixName(names, "rails_root_home_index_") {
		t.Fatalf("expected root entrypoint, got %#v", names)
	}
	if !hasPrefixName(names, "rails_get_admin_users_show_") {
		t.Fatalf("expected namespaced to: entrypoint, got %#v", names)
	}
	if !hasPrefixName(names, "rails_resources_api_accounts_") {
		t.Fatalf("expected namespaced resources entrypoint, got %#v", names)
	}
	if !hasPrefixName(names, "rails_get_api_accounts_preview_") {
		t.Fatalf("expected member get :preview entrypoint, got %#v", names)
	}
	if !hasPrefixName(names, "rails_get_api_accounts_search_") {
		t.Fatalf("expected collection get :search entrypoint, got %#v", names)
	}
	calls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			calls[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{
		"HomeController", "index",
		"UsersController", "Admin", "show",
		"AccountsController", "Api", "preview", "search",
	} {
		if !calls[want] {
			t.Errorf("densify missing call to %q; got %#v", want, calls)
		}
	}

	ctrl := []byte(`
class ReportsController < ApplicationController
	skip_before_action :authenticate
	def index
	end
	def authenticate
	end
end
`)
	cres, err := ParseRuby(context.Background(), "r", "app/controllers/reports_controller.rb", ctrl)
	if err != nil {
		t.Fatal(err)
	}
	cnames := map[string]bool{}
	for _, s := range cres.Symbols {
		cnames[s.Name] = true
	}
	if !hasPrefixName(cnames, "rails_skip_before_action_authenticate_") {
		t.Fatalf("expected skip_before_action site, got %#v", cnames)
	}
}

func TestParseRuby_SuperclassAndMixins(t *testing.T) {
	src := []byte(`
module AuthHelper
end
class Base
end
class App < Base
  include AuthHelper
  extend Forwardable
  def run
    AuthHelper
  end
end
`)
	res, err := ParseRuby(context.Background(), "r", "app.rb", src)
	if err != nil {
		t.Fatal(err)
	}
	var appID string
	for _, s := range res.Symbols {
		if s.Name == "App" {
			appID = s.ID
		}
	}
	if appID == "" {
		t.Fatal("missing App")
	}
	var sawInherits, sawMixin bool
	for _, e := range res.Edges {
		if e.SourceID != appID {
			continue
		}
		if e.Kind == types.RefKindInherits && strings.HasSuffix(e.TargetID, ":Base") {
			sawInherits = true
		}
		if e.Kind == types.RefKindImplements && strings.HasSuffix(e.TargetID, ":AuthHelper") {
			sawMixin = true
		}
	}
	if !sawInherits || !sawMixin {
		t.Fatalf("inherits=%v mixin=%v edges=%#v", sawInherits, sawMixin, res.Edges)
	}
	for _, s := range res.Symbols {
		if s.Name != "App" {
			continue
		}
		if !strings.Contains(s.Signature, "embeds=") {
			t.Fatalf("App should have embeds=, got %q", s.Signature)
		}
		if !strings.Contains(s.Signature, "Base") || !strings.Contains(s.Signature, "AuthHelper") {
			t.Fatalf("App embeds missing Base/AuthHelper: %q", s.Signature)
		}
	}
}

func TestParseJavaImports(t *testing.T) {
	src := []byte(`
package org.example;
import java.util.List;
import static java.util.Collections.emptyList;
class Demo {
  void run() { List.of(); }
}
`)
	res, err := ParseJava(context.Background(), "j", "Demo.java", src)
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindImports {
			continue
		}
		id := e.TargetID
		if i := strings.LastIndex(id, ":"); i >= 0 {
			imports[id[i+1:]] = true
		}
	}
	for _, want := range []string{"java.util.List", "java.util.Collections.emptyList"} {
		if !imports[want] {
			t.Errorf("missing java import %q; got %#v", want, imports)
		}
	}
}

func TestParseCSharpUsings(t *testing.T) {
	src := []byte(`
using System;
using System.Collections.Generic;
using static System.Math;
namespace N {
  class C {
    void M() { Console.WriteLine(1); }
  }
}
`)
	res, err := ParseCSharp(context.Background(), "c", "C.cs", src)
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindImports {
			continue
		}
		id := e.TargetID
		if i := strings.LastIndex(id, ":"); i >= 0 {
			imports[id[i+1:]] = true
		}
	}
	for _, want := range []string{"System", "System.Collections.Generic", "System.Math"} {
		if !imports[want] {
			t.Errorf("missing csharp using %q; got %#v", want, imports)
		}
	}
}

func TestParseCSharp_BaseListAndGetComponent(t *testing.T) {
	src := []byte(`
using UnityEngine;
interface IDamageable {}
class Health : MonoBehaviour {}
[RequireComponent(typeof(Health))]
class Player : MonoBehaviour, IDamageable {
  void Start() {
    var h = GetComponent<Health>();
    gameObject.AddComponent<Rigidbody>();
    var bar = FindObjectOfType<HealthBar>();
    var child = GetComponentInChildren<Weapon>();
  }
}
`)
	res, err := ParseCSharp(context.Background(), "c", "Player.cs", src)
	if err != nil {
		t.Fatal(err)
	}
	var playerID, startID string
	for _, s := range res.Symbols {
		if s.Name == "Player" {
			playerID = s.ID
		}
		if s.Name == "Start" {
			startID = s.ID
			if s.ParentID != "Player" {
				t.Fatalf("Start ParentID=%q want Player", s.ParentID)
			}
		}
	}
	if playerID == "" || startID == "" {
		t.Fatalf("missing Player/Start; %#v", res.Symbols)
	}
	var sawIface bool
	for _, e := range res.Edges {
		if e.SourceID == playerID && e.Kind == types.RefKindImplements &&
			strings.HasSuffix(e.TargetID, ":IDamageable") {
			sawIface = true
		}
	}
	if !sawIface {
		t.Fatal("expected Player implements IDamageable")
	}
	reads := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindReads && (e.SourceID == startID || e.SourceID == playerID) {
			reads[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"Health", "HealthBar", "Weapon"} {
		if !reads[want] {
			t.Fatalf("expected Unity type read %q; got %#v", want, reads)
		}
	}
}
