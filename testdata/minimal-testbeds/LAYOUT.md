# Minimal testbed stubs — layout for sibling agents

Tracked source trees under `testdata/minimal-testbeds/<bed>/`.
**One recipe:** [docs/TESTBEDS.md](../../docs/TESTBEDS.md) — `scripts/testbeds-all.sh` stages stubs + OSS into `.testbeds/active`.

`scripts/ci-prepare-minimal-testbeds.sh` copies them into an output dir, runs
`git init` + `codehelper analyze`, and prints `CODEHELPER_TESTBEDS=<out>`.

**Do not commit** `.codehelper/` indexes here — prepare always re-indexes.

## Single story (where things live)

| Path | Role | Git |
|---|---|---|
| `testdata/minimal-testbeds/<bed>/` | Stub **sources** (this tree) | tracked |
| `.testbeds/active/` | **Canonical** staged/indexed OUT (`CODEHELPER_TESTBEDS`) | ignored |
| `.testbeds/reports/<stamp>/` | Dated paired scorecards | ignored |
| `.testbeds/live-harness/` | Live harness source | tracked |
| `.eval-projects/<name>/` | OSS clone **cache** + live-harness extras (expensive; keep) | ignored |
| `.testbeds/real-oss/` | Legacy mega OUT — optional; prefer `active/` | ignored |
| `.ci-testbeds*` (legacy) | Obsolete root OUT scatter — delete via `scripts/testbeds-clean.sh --force` | ignored |
| `.tools/`, `.vendor/`, `/bin/` | Local toolchains + build outs | ignored |
| `**/.dart_tool/`, `**/.gradle/` | Flutter/Dart + Gradle caches under stubs | ignored |

**Regenerate:** [docs/LOCAL_CACHE.md](../../docs/LOCAL_CACHE.md) — `testbeds-clean` (delete scratch) then `testbeds-all.sh prepare` / `ci-prepare-...` / `prepare-oss-...` (rebuild OUT) then `codehelper analyze` (index).

Prefer `.testbeds/active` over any root `.ci-testbeds*` or `.testbeds/real-oss`. CI smoke example:

```bash
SUITE=ci scripts/ci-prepare-minimal-testbeds.sh .testbeds/ci-smoke
```

Scratch clone dirs under `.eval-projects` (names starting with `_`, e.g. `_*-tmp`) are local junk — delete them; live-harness discovery skips `_`-prefixed dirs.

## Suites

| Env `SUITE` | Beds prepared |
|---|---|
| `ci` (default) | `gin`, `nest`, `express` — GitHub Actions smoke |
| `extended` | all `Source=stub` beds from `internal/bench.StubBedNames()` |

## Bed inventory

