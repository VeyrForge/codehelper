package parser

// Version is bumped when extraction rules change; indexer triggers full reindex on mismatch.
// v3: Go doc comments captured into Signature for natural-language search.
// v4: GDScript (.gd) symbols indexed via line-based lite extractor.
// v5: Shader/material languages (HLSL/GLSL/ShaderLab/GDShader/Metal/WGSL) indexed.
// v6: Kotlin name siblings + Elixir alias args + GDScript call/import edges.
// v7: Svelte SFC <script> symbols/calls + Rust type-use/implements + Axum route handlers.
// v8: CJS/Express prototype APIs indexed under dotted aliases (app.use, res.send).
// v9: CJS require imports + Express top-level app.* entrypoints; relative-import
//   - same_subtree symref; Laravel bootstrap/FormRequest; Svelte events/runes;
//     Sinatra DSL; GDScript extends/emit; Nest @Catch.
//
// v10: provenance confidence bands; JS/TS instances; Nest/Laravel bindings.
// v11: C/C++ call edges; PHP class extends/implements; Ruby Calls/Imports caps.
// v12: densify residuals — PHP trait/ParentID; Ruby include/extend/<;
//
//	C# base_list + GetComponent; GDScript ParentID; Nest useExisting/inject;
//	Express Application/Router inbound from examples.
//
// v13: PHP use-alias → FQCN leaf + $this/self recv_type; Ruby self.recv_type;
//
//	namespaced FQCN import matching for symref.
//
// v14: Unity FindObject*/GetComponentIn* type inbound; GDScript ParentID from
//
//	file stem when class_name missing; Godot addons/ demoted in ranking.
//
// v15: reserved (parallel densify numbering collision; content in v21+).
//
// v16: Kotlin ParentID + : Base/Iface inheritance + member props; Bash
//
//	function→command calls (builtins/CLIs filtered); SQL INDEX/PROC/TRIGGER
//	+ REFERENCES reads.
//
// v17: Swift/Scala/Lua/Elixir/Dart densify — imports + call edges;
//
//	Lua function_statement fix; Dart kinds/ParentID; Elixir alias/import/use.
//
// v18: reserved (C++/Unreal content landed in v22; keep const monotonic).
//
// v19: Java/Kotlin Spring densification — stereotype roles, ctor/@Autowired DI
//
//	call edges, extends/implements, ParentID, typed Class.method calls.
//
// v20: reserved (overlapped v16 Bash/SQL notes; coalesced, keep monotonic).
//
// v21: Vue/Astro/MDX SFC densify (script/frontmatter/JS islands + markup/events);
//
//	shared sfc.go helpers; Svelte reuses same script merge path.
//
// v22: C++/Unreal densify — in-class method/field decls + base_class inherits;
//
//	UCLASS/UFUNCTION/UPROPERTY/GENERATED_BODY skip; Cast/CreateDefaultSubobject
//	template type reads (extends ParseCpp; no parallel UE parser).
//
// v23: Java typed-call field map excludes method params (avoid BindingResult.m);
//
//	Spring stereotype role from class modifiers only.
//
// v24: post-wave coalesce — single Version source of truth after parallel densify
//
//	bumps (v16–v23); Lua greeter stub locked via function_statement extract.
//
// v25: Shader Lite densify — #include imports + brace-scoped heuristic call edges
//
//	(GLSL/HLSL/gdshader/usf); builtins/type constructors filtered.
//
// v26: Terraform/OpenTofu HCL densify — address-style symbols (resource/data/
//
//	module/var/output/local/provider) + module source imports + traversal
//	reads + function_call edges (fixes broken type/labels field lookup).
//
// v27: Swift/Elixir/Dart lite QA — Swift extension no duplicate class;
//
//	Elixir skip module-level extractCalls (def/alias noise); Dart CRLF trim.
//
// v28: Rails + WordPress densify — routes/before_action/AR assoc; WP
//
//	add_*/do_*/shortcode/register_*_hook → callback call edges (Sinatra
//	no longer steals Rails config/routes.rb).
//
// v29: Zig + Solidity regex lite — fn/struct/enum + @import; contract/
//
//	library/interface + function/event + import paths; heuristic calls.
//
// v30: Deno/Bun/edge lite densify — deno.json + bun.lockb detection packs;
//
//	Deno.serve/Bun.serve → handler calls; Workers/edge fetch role tags.
//
// v31: Protobuf/gRPC densify — rpc_name methods (ParentID=service) + import
//
//	path edges; contracts DiscoverProtobuf + cross-repo service/message/rpc links.
//
// v32: Prisma schema (.prisma models/enums + relation fields) + TS/JS ORM
//
//	client densify (prisma.*.findMany, TypeORM @Entity/@ManyToOne,
//	getRepository/find, Sequelize findAll/hasMany).
//
// v33: Go HTTP densify — Gin/Echo/Fiber/Chi/Beego route registration →
//
//	handler call edges (ALLCAPS + path-gated Get/Handle; beego Router).
//
// v34: Clojure / Erlang / F# regex lite — no tree-sitter in
//
//	smacker/go-tree-sitter; ns/defn, -module/fun heads, module/type/let;
//	Low / lite bands + stub beds (Erlang separate from Elixir).
//
// v35: DevOps Lite — Dockerfile stages (+ COPY --from reads), Compose
//
//	services/networks/volumes (+ depends_on reads), Makefile targets
//	(+ prereq reads). Basename walk; Low/lite — empty fanout ≠ isolation.
//
// v36: Prisma/TypeORM densify index bump — ensure reindex after schema +
//
//	TS ORM client edges (content noted in v32) land in working trees.
//
// v37: ASP.NET Core on C# graph — Controllers/[ApiController]/Http*,
//
//	ctor+[FromServices] DI call edges, Minimal API MapGet/MapPost entrypoints,
//	AddScoped<> registration; frameworks=aspnetcore roles.
//
// v38: Zig + Solidity lite extractors registered (.zig/.sol) — reindex gate
//
//	after parallel bumps; content notes remain under v29.
//
// v39: Indexer SourceExtensions includes .prisma so schema.prisma is walked
//
//	(Prisma densify content in v32/v36).
//
// v40: SourceExtensions + languageFromExt for zig/sol/clj/erl/fs lite beds
//
//	(extractors already registered in v29/v38; walk allowlist was the gap).
//
// v41: Solidity/Godot/Elixir agent densify — Solidity `is` inherits/implements
//
//   - Helpers.fn receiver calls; GDScript extends→inherits (class_name source);
//     Elixir remote Mod.fun + alias-resolved calls + @behaviour/use implements.
//
// v42: Mid-framework densify — Beego InsertFilter + qualified Router types;
//
//	Nuxt defineEventHandler→server_api; Deno/Bun *Handler edge roles + error;
//	Prisma/TypeORM include/relations → model leaves.
//
// v43: Zig/Dart/Scala densify — brace-stack ParentID (no sticky class/file-stem);
//
//	Zig skip @builtin call noise; Dart export/part imports + getter symbols;
//	Scala object/class method ParentID + extends/with inherits/implements.
//
// v44: ORM client usage only mints orm_call_N on real matches (no import-line
//
//	noise); React Native Stack.Screen component={X} → navigator→screen calls.
//
// v45: R / Perl / OCaml / Haskell regex lite — no tree-sitter in
//
//	smacker/go-tree-sitter; function/sub/let/module + imports; paren-call
//	edges; Low / lite bands + stub beds (space-application not modeled).
//
// v46: Vue template/ref/computed edges; Astro client:* island markers
//
//	(role=island); MDX imported + expression component densify — no full
//	reactivity / SSR / MDX expression graphs claimed.
//
// v47: SvelteKit/Remix/Electron densify on Svelte/TS graphs — packs + roles
//
//	(+page load, Remix loader/action, Electron main/preload/renderer IPC).
//
// v48: Reindex gate for Vue/Astro/MDX densify (v46) when working trees may
//
//	have landed at v47 without those extractors.
//
// v49: Ops YAML + PowerShell lite — Kubernetes Deployment/Service/Ingress
//
//	(+ Ingress→Service reads), Ansible playbooks/roles/tasks (+ role reads),
//	PowerShell .ps1/.psm1 functions (+ in-file calls). Path-gated YAML (not
//	all .yml). Honest Low/Lite — empty fanout ≠ isolation.
//
// v50: Drizzle ORM + SwiftUI + Capacitor/Ionic densify — pgTable/relations/
//
//	db.query.*.findMany({ with }); View/NavigationLink; Ion Route→page +
//	registerPlugin (beds drizzle/swiftui/capacitor).
//
// v51: Phoenix (Elixir) + Hibernate/JPA (Java) densify — Router
//
//	get/post/live/resources→Controller/LiveView; Controller/LiveView roles;
//	@Entity relations + JpaRepository<Entity> + @Query FROM + EntityManager
//	find/persist; Spring @GetMapping/@PostMapping entrypoint roles.
//
// v52: Zig/Dart densify — methods (SymbolKindMethod), fields/enum variants,
//
//	type aliases, nested-type ParentID; Module.fn / Class.method receiver
//	calls + reads; Dart named/unnamed ctors + mixin members; stdlib call noise cut.
//
// v53: C++/Unreal + Godot inbound densify — field/local typed receivers
//
//	(HealthComp/Comp→UHealthComponent.ApplyDamage); GDScript class funcs as
//	methods + resolve ParentID functions; HealthBar/connect bed edges.
//
// v54: Symfony / Fastify / Hono densify — PHP #[Route]+ctor DI; TS app.get/post
//
//	entrypoint sites + named/inline handlers (Medium; no plugin/YAML/middleware graph).
//
// v55: TS/JS class+interface heritage (extends/implements → inherits/implements
//
//   - embeds=) densifies Nest/Angular base inbound for CallersOf/impact; C++
//     base embeds= for recv_type promotion; MEDIUM call-graph confidence band.
//
// v56: Nest/Angular ctor/field DI typed calls (this.svc.m → Service.m);
//
//	Laravel Facade::method → Concrete.method; PHP $this->dep->m when field/ctor
//	param types are locally known.
//
// v57: Nest/Next densify — Nest class roles (controller/module/service/middleware),
//
//	HTTP @Get/@Post entrypoint methods, MiddlewareConsumer.apply edges, nested
//	@Module array fields (TypeOrmModule.forFeature); Next middleware.ts,
//	generateMetadata/generateStaticParams, 'use server' server_action roles.
//	Also reindexes Laravel/WP densify adapters when present in the same tree.
//
// v58: Laravel/Blade/WordPress app graph + JS/TS path aliases — dedicated
//
//	*.blade.php extractor (view symbols, @extends/@include/@each, <x-*> and
//	<livewire:*>, @section/@yield) with view()/View::make landing on the same
//	symbol; Eloquent relations + Model::static entries; named-route symbols;
//	::class wiring in routes/config/bootstrap/Providers; Artisan signatures;
//	WP register_rest_route/admin pages/meta boxes/settings/block callbacks,
//	require/include file graph, get_template_part; tsconfig/jsconfig/Vite/
//	Webpack/package.json#imports alias expansion into repo-rooted import edges.
//
// v59: Express router densify — express.Router()/Router() factories, routes on
//
//	Router()-bound receivers, app.use mount identifiers, named middleware
//	chains (Fastify-style); nested call sites (not top-level-only).
//
// v60: Nuxt densify deepen — defineNuxtRouteMiddleware / defineNuxtPlugin sites
//
//   - body calls; Nitro server/routes + server/middleware roles
//     (server_route|server_middleware); broader convention-path tagging.
const Version = 60
