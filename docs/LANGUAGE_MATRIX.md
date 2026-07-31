# Language capability matrix

Honest confidence for Codehelper’s **local** symbol/call graph and related tools. This is not a marketing scoreboard: “high” means you can usually trust callers/impact for agent edits; “lite” means symbols or text structure only.

Confidence bands:

- **High** — symbols + useful call/import edges on typical apps
- **Medium** — symbols solid; call graph sparse or heuristic on dynamic patterns
- **Low / lite** — symbol or structural extraction only; treat empty fanout as unknown, not proof of isolation

Honesty: do not read “symbols High” as “impact High.” Framework rows mark **Ceiling:** when calls stay Medium / Medium–High (Zig/Dart/Flutter/RN/engines/ASP.NET/FastAPI OSS/Laravel/WP/Next/Nuxt/multi-repo). Nest can score High on typed DI apps and still miss opaque providers — read the row ceiling.

| Language / surface | Symbols | Calls / graph | AST query | Notes |
|---|---|---|---|---|
| Go | High | High | Yes | Strongest first-party graph; Gin/Echo/Fiber/Chi/Beego route→handler (+ Beego `InsertFilter`) densify; optional gopls via `lsp` |
| TypeScript / JS / TSX / JSX | High | High | Yes | Production paths ranked above samples/tests; optional tsserver. Nest sample monorepos / Express `examples/` still need `path=` when names collide. **Class/interface `extends`/`implements` → inherits/implements** (+ `embeds=`) densify Nest/Angular base inbound for CallersOf/impact (parser ≥55). **Ctor/field DI typed calls** `this.svc.m()` → `Service.m` when annotations / `@Inject(Svc)` / Angular `inject(Svc)` are local (parser ≥56). **Honesty:** string tokens, `ModuleRef.get`, multi-provide, and cross-file opaque providers stay name-only — not a free High-impact claim. **Fastify/Hono** route densify (`frameworks=fastify\|hono`) on same TS graph — Medium (no plugin/middleware graph). **Path aliases resolved** at index time from `tsconfig`/`jsconfig` (incl. `extends`, comments, trailing commas), Vite/Webpack `resolve.alias`, `package.json` `imports`, and a conventional `src/` root — so `@/lib/db` and `~/components/Button` import edges point at the real file instead of an unresolved module node |
| Python | High | High | Yes | FastAPI/Flask/Django(+DRF) route→view→service + Depends/from-import densify; optional pyright via `lsp`. **FastAPI ceiling:** app trees OK on stubs; full OSS still crowds `Depends` hubs — prefer `get_db` / route handlers + `path=` (not a free High impact claim) |
| Rust | High | High | Yes | |
| Java | High | Medium–High | Yes | Spring stereotype roles + ctor/@Autowired DI; **JPA/Hibernate** `@Entity` relations + `JpaRepository<Entity>` + `@Query` FROM + `EntityManager.find` (`frameworks=jpa`); `@GetMapping`/`@PostMapping` entrypoint roles; extends/implements; ParentID; typed `Type.method` |
| Ruby | Medium–High | Medium–High | Yes | require/calls + Sinatra + Rails routes/`before_action`/AR assoc + `<` inherits + include/extend/prepend (`embeds=`); metaprogramming/`method_missing` still sparse — prefer path= / ParentID |
| PHP | High | Medium–High | Yes | Calls + extends/implements + traits (`embeds=` / promoted methods); Laravel routes/facades (**`Facade::method` → `Concrete.method`** when alias known, parser ≥56; broader facade→concrete map + `bind`/`singleton`/`instance`) + typed `$this->dep->m` / `$dep->m` when property/ctor/**method-param** types local + **Symfony** `#[Route]`+ctor DI (`frameworks=symfony`) + WordPress hooks (add_*/do_*/shortcode→callbacks); **`require`/`include` file graph** with `__DIR__`/`ABSPATH`/`plugin_dir_path()` resolution; FQCN + factory demotion. **Laravel** and **Blade** have their own rows. **Honesty:** dynamic `$fn()` / YAML routes / untyped props / unknown facades / hook cross-file fan-in thinner than Go/TS — prefer path= / Type.Method; empty fanout ≠ isolation |
| C# | High | Medium–High | Yes | Base_list + Unity GetComponent inbound; **ASP.NET Core** Controllers/`[FromServices]`/Minimal `MapGet`/`MapPost` densify (`frameworks=aspnetcore`) |
| Kotlin | High | Medium–High | Yes | Member `ParentID` + `: Base(), IFace` inherits/implements + Spring DI when present; ambiguous bare methods still thinner than Go/TS — prefer path= / ParentID |
| C / C++ | Medium–High | Medium–High | Yes | In-class method/field decls + base inherits (+ `embeds=` for recv_type promotion, parser ≥55); typed field/local receivers (`HealthComp→UHealthComponent.ApplyDamage`); Unreal Cast/CreateDefaultSubobject type reads |
| Swift | Medium | Low–Medium | Yes | Imports + heuristic calls (navigation_expression); method ParentID — not High. Paired bed `swift`: Greeter.greet→format. **SwiftUI** densify (separate row): View/`NavigationLink` on bed `swiftui`. Gaps: typed receivers / cross-file resolution stay name-only; extensions attach methods without a second class symbol |
| Scala | Medium | Low–Medium | Yes | Imports + name-only calls; traits as interfaces; method ParentID on object/class/trait members; `extends`/`with` → inherits/implements. Paired bed `scala`: Greeter.greet→format + LoggedGreeter. Gaps: typed receivers stay name-only. Needs parser_version ≥43 |
| Lua | Medium | Low–Medium | Yes | `function_statement` + `require` imports + `function_call` edges. Paired bed `lua`: greet→format + helpers. Gaps: table/module receivers often name-only; metatables not modeled |
| Svelte | Medium–High | Medium | via script bodies | Markup/style/events/runes; full runtime DOM graph not claimed |
| Vue | Medium–High | Medium | via script bodies | `<script setup>` + TS/JS; `@`/`v-on`; `define*`; **ref/computed bindings + simple template reads** (`{{x}}` / `v-if` / `:bind`). Full reactivity / watchEffect dep nets not claimed. Needs parser_version ≥46 |
| Astro | Medium–High | Medium | via frontmatter/scripts | Frontmatter + `<script>` densified; component tags + style; **`client:*` → `island:Comp` markers** (`role=island`). Full island / SSR runtime graph not claimed. Needs parser_version ≥46 |
| MDX | Medium | Medium | via JS islands | Imports/exports/fenced JS + JSX tags; **imported + `{Comp}` expression component densify** (`role=component`). Markdown prose / full expression graphs not claimed. Needs parser_version ≥46 |
| Blade (`*.blade.php`) | Medium–High | Medium–High | — | Dedicated extractor (parser ≥58; not the PHP grammar, which sees Blade directives as text): dotted view symbol per file (`users.profile`, `role=view`) + `@extends`/`@include`/`@includeIf`/`@includeWhen`/`@each` → target views, `<x-alert>`/`<livewire:counter>` → component view **and** class, `@section`/`@push`/`@prepend` defs with `@yield`/`@stack` uses. PHP-side `view('users.profile')`/`View::make` land on the same symbol, so `impact` on a partial or layout lists dependent views. **Ceiling: Medium–High view/include graph — not a PHP runtime or High-impact claim.** Gaps: dynamic `view($name)` / `@include($var)` deliberately not guessed; `@php` blocks not fully parsed as PHP; Livewire/Alpine client graphs not claimed |
| HTML / CSS / SCSS | Medium | — | — | Selectors / custom properties (not call graph) |
| GDScript | Medium | Medium–High | — | Lite extractor: calls, preload/load, extends→inherits (class_name source when present), ClassName.new, emit, as-cast, %Node; func ParentID from class_name. Paired bed `godot`: Player._ready→Enemy.take_hit. Gaps: scene `.tscn` graph / addon collisions still need path= |
| Bash | Low | Partial | Yes | Functions + helper→helper command calls; builtins/CLIs filtered — empty fanout ≠ isolation |
| SQL | Low | Partial | — | Tables/views/funcs/procs/triggers/indexes; `REFERENCES` → reads; not a query planner |
| Elixir | Medium | Medium | — | Modules/defs + alias/import/require/use + def-body calls; remote `Mod.fun` + alias-resolved `Demo.Format.apply`; `@behaviour`/`use`→implements; ParentID on defs. **Phoenix** densify (separate row / bed `phoenix`): Router `get`/`live`/`resources`→Controller/LiveView. Paired bed `elixir`: Demo.Greeter.greet→Format.apply. Gaps: macros/protocols thin; no AST-query grammar; module do-block not call-scanned (avoids def/alias keyword noise). Needs parser_version ≥51 |
| Dart | Medium | Low–Medium | — | **Ceiling: Low–Medium calls — not High.** Regex lite: class/mixin/enum + method/field/ctor kinds, import/export/part URIs, heuristic calls + Capitalized.method receivers — prefer lexical `query`. Brace-stack ParentID (top-level funcs parentless; methods/getters ParentID=type). Paired bed `dart`: Greeter.greet→format + Auditable mixin + Helpers.tag. Gaps: no tree-sitter; Flutter widget/router densify is a separate Medium row; empty fanout ≠ isolation. Needs parser_version ≥52 |
| Zig | Medium | Low–Medium | — | **Ceiling: Low–Medium calls — not High.** Regex lite: `fn`/`const Name = struct|enum` + fields/variants/aliases, `@import`, heuristic calls + `module.fn` receivers — prefer lexical `query`. Brace-stack ParentID; methods SymbolKindMethod; `@builtin` calls filtered. Paired bed `zig`: Greeter.greet/shout→format/helpers.upper + Tone/Stats. Gaps: no tree-sitter; comptime/generics/`anytype` still thin; empty fanout ≠ isolation. Needs parser_version ≥52 |
| Solidity | Medium | Low–Medium | — | Regex lite: contract/library/interface + function/event/modifier/struct/enum, import paths, `is` inherits/implements, Library.fn receiver calls — prefer lexical `query`. Paired bed `solidity`: Greeter.greet→Helpers.format + IGreeter. Gaps: no tree-sitter; Yul/`using for` not claimed; empty fanout ≠ isolation. Needs parser_version ≥41 |
| Clojure | Low / lite | Low | — | Regex lite (no `clojure` grammar in smacker/go-tree-sitter): `ns`/`defn`/`defmacro`/`defprotocol`, `:require`/`require` imports, heuristic `(call …)` edges — prefer lexical `query`. Paired bed `clojure`: greet→format. Gaps: qualified `ns/name` calls, macros, multimethods, reader conditionals; empty fanout ≠ isolation. Needs parser_version ≥34 |
| Erlang | Low / lite | Low | — | Regex lite (separate from Elixir tree-sitter): `-module`/`-include`, function heads, heuristic calls — prefer lexical `query`. Paired bed `erlang`: greet→format. Gaps: no tree-sitter; behaviours/`gen_server` callbacks, macros, records thin; empty fanout ≠ isolation. Needs parser_version ≥34 |
| F# | Low / lite | Low | — | Regex lite (no `fsharp` grammar in smacker/go-tree-sitter): `module`/`type`/`let`/`member`, `open` imports, paren-call edges only — prefer lexical `query`. Paired bed `fsharp`: Greet→format. Gaps: space-application calls, active patterns, computation expressions; empty fanout ≠ isolation. Needs parser_version ≥34 |
| R | Low / lite | Low | — | Regex lite (no `r` grammar in smacker/go-tree-sitter): `name <- function` / `= function`, `library`/`require`/`source` imports, paren-call edges — prefer lexical `query`. Paired bed `r`: greet→format. Gaps: S3/S4/R6 methods, NSE/`...`, pipe (`%>%`/`|>`) graphs; empty fanout ≠ isolation. Needs parser_version ≥45 |
| Perl | Low / lite | Low | — | Regex lite (no `perl` grammar in smacker/go-tree-sitter): `package`/`sub`, `use`/`require` imports, `&name`/`name()` calls — prefer lexical `query`. Paired bed `perl`: greet→format. Gaps: OO/`bless`, prototypes, Moose/Moo, indirect object syntax; empty fanout ≠ isolation. Needs parser_version ≥45 |
| OCaml | Low / lite | Low | — | Regex lite (no `ocaml` grammar in smacker/go-tree-sitter): `module`/`type`/`let`, `open`/`include` imports, paren-call edges only — prefer lexical `query`. Paired bed `ocaml`: Greeter.greet→format. Gaps: space-application, functors, polymorphic variants, `.mli` signatures thin; empty fanout ≠ isolation. Needs parser_version ≥45 |
| Haskell | Low / lite | Low | — | Regex lite (no `haskell` grammar in smacker/go-tree-sitter): `module`/`import`, `data`/`newtype`/`type`/`class`, lowercase bindings (+ `::` sigs), paren-call edges only — prefer lexical `query`. Paired bed `haskell`: greet→format. Gaps: space-application, operators, Template Haskell, typeclass instances; empty fanout ≠ isolation. Needs parser_version ≥45 |
| HCL / Terraform | Medium | Low–Medium | Some | Address-style symbols (`aws_instance.web`, `module.vpc`, `var.x`, `data.T.n`, `local.x`, `output.y`); module `source` imports; traversal **reads** + `function_call` edges. Impact via calls alone under-counts — prefer reads/query. Paired bed `terraform`. Gaps: `${}` template interp, `for_each`/dynamic blocks, provider schemas |
| Dockerfile / Compose / Makefile | Lite | Low / Partial | — | Basename-routed lite: Dockerfile `FROM … AS` stages + `COPY --from` reads; Compose `services`/`networks`/`volumes` + `depends_on` reads; Makefile targets + prereq reads. **Not** a runtime/orchestration graph — empty fanout ≠ isolation. Prefer lexical `query`. Paired bed `devops`. Gaps: BuildKit mounts, Compose profiles/extends, Make macros/`$(call)` |
| Kubernetes | Low / lite | Low / Partial | — | **Ceiling: Lite locate only — not High.** Deployment/Service/Ingress (+ Ingress→Service reads). Path-gated YAML (not every `.yml`). Empty fanout ≠ isolation. Prefer lexical `query`. Paired bed `kubernetes`. Gaps: no cluster runtime / Helm |
| Ansible | Low / lite | Low / Partial | — | **Ceiling: Lite locate only — not High.** Playbooks/roles/tasks (+ role reads). Path-gated YAML. Empty fanout ≠ isolation. Prefer lexical `query`. Paired bed `ansible`. Gaps: no Jinja/facts |
| PowerShell | Low / lite | Low / Partial | — | **Ceiling: Lite locate only — not High.** In-file function calls (regex lite). Empty fanout ≠ isolation. Prefer lexical `query`. Paired bed `powershell`. Gaps: no pipelines/modules |
| GLSL / HLSL / shaders | Lite | Low–Medium | — | `#include` imports + brace-scoped heuristic calls (builtins filtered); prefer path=; empty fanout ≠ isolation. Paired bed `shaders`: main→tonemap / frag→ApplyFog |
| Godot / Unity / Unreal (project detect) | Detected | Medium (engine-lite) | — | **Ceiling: Medium inbound densify — not High.** GDScript class funcs as methods + extends/cross-script calls (`take_hit` / HealthBar inbound); C# GetComponent inbound; Unreal `.uproject` + UE C++ Cast/CreateDefaultSubobject + typed ApplyDamage (not a separate parser). `.uasset` Blueprint binaries not indexed (Config soft paths lite); addon/`_ready` collisions still need path=. Paired beds `godot` / `unity` / `unreal` |
| NestJS (framework) | High | High | via TS | Module DI (providers/controllers/imports/exports, `useClass`/`useExisting`/`inject`, nested `TypeOrmModule.forFeature`). **Ctor/field DI method calls** (`this.svc.m` → `Service.m`) when types or `@Inject(Svc)` are local (parser ≥56). **Roles** (parser ≥57): `role=module|controller|service|middleware` + HTTP `@Get`/`@Post`/… methods as `role=entrypoint`; `MiddlewareConsumer.apply(AuthMiddleware)` + `@UseGuards`/`@UseInterceptors`/`@UsePipes`/`@UseFilters`/`@Catch` call edges when types are local. **App trees** (e.g. typescript-starter): production `src/` wins. **Sample monorepos** (`sample/01-*` + siblings): canonical `sample/01-…` among fixtures; still pass `path=sample/01-…` when editing a non-canonical sample. Full `@nestjs/*` framework monorepo not a paired hold-out — do not treat starter as monorepo proof. **Ceiling:** High on typed DI apps only — string tokens / dynamic `ModuleRef.get` / cross-package opaque providers / guard factories stay name-only; empty fanout ≠ isolation |
| Angular (framework) | High | Medium–High | via TS | `@NgModule`/`@Component`/`@Injectable` + ctor DI call edges on the TS graph (not a separate parser); **typed `this.svc.m` → `Service.m`** when ctor/field types or `inject(Svc)` are local (parser ≥56). Template expression / RxJS subscribe graphs not claimed — prefer `path=` on Hero* name collisions. **Ceiling:** Medium–High — string inject tokens / multi-provide / template-only calls stay unresolved |
| Next.js (framework) | High | Medium–High | via TS/TSX | App Router `app/**/{page,layout,route}.*` + Pages `pages/` tagged `role=page|layout|route_handler`. **Additive roles** (parser ≥57): root/`src` `middleware.ts` → `role=middleware`; `generateMetadata`/`generateViewport`/`generateStaticParams` → `role=metadata|static_params`; `'use server'` file exports → `role=server_action` (dynamic action names not guessed). TS path-alias import edges help `@/`/`~/` (see TS row). **Ceiling: Medium–High calls — not High impact.** Role tags densify locate/entrypoints; Server/Client Component runtime, RSC payload, and middleware matcher graphs are not claimed — empty fanout ≠ isolation |
| Nuxt (framework) | Medium–High | Medium–High | via TS + Vue | `composables/use*` + `#imports`/`defineNuxt*` + `server/api` `defineEventHandler`/`eventHandler` → body calls (`role=server_api`); page SFCs via Vue; TS path-alias import edges help `@/` when configured (see TS row). **Ceiling: Medium–High — not High.** Auto-import / `#imports` resolution is conventional; full Nitro runtime, middleware chains, and Vue reactivity nets are not claimed — empty fanout ≠ isolation |
| SvelteKit (framework) | Medium–High | Medium–High | via Svelte + TS | `+page`/`+layout`/`+server` packs; `load` → `role=loader` + body calls; `+page.svelte` tagged `sveltekit`+`role=page`. Form `actions` / `$app` runtime graph not claimed |
| Remix (framework) | Medium–High | Medium–High | via TS/TSX | `app/routes` + `@remix-run/*`; `loader`/`action`/`clientLoader` roles + body calls. Nested route modules / deferred data graph not claimed — prefer `path=app/routes/` |
| Electron (framework) | Medium | Medium | via JS/TS | `main`/`preload`/`renderer` roles; `ipcMain.handle`/`contextBridge.exposeInMainWorld` → handler/channel edges. Full BrowserWindow lifecycle / native addon graph not claimed |
| Deno (runtime) | Medium | Medium | via TS/JS | `deno.json(c)` project detect; `Deno.serve(handler)` → route → `*Handler` (`role=edge_handler`) → leaf. JSR/npm: URL imports tagged. Permissions / Deploy isolate graph not claimed — empty fanout ≠ isolation |
| Bun (runtime) | Medium | Medium | via TS/JS | `bun.lockb`/`bun.lock`/`bunfig.toml` detect; `Bun.serve({ fetch, error })` → named handlers densify. Native Bun APIs beyond serve are lite; Next+bun.lockb still resolves Next |
| Cloudflare Workers / edge | Medium | Low–Medium | via TS/JS | `wrangler.toml` + `export default { fetch }` / `runtime = "edge"` → `role=edge_handler` (+ `edge` pack). Bindings (KV/R2/D1), Durable Objects, queues not modeled |
| Express (framework) | High | High | via JS | Real OSS (`expressjs/express`): MCP demote prefers `lib/` (`app.use`→`lib/application.js`, `createApplication` tops). `examples/*` still collide on bare route names — use `path=lib/` or `path=examples/hello*`. Weak paired tier (library inbound sparse) |
| Fastify (framework) | Medium | Medium | via TS/JS | `fastify()`/`Fastify()` + `app.get/post` → entrypoint sites + named/inline handlers (`frameworks=fastify`). Paired bed `fastify`. Gaps: `@fastify/*` plugins, encapsulate trees, schema validation graph not claimed — empty fanout ≠ safe |
| Hono (framework) | Medium | Medium | via TS/JS | `new Hono()` + `app.get/post` → entrypoint sites + body calls (`frameworks=hono`). Paired bed `hono`. Gaps: middleware chains, JSX/`c.render`, adapters (Node/Bun/Workers) not modeled — prefer `path=` on shared handler names |
| Prisma (ORM) | Medium–High | Medium–High | via `.prisma` + TS | Schema models/enums + relation-field edges; client `prisma.user.findMany` → User/findMany + `include`/`select` keys → related models. Paired bed `prisma`. Gaps: `$queryRaw`/middleware/extensions not modeled; generated client tree demoted like vendor |
| TypeORM / Sequelize (ORM) | Medium–High | Medium–High | via TS | `@Entity` + `@ManyToOne`/`@OneToMany`/… → related models; `getRepository(User).find` + `relations: […]` → model leaves / `User.findAll`/`hasMany`. Paired bed `typeorm`. Gaps: QueryBuilder chains, DataSource config, migration graphs not claimed |
| Drizzle (ORM) | Medium–High | Medium–High | via TS | `pgTable`/`sqliteTable`/`mysqlTable` → `role=table`; `relations(users→posts)`; `db.query.users.findMany({ with })` + `db.insert`/`select().from` → users/User + related. Paired bed `drizzle`. Gaps: SQL template/`$with` CTE graphs, drizzle-kit migrations not claimed |
| SwiftUI (framework) | Medium | Medium | via Swift | `struct X: View` → `frameworks=swiftui;role=view` (`*Screen` → screen); NavigationLink destination + `FooView()` in body → enclosing view calls. Paired bed `swiftui`. Gaps: `@State`/`@Binding`/preference keys / full destination type resolution stay name-only |
| Capacitor / Ionic (framework) | Medium–High | Medium–High | via TS/TSX | `registerPlugin` → `role=plugin`; Ionic `*Page` + `AppRoutes`/`IonRouterOutlet` Route `component={Page}` → router→page calls. Paired bed `capacitor`. Gaps: native bridge / Capacitor listener graphs, Angular/Vue Ionic routers thinner than React |
| Flutter (framework) | Medium | Medium | via Dart | Widget/`*Screen` roles + GoRouter `appRouter`→`route:home` densify on Dart graph. **Ceiling: Medium — not High** (no SDK download; no full widget-tree / BuildContext graph). Paired bed `flutter` |
| React Native (framework) | Medium | Medium | via TS/TSX | `HomeScreen`/`Greeting` + `RootNavigator`/`createNativeStackNavigator` + `Stack.Screen component={X}` → navigator→screen. **Ceiling: Medium — not High** (no Metro; native bridge / navigation state graphs not claimed). Paired bed `react-native` |
| Multi-repo pair (stubs) | Medium (via Go) | Medium | via Go | Beds `multi-repo-a`/`multi-repo-b`: locate across sibling roots under one `CODEHELPER_TESTBEDS`. **Ceiling: Medium** — group fan-out UX still immature; prefer `group=` / `path=` (see WORKSPACE_GROUPS). Not a High cross-repo call graph |
| Spring (framework) | High | Medium–High | via Java/Kotlin | Stereotype roles + ctor/@Autowired DI; `@GetMapping`/`@PostMapping` entrypoint roles; prefer `OwnerController`/`*Service` over SQL schema name collisions |
| Hibernate / JPA (ORM) | Medium–High | Medium–High | via Java | `@Entity` + `@ManyToOne`/`@OneToMany`/… → related types; `JpaRepository<Owner,Id>` + `@Query("… FROM Owner")` + `EntityManager.find(Owner.class)`. Paired bed `hibernate`. Gaps: CriteriaBuilder / Session API / second-level cache / migration graphs not claimed |
| Phoenix (framework) | Medium–High | Medium | via Elixir | Router `get`/`post`/`live`/`resources` → Controller/LiveView leaves; `plug`/`pipe_through` filters; Controller/LiveView module + action roles (`frameworks=phoenix`). Paired bed `phoenix`. Gaps: HEEx/`~H` AST, channels/Presence, and verified routes macros thin — empty fanout ≠ safe |
| ASP.NET Core (framework) | High | Medium–High | via C# | Controllers (`[ApiController]`/`[Http*]`), ctor + `[FromServices]` DI call edges, Minimal API `MapGet`/`MapPost` entrypoints + `AddScoped<>`. **Ceiling: Medium–High calls — not High impact.** No full middleware pipeline / filter-attributes graph; real OSS bed not cloned — stub `csharp` is the paired hold-out. Empty impact ≠ safe |
| Rails (framework) | Medium–High | Medium | via Ruby | App stub densifies `config/routes` → `UsersController#action`, `before_action` → filter, AR `has_many`/`belongs_to` → model leaf; `include`/`extend`/`prepend` → embeds. Ceiling: no `method_missing`/`scope` graph; concern method bodies still name-only unless `self` typed; full Rails gem bed is library-shaped (prefer app paths). Empty impact ≠ safe |
| Laravel (framework) | High | Medium–High | via PHP / Blade | Routes → controller actions + `::class` refs on the route line (middleware, form requests); **named routes** get a `route_name_users.index` symbol so `route('users.index')` reaches the definition site; **Eloquent** `hasMany`/`belongsTo`/`belongsToMany`/… → related model, `Model::where()`/`Job::dispatch()`/`Event::dispatch()` static entries → the class; `view()`/`View::make()` → Blade view symbols (see Blade row); `::class` in `routes/`, `config/`, `bootstrap/`, `app/Providers/` wires providers/listeners/commands; Artisan `$signature` → entrypoint (parser ≥58 densify adapters). **Ceiling: Medium–High calls — not High impact.** Container `app()->make('string')`, macros, dynamic `$model->{$rel}`, and Blade `@php` bodies stay thin — empty fanout ≠ isolation |
| WordPress (framework) | Medium–High | Medium–High | via PHP | Plugin/theme: `add_action`/`add_filter`/`add_shortcode`/`register_*_hook` sites call callbacks (multi-line calls joined); `do_action`/`apply_filters` fire sites. Also **`register_rest_route`** → `callback`/`permission_callback` (incl. `[$this,'m']` arrays) as entrypoints, admin `add_menu_page`/`add_submenu_page`/`add_options_page`/`add_meta_box` positional callbacks, `register_setting`/`register_block_type` keyed callbacks, the **`require`/`include` file graph** (`__DIR__`, `plugin_dir_path()`, `ABSPATH`, `get_template_directory()`), and `get_template_part`/`locate_template`/`get_header` → template files (parser ≥58). **Ceiling: Medium–High — not High.** No cross-file hook registry (fire↛all listeners); closures/`__invoke` thin; prefer `path=wp-content/…` — empty fanout ≠ isolation |
| Symfony (framework) | Medium | Medium | via PHP | `#[Route]` entrypoint sites + action role; ctor/promoted DI → service leaves (`frameworks=symfony`). Paired bed `symfony`. Gaps: YAML/XML routes, EventSubscriber, full DI container graph not claimed — empty fanout ≠ safe. Prefer `path=src/Controller/` |