| Bed | Lang | Probe symbol (paired) | Expected layout | Sibling fill-in |
|---|---|---|---|---|
| `gin` | Go | `Context.JSON` | `context.go`, `go.mod` | — (strong already) |
| `echo` | Go | `HealthHandler` / Routes→GET | `routes.go`, `go.mod` | optional labstack/echo OSS |
| `chi` | Go | `HealthHandler` / Routes→Get | `routes.go`, `go.mod` | optional go-chi/chi OSS |
| `beego` | Go | `UserController` / Router | `routes.go`, `go.mod` | optional beego OSS |
| `nest` | TS | `ArticleService` (RealWorld) / stub gold `CatsService` via `nest-starter` | dense: `.eval-projects/nestjs-sample`; stub fixture `src/cats/*.ts` (+ `sample/01|06`) staged as `nest-starter` | typescript-starter is live-harness only |
| `express` | JS | `createApplication` / `app.use` | `lib/application.js`, `middleware/*.js`, `examples/{hello-world,auth}` | router graph edges |
| `fastify` | TS | `listUsers` / `getUser` via `app.get` | `src/{app,users}.ts`, `package.json` | plugin encapsulate not claimed |
| `hono` | TS | `listUsers` / `greet` via `app.get` | `src/{index,users}.ts`, `package.json` | middleware/jsx not claimed |
| `laravel` | PHP | `User` model | `app/Models/User.php` (+ traits/extends, `routes/web.php`, factory collision under `database/factories/`) | full Laravel under `.eval-projects/laravel` |
| `symfony` | PHP | `UserController` / `show` → `UserService` | `src/Controller/UserController.php`, `src/Service/UserService.php`, `bin/console`, `composer.json` | full Symfony under `.eval-projects/symfony-demo` |
| `wordpress` | PHP | `ProbePlugin.boot` / hook sites | `wp-content/plugins/probe-plugin/`, `wp-content/themes/probe-theme/` | core subset under `.eval-projects/wordpress` |
| `sinatra` | Ruby | `Sinatra::Base` | `lib/sinatra/base.rb` (+ `include` mixin, `app.rb` DSL) | full Rails under `.eval-projects/rails` |
| `rails` | Ruby | `UsersController` / route→show | `config/routes.rb`, `app/controllers/`, `app/models/` | full Rails gem under `.eval-projects/rails` |
| `csharp` | C# | `UsersController` / `MapGet` → `UserService` | `Controllers/UsersController.cs`, `Services/UserService.cs`, `Program.cs` (+ legacy `Greeter.cs`) | real ASP.NET OSS optional |
| `unity` | C# | `Health` inbound via GetComponent | `Assets/Scripts/{Health,PlayerController}.cs`, `ProjectSettings/` | asmdef / more components |
| `godot` | GDScript | `Enemy.take_hit` inbound (+ HealthBar) | `scripts/{player,enemy,health_bar}.gd`, `addons/vendor_ui/`, `project.godot` | scene `.tscn` |
| `unreal` | C++ / detect | `UHealthComponent` inbound via Cast/CreateDefaultSubobject | `*.uproject`, `Source/MyGame/*.{h,cpp}`, `Config/DefaultEngine.ini` | full UBT / `.uasset` (not indexed) |
| `cpp` | C++ | `Widget::resize` → `draw` | `include/*.h`, `src/*.cpp` (`this->draw`) | denser call edges |
| `swift` | Swift | `Greeter.greet` → `format` | `Greeter.swift` + `FormatHelpers.swift` (protocol/extension) | full Alamofire/etc under `.eval-projects` (not OSS-paired) |
| `elixir` | Elixir | `Demo.Greeter.greet` | `lib/*.ex` + `mix.exs` (alias/import/use/defp) | Phoenix densify under bed `phoenix` |
| `phoenix` | Elixir / Phoenix | `PageController` / `DashboardLive` / route sites | `lib/demo_web/{router,controllers/*,live/*}.ex`, `mix.exs` | full Phoenix apps under `.eval-projects` |
| `dart` | Dart | `Greeter.greet` → `format` (+ mixin/fields/ctors; `shout` parentless) | `lib/{greeter,helpers,tone}.dart` (methods + Helpers.tag) | Flutter densify under bed `flutter` |
| `flutter` | Dart / Flutter | `HomeScreen` / `appRouter` / `route:home` | `lib/{main,screens/*,widgets/*}.dart`, `pubspec.yaml` | full apps under `.eval-projects` |
| `react-native` | TSX / RN | `HomeScreen` / `RootNavigator` / `Stack` | `App.tsx`, `src/{screens,navigation,components}/*` | Expo/RN OSS under `.eval-projects` |
| `zig` | Zig | `Greeter.greet`/`shout` → `format`/`helpers.upper` | `src/{greeter,helpers}.zig` (methods + Tone/Stats/fields) | — |
| `solidity` | Solidity | `Greeter.greet` → `format` | `contracts/{Greeter,Helpers}.sol` (lite regex) | — |
| `clojure` | Clojure | `greet` → `format` | `src/demo/{greeter,helpers}.clj` (lite regex; no tree-sitter) | — |
| `erlang` | Erlang | `greet` → `format` | `src/greeter.erl` + `helpers.hrl` (lite regex; separate from Elixir) | — |
| `fsharp` | F# | `Greeter.Greet` → `format` | `Greeter.fs` + `Helpers.fs` (lite regex; no tree-sitter) | — |
| `r` | R | `greet` → `format` | `greeter.R` + `helpers.R` (lite regex; no tree-sitter) | — |
| `perl` | Perl | `greet` → `format` | `Greeter.pm` + `Helpers.pm` (lite regex; no tree-sitter) | — |
| `ocaml` | OCaml | `Greeter.greet` → `format` | `greeter.ml` + `helpers.ml` (lite regex; paren calls) | — |
| `haskell` | Haskell | `greet` → `format` | `Greeter.hs` + `Helpers.hs` (lite regex; paren calls) | — |
| `shaders` | GLSL / HLSL | `main` → `tonemap` / `frag` → `ApplyFog` | `post.frag` + `common.glsl`, `Water.hlsl` + `Lighting.hlsli` | engine materials under game beds |
| `terraform` | HCL / TF | `aws_instance.web` → `module.vpc` | `main.tf`, `variables.tf`, `outputs.tf`, `modules/vpc/` | small OSS under `.eval-projects` (e.g. learn-terraform) |
| `devops` | Dockerfile / Compose / Make | `web` / `runtime` / `test` | `Dockerfile`, `docker-compose.yml`, `Makefile` | Low/lite locate only — no runtime graph |
| `kubernetes` | Kubernetes YAML | `api-ingress` → `api` | `k8s/{deployment,service,ingress}.yaml` | Low/lite — no cluster runtime / Helm |
| `ansible` | Ansible | `Configure web` → `web` | `playbooks/site.yml`, `roles/web/{tasks,handlers}/main.yml` | Low/lite — no Jinja/facts |
| `powershell` | PowerShell | `Write-Info` → `Deploy-App` | `deploy.ps1` | Low/lite regex — no pipelines/modules |
| `protobuf` | Protobuf / gRPC | `UserService.GetUser` + import | `proto/user.proto`, `proto/common/types.proto` | real buf/grpc OSS under `.eval-projects` |
| `prisma` | Prisma + TS | `listUsers` → `User` / `findMany` | `prisma/schema.prisma`, `src/users.ts` | full apps under `.eval-projects` |
| `typeorm` | TypeORM / TS | `listUsers` → `User` / `Post` | `src/entities/{User,Post}.ts`, `src/users.service.ts` | Nest+TypeORM under `.eval-projects` |
| `drizzle` | Drizzle / TS | `listUsers` → `users`/`User` / `findMany` | `src/db/schema.ts`, `src/users.ts` | full apps under `.eval-projects` |
| `swiftui` | Swift / SwiftUI | `HomeView` → `DetailView` | `Views/{Home,Detail,Greeting}View.swift` | full apps under `.eval-projects` |
| `capacitor` | TSX / Capacitor+Ionic | `AppRoutes` → `HomePage` / `DevicePlugin` | `src/{routes,pages}/*`, `capacitor.config.ts` | Ionic Angular/Vue under `.eval-projects` |
| `lua` | Lua | `greet` → `format` + `require` | `greeter.lua`, `helpers.lua` | — |
| `scala` | Scala | `Greeter.greet` (+ ParentID / LoggedGreeter) | `src/Greeter.scala` + `src/helpers/Format.scala` | — |
| `kotlin` | Kotlin | `OwnerController` → `PetService` | `src/main/kotlin/.../{OwnerController,PetService}.kt`, `build.gradle.kts` | full Ktor/Spring under `.eval-projects` |
| `multi-repo-a` | Go | `InventoryClient` | top-level Go module | wire real multi-root groups |
| `multi-repo-b` | Go | `CheckoutService` | top-level Go module | pair with `multi-repo-a` |
| `svelte` | Svelte | `toggle` / `Page` | `lib/{Toggle,Page}.svelte` | full OSS under `.eval-projects/svelte` |
| `vue` | Vue | `greet` / `open`/`label` (ref/computed) | `src/{Greeter,Button}.vue` | Nuxt apps may add more routes |
| `angular` | TS / Angular | `HeroService` inbound | `src/app/hero/*.{module,component,service}.ts`, `package.json` | full Angular under `.eval-projects` |
| `nextjs` | TSX / Next | App Router (dense) / stub gold `Page`/`GET`/`greet` via `nextjs-starter` | dense: `.eval-projects/nextjs-app-router-playground`; stub fixture `app/{page,layout}.tsx`, `app/api/health/route.ts`, `lib/greet.ts` staged as `nextjs-starter` | `nextjs-hello-world` is live-harness only |
| `nuxt` | TS + Vue / Nuxt | `useCounter` | `composables/useCounter.ts`, `pages/index.vue`, `nuxt.config.ts` | full Nuxt under `.eval-projects` |
| `sveltekit` | Svelte + TS / Kit | `load` → `greet` | `src/routes/+page.server.ts`, `+page.svelte`, `+server.ts`, `src/lib/greet.ts` | full Kit under `.eval-projects` |
| `remix` | TSX / Remix | `loader` / `action` | `app/routes/_index.tsx`, `app/lib/greet.ts` | full Remix under `.eval-projects` |
| `electron` | JS / Electron | `handleGreet` / IPC | `src/main/main.js`, `src/preload/preload.js`, `src/renderer/renderer.js` | full Electron apps under `.eval-projects` |
| `deno` | TS / Deno | `handler` / `greet` | `deno.json`, `main.ts` (`Deno.serve`) | Deploy/JSR graphs not claimed |
| `bun` | TS / Bun | `fetchHandler` / `greet` | `bun.lockb`, `package.json`, `index.ts` (`Bun.serve`) | native APIs lite |
| `cloudflare-worker` | TS / Workers | `fetch` edge_handler | `wrangler.toml`, `src/index.ts` | bindings/KV not modeled |
| `astro` | Astro | `getStaticPaths` / `island:Card` | `src/pages/{Index,Card}.astro` (`client:load`) | content collections not stubbed |
| `mdx` | MDX | `highlight` / `Callout`/`Hint` | `docs/{Intro,Callout,Note}.mdx` | prose/expression graphs not claimed |
| `spring` | Java | `OwnerController` → `PetService` | `src/main/java/.../{OwnerController,PetService}.java`, `pom.xml` | full petclinic under `.eval-projects/spring-petclinic` |
| `hibernate` | Java / JPA | `Owner` → `Pet`/`Account` + `OwnerRepository` | `src/main/java/.../{Owner,Pet,Account,OwnerRepository,OwnerService}.java`, `pom.xml` | full Spring Data JPA apps under `.eval-projects` |
| `fastapi` | Python | `Depends` / `list_users` | `main.py` (FastAPI+APIRouter+Depends+UserService) | full FastAPI under `.eval-projects` |
| `flask` | Python | `Flask` / `list_users` | `app.py` (`class Flask` + `@app.route`) | full Flask under `.eval-projects` |
| `djangorest` | Python | `APIView` / `UserViewSet` | `rest_framework/*` + `app/{views,urls}.py` | full DRF under `.eval-projects` |

