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

func TestParseTypeScript_NestCtorInjectTypedMethodCall(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Controller, Get } from '@nestjs/common';
import { CatsService } from './cats.service';

@Controller('cats')
export class CatsController {
  constructor(private readonly catsService: CatsService) {}

  @Get()
  findAll() {
    return this.catsService.findAll();
  }
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "cats.controller.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var findID string
	for _, s := range res.Symbols {
		if s.Name == "findAll" && s.ParentID == "CatsController" {
			findID = s.ID
			break
		}
	}
	if findID == "" {
		t.Fatal("missing CatsController.findAll")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == findID {
			targets[symrefName(e.TargetID)] = true
		}
	}
	if !targets["CatsService.findAll"] {
		t.Fatalf("expected typed CatsService.findAll call; got %#v", targets)
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

func TestParseTypeScript_NestInjectDecoratorTypedMethodCall(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Controller, Get, Inject } from '@nestjs/common';
import { CatsService } from './cats.service';

@Controller('cats')
export class CatsController {
  constructor(@Inject(CatsService) private readonly cats) {}

  @Get()
  list() {
    return this.cats.findAll();
  }
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "cats.controller.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var listID string
	for _, s := range res.Symbols {
		if s.Name == "list" && s.ParentID == "CatsController" {
			listID = s.ID
			break
		}
	}
	if listID == "" {
		t.Fatal("missing CatsController.list")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == listID {
			targets[symrefName(e.TargetID)] = true
		}
	}
	if !targets["CatsService.findAll"] {
		t.Fatalf("expected @Inject-typed CatsService.findAll; got %#v", targets)
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

func TestParseTypeScript_NestUseExistingAndInject(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Module } from '@nestjs/common';
@Module({
  providers: [
    { provide: 'ALIAS', useExisting: CatsService },
    { provide: 'CFG', useFactory: createConfig, inject: [ConfigService, LoggerService] },
  ],
})
export class AppModule {}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app.module.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var moduleID string
	targets := map[string]bool{}
	for _, s := range res.Symbols {
		if s.Name == "AppModule" {
			moduleID = s.ID
		}
	}
	if moduleID == "" {
		t.Fatal("missing AppModule")
	}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == moduleID {
			targets[symrefName(e.TargetID)] = true
		}
		if e.Kind == types.RefKindCalls {
			targets[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"CatsService", "ConfigService", "LoggerService", "createConfig"} {
		if !targets[want] {
			t.Errorf("missing DI edge to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_NestRolesRoutesAndMiddlewareApply(t *testing.T) {
	t.Parallel()
	modSrc := []byte(`
import { MiddlewareConsumer, Module, NestModule, RequestMethod } from '@nestjs/common';
import { TypeOrmModule } from '@nestjs/typeorm';
import { UserController } from './user.controller';
import { UserService } from './user.service';
import { AuthMiddleware } from './auth.middleware';
import { UserEntity } from './user.entity';

@Module({
  imports: [TypeOrmModule.forFeature([UserEntity]), SharedModule],
  providers: [UserService],
  controllers: [UserController],
})
export class UserModule implements NestModule {
  configure(consumer: MiddlewareConsumer) {
    consumer.apply(AuthMiddleware).forRoutes({ path: 'user', method: RequestMethod.GET });
  }
}
`)
	mod, err := ParseTypeScript(context.Background(), "repo", "src/user/user.module.ts", modSrc)
	if err != nil {
		t.Fatalf("parse module: %v", err)
	}
	var moduleID string
	for _, s := range mod.Symbols {
		if s.Name == "UserModule" {
			moduleID = s.ID
			if !strings.Contains(s.Signature, "role=module") {
				t.Errorf("UserModule signature=%q want role=module", s.Signature)
			}
		}
	}
	if moduleID == "" {
		t.Fatal("missing UserModule")
	}
	targets := map[string]bool{}
	for _, e := range mod.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == moduleID {
			targets[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"UserController", "UserService", "UserEntity", "SharedModule", "AuthMiddleware"} {
		if !targets[want] {
			t.Errorf("module missing edge to %q; got %#v", want, targets)
		}
	}

	ctrlSrc := []byte(`
import { Controller, Get, Post } from '@nestjs/common';
import { UserService } from './user.service';

@Controller('users')
export class UserController {
  constructor(private readonly users: UserService) {}

  @Get()
  list() { return this.users.findAll(); }

  @Post('login')
  login() { return this.users.login(); }
}
`)
	ctrl, err := ParseTypeScript(context.Background(), "repo", "src/user/user.controller.ts", ctrlSrc)
	if err != nil {
		t.Fatalf("parse controller: %v", err)
	}
	foundCtrl, foundList, foundLogin := false, false, false
	for _, s := range ctrl.Symbols {
		switch {
		case s.Name == "UserController":
			foundCtrl = true
			if !strings.Contains(s.Signature, "role=controller") {
				t.Errorf("UserController signature=%q want role=controller", s.Signature)
			}
		case s.Name == "list" && s.ParentID == "UserController":
			foundList = true
			if !strings.Contains(s.Signature, "role=entrypoint") {
				t.Errorf("list signature=%q want role=entrypoint", s.Signature)
			}
		case s.Name == "login" && s.ParentID == "UserController":
			foundLogin = true
			if !strings.Contains(s.Signature, "role=entrypoint") {
				t.Errorf("login signature=%q want role=entrypoint", s.Signature)
			}
		}
	}
	if !foundCtrl || !foundList || !foundLogin {
		t.Fatalf("missing controller/entrypoint symbols; got %#v", ctrl.Symbols)
	}

	mwSrc := []byte(`
import { Injectable, NestMiddleware } from '@nestjs/common';
import { UserService } from './user.service';

@Injectable()
export class AuthMiddleware implements NestMiddleware {
  constructor(private readonly users: UserService) {}
  use(req: any, res: any, next: () => void) { next(); }
}
`)
	mw, err := ParseTypeScript(context.Background(), "repo", "src/user/auth.middleware.ts", mwSrc)
	if err != nil {
		t.Fatalf("parse middleware: %v", err)
	}
	foundMW := false
	for _, s := range mw.Symbols {
		if s.Name == "AuthMiddleware" {
			foundMW = true
			if !strings.Contains(s.Signature, "role=middleware") {
				t.Errorf("AuthMiddleware signature=%q want role=middleware", s.Signature)
			}
		}
	}
	if !foundMW {
		t.Fatal("missing AuthMiddleware")
	}
}

func TestParseTypeScript_NestGuardsInterceptorsPipesRoles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path, src, name, role string
	}{
		{
			"src/auth/auth.guard.ts",
			`import { CanActivate, Injectable } from '@nestjs/common';
@Injectable()
export class AuthGuard implements CanActivate {
  canActivate() { return true; }
}`,
			"AuthGuard", "guard",
		},
		{
			"src/logging/logging.interceptor.ts",
			`import { NestInterceptor, Injectable } from '@nestjs/common';
@Injectable()
export class LoggingInterceptor implements NestInterceptor {
  intercept(ctx: any, next: any) { return next.handle(); }
}`,
			"LoggingInterceptor", "interceptor",
		},
		{
			"src/parse/parse-int.pipe.ts",
			`import { PipeTransform, Injectable } from '@nestjs/common';
@Injectable()
export class ParseIntPipe implements PipeTransform {
  transform(value: any) { return parseInt(value, 10); }
}`,
			"ParseIntPipe", "pipe",
		},
		{
			"src/http/http-exception.filter.ts",
			`import { Catch, ExceptionFilter } from '@nestjs/common';
@Catch()
export class HttpExceptionFilter implements ExceptionFilter {
  catch() {}
}`,
			"HttpExceptionFilter", "filter",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.role, func(t *testing.T) {
			t.Parallel()
			res, err := ParseTypeScript(context.Background(), "repo", tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			found := false
			for _, s := range res.Symbols {
				if s.Name != tc.name {
					continue
				}
				found = true
				if !strings.Contains(s.Signature, "role="+tc.role) {
					t.Errorf("%s signature=%q want role=%s", tc.name, s.Signature, tc.role)
				}
			}
			if !found {
				t.Fatalf("missing %s; symbols=%#v", tc.name, res.Symbols)
			}
		})
	}
}

func TestParseTypeScript_NestUseGuardsInterceptorsPipes(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Controller, Get, UseGuards, UseInterceptors, UsePipes, UseFilters } from '@nestjs/common';
import { AuthGuard } from './auth.guard';
import { RolesGuard } from './roles.guard';
import { LoggingInterceptor } from './logging.interceptor';
import { ValidationPipe } from './validation.pipe';
import { ParseIntPipe } from './parse-int.pipe';
import { HttpExceptionFilter } from './http-exception.filter';

@UseGuards(AuthGuard, RolesGuard)
@UseInterceptors(LoggingInterceptor)
@UseFilters(HttpExceptionFilter)
@Controller('cats')
export class CatsController {
  @UsePipes(new ValidationPipe({ whitelist: true }), ParseIntPipe)
  @Get(':id')
  findOne() { return null; }
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
	for _, want := range []string{
		"AuthGuard", "RolesGuard", "LoggingInterceptor",
		"ValidationPipe", "ParseIntPipe", "HttpExceptionFilter",
	} {
		if !targets[want] {
			t.Errorf("missing Use* edge to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_NestInjectStringAndForwardRef(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Controller, Inject, Optional } from '@nestjs/common';
import { CatsService } from './cats.service';
import { DogsService } from './dogs.service';

@Controller('pets')
export class PetsController {
  @Inject('CONFIG_TOKEN')
  private readonly config: unknown;

  constructor(
    @Inject(forwardRef(() => CatsService)) private readonly cats: CatsService,
    @Optional() @Inject('LOGGER') private readonly logger: unknown,
    private readonly dogs: DogsService,
  ) {}
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/pets/pets.controller.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var ctrlID string
	for _, s := range res.Symbols {
		if s.Name == "PetsController" {
			ctrlID = s.ID
		}
	}
	if ctrlID == "" {
		t.Fatal("missing PetsController")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == ctrlID {
			targets[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"CONFIG_TOKEN", "CatsService", "LOGGER", "DogsService"} {
		if !targets[want] {
			t.Errorf("missing @Inject/DI edge to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_NestProvidersEdgeCases(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Module, Scope } from '@nestjs/common';
import { APP_GUARD } from '@nestjs/core';
import { CatsService } from './cats.service';
import { RolesGuard } from './roles.guard';
import { ConfigService } from './config.service';

@Module({
  providers: [
    CatsService,
    forwardRef(() => CircularService),
    { provide: APP_GUARD, useClass: RolesGuard },
    { provide: 'CONFIG', useValue: { env: 'test' } },
    {
      provide: ConfigService,
      useFactory: createConfig,
      inject: [ConfigService, 'LOGGER'],
      scope: Scope.REQUEST,
    },
  ],
  exports: [CatsService],
})
export class AppModule {}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app.module.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var moduleID string
	targets := map[string]bool{}
	bindTargets := map[string]bool{}
	for _, s := range res.Symbols {
		if s.Name == "AppModule" {
			moduleID = s.ID
		}
	}
	if moduleID == "" {
		t.Fatal("missing AppModule")
	}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		name := symrefName(e.TargetID)
		targets[name] = true
		if e.SourceID == moduleID {
			targets[name] = true
		}
		if strings.Contains(e.SourceID, "nest_bind_") {
			bindTargets[name] = true
		}
	}
	for _, want := range []string{"CatsService", "CircularService", "RolesGuard", "CONFIG", "ConfigService", "createConfig"} {
		if !targets[want] {
			t.Errorf("missing provider edge to %q; got %#v", want, targets)
		}
	}
	for _, want := range []string{"APP_GUARD", "RolesGuard"} {
		if !bindTargets[want] {
			t.Errorf("APP_GUARD bind missing %q; got %#v", want, bindTargets)
		}
	}
	// Scope.REQUEST noise must not become DI targets.
	if targets["Scope"] || targets["REQUEST"] {
		t.Errorf("unexpected Scope/REQUEST edges: %#v", targets)
	}
}

func TestParseTypeScript_NestMiddlewareApplyMulti(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { MiddlewareConsumer, Module, NestModule } from '@nestjs/common';
import { AuthMiddleware } from './auth.middleware';
import { LoggerMiddleware } from './logger.middleware';

@Module({ providers: [] })
export class AppModule implements NestModule {
  configure(consumer: MiddlewareConsumer) {
    consumer.apply(AuthMiddleware, LoggerMiddleware).forRoutes('*');
  }
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app.module.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var moduleID string
	for _, s := range res.Symbols {
		if s.Name == "AppModule" {
			moduleID = s.ID
		}
	}
	if moduleID == "" {
		t.Fatal("missing AppModule")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == moduleID {
			targets[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"AuthMiddleware", "LoggerMiddleware"} {
		if !targets[want] {
			t.Errorf("missing apply edge to %q; got %#v", want, targets)
		}
	}
}