## Optional LSP (when installed)

Use the MCP `lsp` tool for **compiler-grade** hover / definition / references / rename / implementations at a `path`+`line` when gopls, typescript-language-server, or pyright is on PATH (or project `node_modules`). Disable with `CODEHELPER_LSP=0`.

| Situation | Source |
|---|---|
| Server binary present and request succeeds | `source=lsp` (+ `graph_symbols` when indexed) |
| Missing binary, disabled, or empty result | `source=graph-fallback` — use `context` / `query` / `trace` / `rename_symbol` / `find_implementations` |

`rename_symbol` and `find_implementations` stay **graph-first**; they merge LSP refs/impls when available (`source=graph+lsp`) and never require a language server.

Default navigation remains `query` → `context` → `trace` / `impact`. Do not treat LSP as the primary search path.

## Call-edge provenance

Resolved call edges and impact nodes carry a confidence float plus a provenance band when available:

| Band | Typical meaning |
|---|---|
| `exact` | Import- or receiver-typed resolution |
| `scoped` | Same file/dir/subtree / public API / embedded |
| `name_only` | Unique or non-fixture name match |
| `inferred` | Heuristic / unresolved symref |

`context` callees expose `conf` + `provenance`; `impact` nodes stamp `provenance` from edge confidence. On sparse stacks, also read `call_graph_confidence` (`LOW` / `MEDIUM` bands — empty fanout ≠ isolation either way).

