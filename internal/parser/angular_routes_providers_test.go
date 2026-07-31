package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseTypeScript_AngularProvideUseClass(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { NgModule } from '@angular/core';
import { HeroService } from './hero.service';
import { MockHeroService } from './mock-hero.service';

@NgModule({
  providers: [
    { provide: HeroService, useClass: MockHeroService },
    { provide: 'ALIAS', useExisting: HeroService },
    { provide: 'CFG', useFactory: createConfig, deps: [ConfigService] },
  ],
})
export class HeroModule {}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app/hero/hero.module.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var bindID string
	for _, sym := range res.Symbols {
		if strings.HasPrefix(sym.Name, "angular_bind_") {
			bindID = sym.ID
			if !strings.Contains(sym.Signature, "role=provider_bind") {
				t.Errorf("bind signature=%q want role=provider_bind", sym.Signature)
			}
			if !strings.Contains(sym.Signature, "angular") {
				t.Errorf("bind signature=%q want angular", sym.Signature)
			}
		}
	}
	if bindID == "" {
		t.Fatalf("missing angular_bind_* symbol; symbols=%#v", res.Symbols)
	}
	calls := map[string]bool{}
	for _, edge := range res.Edges {
		if edge.Kind == types.RefKindCalls {
			calls[symrefName(edge.TargetID)] = true
		}
	}
	for _, want := range []string{"HeroService", "MockHeroService", "createConfig", "ConfigService"} {
		if !calls[want] {
			t.Errorf("missing DI edge to %q; got %#v", want, calls)
		}
	}
}

func TestParseTypeScript_AngularInjectableProvidedIn(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Injectable } from '@angular/core';
import { HeroModule } from './hero.module';

@Injectable({ providedIn: HeroModule })
export class HeroService {
  findOne(id: number): string { return String(id); }
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app/hero/hero.service.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var svcID string
	for _, s := range res.Symbols {
		if s.Name == "HeroService" {
			svcID = s.ID
			if !strings.Contains(s.Signature, "role=injectable") {
				t.Errorf("HeroService signature=%q want role=injectable", s.Signature)
			}
		}
	}
	if svcID == "" {
		t.Fatal("missing HeroService")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == svcID {
			targets[symrefName(e.TargetID)] = true
		}
	}
	if !targets["HeroModule"] {
		t.Errorf("missing providedIn edge to HeroModule; got %#v", targets)
	}
}

func TestParseTypeScript_AngularComponentViewProviders(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Component } from '@angular/core';
import { HeroService } from './hero.service';
import { LocaleService } from './locale.service';

@Component({
  selector: 'app-hero',
  template: '<p></p>',
  standalone: true,
  providers: [HeroService],
  viewProviders: [LocaleService],
})
export class HeroComponent {}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app/hero/hero.component.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var compID string
	for _, s := range res.Symbols {
		if s.Name == "HeroComponent" {
			compID = s.ID
		}
	}
	if compID == "" {
		t.Fatal("missing HeroComponent")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == compID {
			targets[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"HeroService", "LocaleService"} {
		if !targets[want] {
			t.Errorf("missing Component→%q edge; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_AngularRoutesComponentAndLazy(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Routes } from '@angular/router';
import { HomeComponent } from './home.component';

export const routes: Routes = [
  { path: '', component: HomeComponent },
  {
    path: 'admin',
    loadComponent: () => import('./admin.component').then(m => m.AdminComponent),
  },
  {
    path: 'heroes',
    loadChildren: () => import('./heroes.module').then(m => m.HeroesModule),
  },
];
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app/app.routes.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var routerID string
	for _, s := range res.Symbols {
		if s.Name == "routes" {
			routerID = s.ID
			if !strings.Contains(s.Signature, "role=router") {
				t.Errorf("routes signature=%q want role=router", s.Signature)
			}
			if !strings.Contains(s.Signature, "angular") {
				t.Errorf("routes signature=%q want angular", s.Signature)
			}
		}
	}
	if routerID == "" {
		t.Fatalf("missing routes router symbol; symbols=%#v", res.Symbols)
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == routerID {
			targets[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"HomeComponent", "AdminComponent", "HeroesModule"} {
		if !targets[want] {
			t.Errorf("missing route edge to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_AngularProvideRouterInline(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { ApplicationConfig } from '@angular/core';
import { provideRouter } from '@angular/router';
import { HomeComponent } from './home.component';

export const appConfig: ApplicationConfig = {
  providers: [
    provideRouter([
      { path: '', component: HomeComponent },
    ]),
  ],
};
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app/app.config.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var routerID string
	for _, s := range res.Symbols {
		if strings.Contains(s.Signature, "role=router") {
			routerID = s.ID
			break
		}
	}
	if routerID == "" {
		t.Fatalf("missing role=router symbol; symbols=%#v", res.Symbols)
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == routerID {
			targets[symrefName(e.TargetID)] = true
		}
	}
	if !targets["HomeComponent"] {
		t.Errorf("missing provideRouter→HomeComponent; got %#v", targets)
	}
}