**Workspace group fan-out harness** (separate from densify stubs): [../workspace-groups/README.md](../workspace-groups/README.md) + `scripts/prepare-workspace-groups-testbed.sh`.

## OSS hold-outs (not stubbed here)

Use `scripts/prepare-oss-testbeds.sh [OUT]` to shallow-clone/cache under
`.eval-projects/<canonical>` and stage junctions into `CODEHELPER_TESTBEDS`
(default OUT: `.testbeds/active`; plus probe-aligned stubs). Canonical OSS names: `axum`, `fiber`, `gin`,
`express`, `fastapi`, `flask`, `djangorest` (encode/django-rest-framework — **not**
the Django framework tree), `laravel`, `spring-petclinic`. Live extras:
`rails` (gem), `wordpress` (sparse `src/wp-includes` + `wp-admin/includes`).

Svelte/Vue/Astro/MDX paired probes target stub symbols (`Toggle` / `Greeter` /
`getStaticPaths` / `highlight`) — prepare-oss prefers stubs for those bed names even when
full OSS clones exist under `.eval-projects/`.

**Nest staging:** when `.eval-projects/nestjs-sample` is indexed, prepare-oss links it as
`nest` (RealWorld `ArticleService`). The CatsService collision stub is always staged as
`nest-starter` (fixture still lives under `testdata/minimal-testbeds/nest/`). Do not put
`nest` in `STUB_PREFERRED` or dense RealWorld will be overwritten.