## Optional embeddings (local tiny path)

Lexical retrieval is always on (BM25 + trigram + graph expand). Semantic rerank
engages when a local OpenAI-compatible embedder is available:

1. Explicit `CODEHELPER_EMBED_URL`, or
2. `~/.codehelper/green.json` (embed-only profile from `codehelper green init-embed`
   / `scripts/install-local-embed.sh`), or
3. Auto-probe of `http://127.0.0.1:8766` (Green `ge embed serve --mcp`).

Codehelper does **not** bundle weights and does **not** download models in CI.
First local serve pulls ~195 MB (Granite multilingual). See [LOCAL_EMBED.md](LOCAL_EMBED.md).

## Contract discovery (OpenAPI / GraphQL / events)

Separate from the language symbol/call matrix. Used by `codehelper projects contracts`
and cross-repo `--cross` / `--group` linking — a locate hook for shared API keys, **not**
part of the indexed call graph.

| Surface | Confidence | Notes |
|---|---|---|
| OpenAPI / Swagger | Lite / scaffold | Candidate paths + shallow dir scan (`openapi*`, `swagger*` JSON/YAML). Path lists for linking. YAML-lite (no full parser). Not a validator. Runtime-only `/openapi.json` (common in FastAPI) is invisible unless a file is on disk. |
| GraphQL SDL (`.graphql` / `.gql`) | Lite / scaffold | Common paths + shallow dirs; type names + Query/Mutation/Subscription fields. Not a full SDL AST / federation merger. |
| Events (AsyncAPI / CloudEvents / name lists) | Lite / scaffold | Same shallow-discovery model; channel ↔ event cross-kind links when keys match. |
| Protobuf / gRPC (`.proto`) | Lite / scaffold | Candidate paths + shallow `proto/`/`api/` dirs (one nested level); services/messages/RPCs. Not protoc. |

Tracked stubs: `testdata/contracts/` (api + web share `/orders` and `Order` for link smoke).
Real OSS under `.testbeds/active` often has **zero** on-disk specs — empty discovery there
is expected, not a regression.

## How to read agent results

1. Check `call_graph_confidence` / doctor warnings when a stack is dynamic (PHP, Ruby, C/C++).
2. `0 callers` on a sparse graph is **not** isolation proof — see sparse-graph honesty notes in release changelogs.
3. Prefer `context` + `impact` on High-band languages; on Lite bands, verify with `read_workspace_file` / tests.
4. Framework gotchas (WordPress, Laravel, Next.js, …) ship in `project_context` — use them before inventing patterns.
5. Prefer graph tools first; use `lsp` only when you need type-precise locations at a known file:line.
6. Contract discovery is Lite/scaffold — use `projects contracts` to locate keys; do not treat empty OSS beds or missing runtime OpenAPI as proof the API does not exist.

## Methodology placeholder

Quantitative per-language F1 / Recall figures belong in [BENCHMARK_COMPARISON.md](BENCHMARK_COMPARISON.md) (methodology) only when measured on named corpora. This matrix is qualitative until those cells are filled.
