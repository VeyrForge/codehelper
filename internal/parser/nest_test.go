package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseTypeScript_NestModuleDI(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Module } from '@nestjs/common';
import { CatsController } from './cats.controller';
import { CatsService } from './cats.service';

@Module({
  controllers: [CatsController],
  providers: [CatsService],
  imports: [CommonModule],
})
export class CatsModule {}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/cats/cats.module.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var moduleID string
	for _, s := range res.Symbols {
		if s.Name == "CatsModule" {
			moduleID = s.ID
			if !strings.Contains(s.Signature, "nestjs") {
				t.Errorf("CatsModule signature=%q want nestjs", s.Signature)
			}
		}
	}
	if moduleID == "" {
		t.Fatalf("missing CatsModule; symbols=%#v", res.Symbols)
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != moduleID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"CatsController", "CatsService", "CommonModule"} {
		if !targets[want] {
			t.Errorf("missing Module call edge to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_NestCtorInject(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Controller } from '@nestjs/common';
import { CatsService } from './cats.service';

@Controller('cats')
export class CatsController {
  constructor(private readonly catsService: CatsService) {}
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/cats/cats.controller.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var ctrlID string
	for _, s := range res.Symbols {
		if s.Name == "CatsController" {
			ctrlID = s.ID
		}
	}
	if ctrlID == "" {
		t.Fatal("missing CatsController")
	}
	found := false
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == ctrlID && symrefName(e.TargetID) == "CatsService" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected CatsController→CatsService inject edge; edges=%#v", res.Edges)
	}
}

func TestParseTypeScript_NestPropertyAndUseGuards(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Controller, Inject, UseGuards } from '@nestjs/common';
import { CatsService } from './cats.service';
import { AuthGuard } from './auth.guard';
import { OtherService } from './other.service';

@UseGuards(AuthGuard)
@Controller('cats')
export class CatsController {
  private readonly other: OtherService;

  constructor(@Inject(CatsService) private readonly cats: CatsService) {}
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/cats/cats.controller.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var ctrlID string
	for _, s := range res.Symbols {
		if s.Name == "CatsController" {
			ctrlID = s.ID
		}
	}
	if ctrlID == "" {
		t.Fatal("missing CatsController")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == ctrlID {
			targets[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"CatsService", "OtherService", "AuthGuard"} {
		if !targets[want] {
			t.Errorf("missing DI/use edge to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_NestProvideUseClass(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Module } from '@nestjs/common';
@Module({ providers: [{ provide: AnimalService, useClass: CatService }] })
export class AppModule {}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app.module.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var bindID string
	for _, sym := range res.Symbols {
		if strings.HasPrefix(sym.Name, "nest_bind_") {
			bindID = sym.ID
		}
	}
	if bindID == "" {
		t.Fatalf("missing nest bind symbol: %#v", res.Symbols)
	}
	calls := map[string]bool{}
	for _, edge := range res.Edges {
		if edge.Kind == types.RefKindCalls && edge.SourceID == bindID {
			calls[symrefName(edge.TargetID)] = true
		}
	}
	for _, want := range []string{"AnimalService", "CatService"} {
		if !calls[want] {
			t.Errorf("bind missing %q: %#v", want, calls)
		}
	}
}
