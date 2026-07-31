package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseTypeScript_AngularNgModuleDI(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { NgModule } from '@angular/core';
import { HeroComponent } from './hero.component';
import { HeroService } from './hero.service';

@NgModule({
  declarations: [HeroComponent],
  providers: [HeroService],
  exports: [HeroComponent],
})
export class HeroModule {}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app/hero/hero.module.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var moduleID string
	for _, s := range res.Symbols {
		if s.Name == "HeroModule" {
			moduleID = s.ID
			if !strings.Contains(s.Signature, "angular") {
				t.Errorf("HeroModule signature=%q want angular", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=module") {
				t.Errorf("HeroModule signature=%q want role=module", s.Signature)
			}
		}
	}
	if moduleID == "" {
		t.Fatalf("missing HeroModule; symbols=%#v", res.Symbols)
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != moduleID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"HeroComponent", "HeroService"} {
		if !targets[want] {
			t.Errorf("missing NgModule call edge to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_AngularComponentCtorInject(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Component } from '@angular/core';
import { HeroService } from './hero.service';

@Component({
  selector: 'app-hero',
  template: '<p>{{name}}</p>',
  standalone: true,
  providers: [HeroService],
})
export class HeroComponent {
  constructor(private readonly heroes: HeroService) {}

  load(): string {
    return this.heroes.findOne(1);
  }
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app/hero/hero.component.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var compID string
	for _, s := range res.Symbols {
		if s.Name == "HeroComponent" {
			compID = s.ID
			if !strings.Contains(s.Signature, "angular") {
				t.Errorf("HeroComponent signature=%q want angular", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=component") {
				t.Errorf("HeroComponent signature=%q want role=component", s.Signature)
			}
		}
	}
	if compID == "" {
		t.Fatal("missing HeroComponent")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != compID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	if !targets["HeroService"] {
		t.Errorf("missing Component→HeroService edge; got %#v", targets)
	}
	// load() → this.heroes.findOne(1) should resolve via ctor DI type.
	var loadID string
	for _, s := range res.Symbols {
		if s.Name == "load" && s.ParentID == "HeroComponent" {
			loadID = s.ID
			break
		}
	}
	if loadID == "" {
		t.Fatal("missing HeroComponent.load")
	}
	loadCalls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == loadID {
			loadCalls[symrefName(e.TargetID)] = true
		}
	}
	if !loadCalls["HeroService.findOne"] {
		t.Errorf("missing typed HeroService.findOne from load; got %#v", loadCalls)
	}
}

func TestParseTypeScript_AngularInjectFnTypedMethodCall(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Component, inject } from '@angular/core';
import { HeroService } from './hero.service';

@Component({
  selector: 'app-hero',
  template: '<p></p>',
  standalone: true,
})
export class HeroComponent {
  private readonly heroes = inject(HeroService);

  load(): string {
    return this.heroes.findOne(1);
  }
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app/hero/hero.component.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var loadID string
	for _, s := range res.Symbols {
		if s.Name == "load" && s.ParentID == "HeroComponent" {
			loadID = s.ID
			break
		}
	}
	if loadID == "" {
		t.Fatal("missing HeroComponent.load")
	}
	loadCalls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == loadID {
			loadCalls[symrefName(e.TargetID)] = true
		}
	}
	if !loadCalls["HeroService.findOne"] {
		t.Fatalf("expected inject()-typed HeroService.findOne; got %#v", loadCalls)
	}
}

func TestParseTypeScript_NextAppRouterRoles(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { NextResponse } from 'next/server';

export default function Page() {
  return null;
}

export async function GET() {
  return NextResponse.json({ ok: true });
}
`)
	page, err := ParseTypeScript(context.Background(), "repo", "app/dashboard/page.tsx", src)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	foundPage := false
	for _, s := range page.Symbols {
		if s.Name == "Page" {
			foundPage = true
			if !strings.Contains(s.Signature, "nextjs") {
				t.Errorf("Page signature=%q want nextjs", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=page") {
				t.Errorf("Page signature=%q want role=page", s.Signature)
			}
		}
	}
	if !foundPage {
		t.Fatalf("missing Page; symbols=%#v", page.Symbols)
	}

	routeSrc := []byte(`
import { NextResponse } from 'next/server';
export async function GET() {
  return NextResponse.json({ ok: true });
}
`)
	route, err := ParseTypeScript(context.Background(), "repo", "app/api/health/route.ts", routeSrc)
	if err != nil {
		t.Fatalf("parse route: %v", err)
	}
	foundGET := false
	for _, s := range route.Symbols {
		if s.Name == "GET" {
			foundGET = true
			if !strings.Contains(s.Signature, "role=route_handler") && !strings.Contains(s.Signature, "role=entrypoint") {
				// path role wins as route_handler
				t.Errorf("GET signature=%q want role=route_handler", s.Signature)
			}
			if !strings.Contains(s.Signature, "nextjs") {
				t.Errorf("GET signature=%q want nextjs", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=route_handler") {
				t.Errorf("GET signature=%q want role=route_handler", s.Signature)
			}
		}
	}
	if !foundGET {
		t.Fatalf("missing GET; symbols=%#v", route.Symbols)
	}
}

func TestParseTypeScript_NextMetadataServerActionMiddleware(t *testing.T) {
	t.Parallel()
	layoutSrc := []byte(`
import type { Metadata } from 'next';

export async function generateMetadata(): Promise<Metadata> {
  return { title: 'Layouts' };
}

export default function Layout({ children }: { children: React.ReactNode }) {
  return children;
}
`)
	layout, err := ParseTypeScript(context.Background(), "repo", "app/layouts/layout.tsx", layoutSrc)
	if err != nil {
		t.Fatalf("parse layout: %v", err)
	}
	foundMeta, foundPage := false, false
	for _, s := range layout.Symbols {
		if s.Name == "generateMetadata" {
			foundMeta = true
			if !strings.Contains(s.Signature, "nextjs") {
				t.Errorf("generateMetadata signature=%q want nextjs", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=metadata") {
				t.Errorf("generateMetadata signature=%q want role=metadata", s.Signature)
			}
		}
		if s.Name == "Layout" {
			foundPage = true
			if !strings.Contains(s.Signature, "role=layout") {
				t.Errorf("Layout signature=%q want role=layout", s.Signature)
			}
		}
	}
	if !foundMeta || !foundPage {
		t.Fatalf("missing metadata/layout; symbols=%#v", layout.Symbols)
	}

	actionSrc := []byte(`
'use server';

import { cookies } from 'next/headers';

export async function changeSessionAction() {
  (await cookies()).set('session-id', 'x');
}
`)
	action, err := ParseTypeScript(context.Background(), "repo", "app/private-cache/actions.ts", actionSrc)
	if err != nil {
		t.Fatalf("parse action: %v", err)
	}
	foundAction := false
	for _, s := range action.Symbols {
		if s.Name == "changeSessionAction" {
			foundAction = true
			if !strings.Contains(s.Signature, "role=server_action") {
				t.Errorf("changeSessionAction signature=%q want role=server_action", s.Signature)
			}
			if !strings.Contains(s.Signature, "nextjs") {
				t.Errorf("changeSessionAction signature=%q want nextjs", s.Signature)
			}
		}
	}
	if !foundAction {
		t.Fatalf("missing changeSessionAction; symbols=%#v", action.Symbols)
	}

	mwSrc := []byte(`
import { NextResponse } from 'next/server';
import type { NextRequest } from 'next/server';

export function middleware(request: NextRequest) {
  return NextResponse.next();
}
`)
	mw, err := ParseTypeScript(context.Background(), "repo", "middleware.ts", mwSrc)
	if err != nil {
		t.Fatalf("parse middleware: %v", err)
	}
	foundMW := false
	for _, s := range mw.Symbols {
		if s.Name == "middleware" {
			foundMW = true
			if !strings.Contains(s.Signature, "role=middleware") {
				t.Errorf("middleware signature=%q want role=middleware", s.Signature)
			}
		}
	}
	if !foundMW {
		t.Fatalf("missing middleware; symbols=%#v", mw.Symbols)
	}
}

func TestParseTypeScript_NextAppRouterLoadingErrorStaticParams(t *testing.T) {
	t.Parallel()

	loadingSrc := []byte(`
export default function Loading() {
  return null;
}
`)
	loading, err := ParseTypeScript(context.Background(), "repo", "app/blog/[slug]/loading.tsx", loadingSrc)
	if err != nil {
		t.Fatalf("parse loading: %v", err)
	}
	foundLoading := false
	for _, s := range loading.Symbols {
		if s.Name == "Loading" {
			foundLoading = true
			if !strings.Contains(s.Signature, "nextjs") {
				t.Errorf("Loading signature=%q want nextjs", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=loading") {
				t.Errorf("Loading signature=%q want role=loading", s.Signature)
			}
		}
	}
	if !foundLoading {
		t.Fatalf("missing Loading; symbols=%#v", loading.Symbols)
	}

	errorSrc := []byte(`
'use client';

export default function Error({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return null;
}
`)
	errFile, err := ParseTypeScript(context.Background(), "repo", "app/blog/[slug]/error.tsx", errorSrc)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	foundError := false
	for _, s := range errFile.Symbols {
		if s.Name == "Error" {
			foundError = true
			if !strings.Contains(s.Signature, "nextjs") {
				t.Errorf("Error signature=%q want nextjs", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=error") {
				t.Errorf("Error signature=%q want role=error", s.Signature)
			}
		}
	}
	if !foundError {
		t.Fatalf("missing Error; symbols=%#v", errFile.Symbols)
	}

	pageSrc := []byte(`
export async function generateStaticParams() {
  return [{ slug: 'a' }, { slug: 'b' }];
}

export default function Page({ params }: { params: { slug: string } }) {
  return null;
}
`)
	page, err := ParseTypeScript(context.Background(), "repo", "src/app/blog/[slug]/page.tsx", pageSrc)
	if err != nil {
		t.Fatalf("parse page: %v", err)
	}
	foundParams, foundPage := false, false
	for _, s := range page.Symbols {
		if s.Name == "generateStaticParams" {
			foundParams = true
			if !strings.Contains(s.Signature, "nextjs") {
				t.Errorf("generateStaticParams signature=%q want nextjs", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=static_params") {
				t.Errorf("generateStaticParams signature=%q want role=static_params", s.Signature)
			}
		}
		if s.Name == "Page" {
			foundPage = true
			if !strings.Contains(s.Signature, "role=page") {
				t.Errorf("Page signature=%q want role=page", s.Signature)
			}
		}
	}
	if !foundParams || !foundPage {
		t.Fatalf("missing static_params/page; symbols=%#v", page.Symbols)
	}

	layoutSrc := []byte(`
export default function Layout({ children }: { children: React.ReactNode }) {
  return children;
}
`)
	layout, err := ParseTypeScript(context.Background(), "repo", "app/(shop)/layout.tsx", layoutSrc)
	if err != nil {
		t.Fatalf("parse layout: %v", err)
	}
	foundLayout := false
	for _, s := range layout.Symbols {
		if s.Name == "Layout" {
			foundLayout = true
			if !strings.Contains(s.Signature, "role=layout") {
				t.Errorf("Layout signature=%q want role=layout", s.Signature)
			}
			if !strings.Contains(s.Signature, "nextjs") {
				t.Errorf("Layout signature=%q want nextjs", s.Signature)
			}
		}
	}
	if !foundLayout {
		t.Fatalf("missing Layout; symbols=%#v", layout.Symbols)
	}

	routeSrc := []byte(`
import { NextResponse } from 'next/server';

export async function GET() {
  return NextResponse.json({ ok: true });
}

export async function POST() {
  return NextResponse.json({ created: true }, { status: 201 });
}

export async function DELETE() {
  return NextResponse.json({ deleted: true });
}
`)
	route, err := ParseTypeScript(context.Background(), "repo", "app/api/items/route.ts", routeSrc)
	if err != nil {
		t.Fatalf("parse route: %v", err)
	}
	wantMethods := map[string]bool{"GET": false, "POST": false, "DELETE": false}
	for _, s := range route.Symbols {
		if _, ok := wantMethods[s.Name]; !ok {
			continue
		}
		wantMethods[s.Name] = true
		if !strings.Contains(s.Signature, "role=route_handler") {
			t.Errorf("%s signature=%q want role=route_handler", s.Name, s.Signature)
		}
		if !strings.Contains(s.Signature, "nextjs") {
			t.Errorf("%s signature=%q want nextjs", s.Name, s.Signature)
		}
	}
	for name, found := range wantMethods {
		if !found {
			t.Errorf("missing route handler %s; symbols=%#v", name, route.Symbols)
		}
	}

	// Shared handler module (not under app/**/route.*) — nextExportRole densifies
	// HTTP methods via next/server instead of falling through to role=entrypoint.
	sharedSrc := []byte(`
import { NextResponse } from 'next/server';

export async function PUT() {
  return NextResponse.json({ updated: true });
}
`)
	shared, err := ParseTypeScript(context.Background(), "repo", "lib/handlers/items.ts", sharedSrc)
	if err != nil {
		t.Fatalf("parse shared handler: %v", err)
	}
	foundPUT := false
	for _, s := range shared.Symbols {
		if s.Name == "PUT" {
			foundPUT = true
			if !strings.Contains(s.Signature, "role=route_handler") {
				t.Errorf("PUT signature=%q want role=route_handler", s.Signature)
			}
			if !strings.Contains(s.Signature, "nextjs") {
				t.Errorf("PUT signature=%q want nextjs", s.Signature)
			}
		}
	}
	if !foundPUT {
		t.Fatalf("missing PUT; symbols=%#v", shared.Symbols)
	}
}

func TestParseTypeScript_NuxtComposableRole(t *testing.T) {
	t.Parallel()
	src := []byte(`
export function useCounter() {
  const n = useState('count', () => 0)
  function increment() {
    n.value++
  }
  return { n, increment }
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "composables/useCounter.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, s := range res.Symbols {
		if s.Name == "useCounter" {
			found = true
			if !strings.Contains(s.Signature, "nuxt") {
				t.Errorf("useCounter signature=%q want nuxt", s.Signature)
			}
			if !strings.Contains(s.Signature, "role=composable") {
				t.Errorf("useCounter signature=%q want role=composable", s.Signature)
			}
		}
		if s.Name == "increment" {
			// nested function may or may not be indexed depending on walk — ignore
		}
	}
	if !found {
		t.Fatalf("missing useCounter; symbols=%#v", res.Symbols)
	}
}

func TestDetectFrameworkPacks_AngularAndNestDistinct(t *testing.T) {
	t.Parallel()
	angular := DetectFrameworkPacks("src/app/hero.component.ts", nil,
		"import { Component } from '@angular/core';\n@Component({selector:'app-hero',template:''})\nexport class HeroComponent {}")
	if !containsFramework(angular, "angular") {
		t.Fatalf("want angular, got %v", angular)
	}
	if containsFramework(angular, "nestjs") {
		t.Fatalf("angular file must not be nestjs, got %v", angular)
	}
	mod := DetectFrameworkPacks("src/app/hero.module.ts", nil,
		"import { NgModule } from '@angular/core';\n@NgModule({declarations:[]})\nexport class HeroModule {}")
	if !containsFramework(mod, "angular") {
		t.Fatalf("want angular on NgModule, got %v", mod)
	}
	if containsFramework(mod, "nestjs") {
		t.Fatalf("NgModule must not be nestjs (@module substring), got %v", mod)
	}
	svc := DetectFrameworkPacks("src/app/hero.service.ts", nil,
		"import { Injectable } from '@angular/core';\n@Injectable({providedIn:'root'})\nexport class HeroService {}")
	if !containsFramework(svc, "angular") {
		t.Fatalf("want angular on Injectable service, got %v", svc)
	}
	if containsFramework(svc, "nestjs") {
		t.Fatalf("Angular @Injectable must not be nestjs, got %v", svc)
	}
	nest := DetectFrameworkPacks("src/cats/cats.module.ts", nil,
		"import { Module } from '@nestjs/common';\n@Module({providers:[]})\nexport class CatsModule {}")
	if !containsFramework(nest, "nestjs") {
		t.Fatalf("want nestjs, got %v", nest)
	}
	if containsFramework(nest, "angular") {
		t.Fatalf("nest module must not be angular, got %v", nest)
	}
}