**Next.js staging:** when `.eval-projects/nextjs-app-router-playground` is indexed, prepare-oss
links it as `nextjs`. The Page/greet stub is always staged as `nextjs-starter` (fixture under
`testdata/minimal-testbeds/nextjs/`). Do not put `nextjs` in `STUB_PREFERRED` or dense
playground will be overwritten.

Live-harness extras (not paired bed names): `nestjs-typescript-starter`,
`nextjs-hello-world`, `django`, `rails` (full gem — paired bed uses the `rails` **app stub**),
`vue` (OSS), `symfony-demo`, `redis`, `wordpress` (core subset — paired bed uses the plugin/theme stub).

## Conventional paths

```
CODEHELPER_TESTBEDS/          # usually .testbeds/active
  gin/.codehelper/            # indexed bed root (name = directory basename)
  nest/.codehelper/
  …
  multi-repo-a/.codehelper/
  multi-repo-b/.codehelper/

.eval-projects/               # optional full OSS clones (gitignored)
  laravel/
  nestjs-sample/              # RealWorld Nest API → staged as active/nest
  nestjs-typescript-starter/  # live-harness AppService DI; not CatsService gold
  nextjs-app-router-playground/  # dense Next App Router → staged as active/nextjs
  nextjs-hello-world/         # live-harness thin Next; not Page/greet gold
```

Paired: `nest` → ArticleService (dense); `nest-starter` → CatsService stub collisions.
Paired: `nextjs` → dense App Router playground; `nextjs-starter` → Page/greet stub probes.
Paired probes soft-skip any bed missing `.codehelper/`. Language **parsers** are
owned by sibling agents — only add symbols/fixtures here, do not rewrite parsers.
