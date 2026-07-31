package profile

// This file curates framework- and language-specific "gotchas": the things an LLM
// writing code for this stack commonly gets wrong. Surfacing them in the profile
// (and thus project_context) means the agent knows the stack's sharp edges up
// front and doesn't have to discover them by trial, error, and extra tool calls.

// frameworkGotchas maps a detected framework to its pitfalls.
var frameworkGotchas = map[string][]string{
	"wordpress": {
		"Enqueue CSS/JS via wp_enqueue_style/script on the wp_enqueue_scripts hook, and set the $ver argument to filemtime($file) — a static/omitted version means browsers serve a STALE cached asset after you edit it (changes appear to 'not work').",
		"Escape on output (esc_html/esc_attr/esc_url), sanitize on input, verify nonces (wp_verify_nonce) for state changes, and use $wpdb->prepare() for SQL.",
		"Prefix global functions/classes/constants/options to avoid collisions; load the text domain for i18n (__/_e).",
		"Hook densify: add_action/add_filter sites call the callback leaf — query the callback name; do_action fire sites do NOT list all listeners across files.",
	},
	"woocommerce": {
		"Use WooCommerce hooks/CRUD (wc_get_product, $order->get_*) — never write to WC tables directly.",
		"Same asset-versioning rule as WordPress: enqueue with filemtime() as $ver or edits to CSS/JS won't show.",
		"Treat checkout/payment/order code as high-risk: validate, sanitize, and use nonces.",
	},
	"wordpress_plugin": {
		"Guard direct access: start every PHP file with `if (!defined('ABSPATH')) exit;`.",
		"Do setup in register_activation_hook / register_deactivation_hook / uninstall.php — not on every page load. Keep the Plugin Name/Version header current.",
	},
	"wordpress_theme": {
		"Enqueue assets in functions.php on the wp_enqueue_scripts hook with filemtime() as $ver — don't hardcode <link>/<script> in templates.",
		"Follow the template hierarchy (index.php, single.php, page.php, …) and call the required tags (wp_head(), wp_footer(), the_content()).",
	},
	"wordpress_child_theme": {
		"Child theme: enqueue the PARENT stylesheet (get_template_directory_uri().'/style.css') as a dependency, then the child's (get_stylesheet_directory_uri()) — set $ver to filemtime() on both.",
		"A template file placed in the child OVERRIDES the parent's; but functions.php is ADDITIVE (parent's also runs). Use get_stylesheet_directory() for child paths, get_template_directory() for parent paths.",
	},
	"unity": {
		"Unity: use the MonoBehaviour lifecycle (Awake/Start/Update); don't allocate or GetComponent in Update; expose fields with [SerializeField]; scene/prefab changes are data, not code.",
	},
	"unreal": {
		"Unreal: keep UCLASS/UFUNCTION/UPROPERTY/GENERATED_BODY and include *.generated.h; regenerate project files after adding classes; Blueprint↔C++ boundary — edit C++ for logic, Blueprint for data/wiring.",
		"Graph densifies Cast/CreateDefaultSubobject/NewObject<T> type reads and header method decls; engine bases (AActor/ACharacter) are skipped as inherits hubs — query AMyGameCharacter / UHealthComponent with path=Source/… when names collide.",
		"Binary .uasset Blueprints are not indexed; use Config/*.ini soft paths / ActiveClassRedirects and Source/ C++ for locate.",
	},
	"laravel": {
		"Use Eloquent/Query Builder, not raw SQL. Run `php artisan migrate` after model/schema changes; never edit an applied migration.",
		"Config is cached: run `php artisan config:clear` after editing .env. Routes may be cached too (`route:clear`).",
		"Respect mass-assignment ($fillable/$guarded) and validate via Form Requests.",
		"Call graphs are sparse for PHP — do not trust callers/callees for facades or dynamic dispatch; prefer routes/, Models, Form Requests, and `docs` over impact traces.",
	},
	"flask": {
		"Prefer application factories and blueprints; keep config outside the app module; use Flask's test client for route checks.",
		"Use the application/request context correctly; register blueprints; don't share mutable globals across requests.",
		"Decorator-registered routes may not appear as call-graph edges — query the view function name and open the blueprint module.",
	},
	"axum": {
		"Route handlers are often free functions wired via Router::route — query the handler name, not only `Router`.",
		"Extractors (State, Path, Json) are type-driven; prefer reading the handler signature over inventing middleware.",
	},
	"express": {
		"Public APIs are often prototype assigns indexed as dotted aliases (app.use, res.send, exports.Router) under lib/ — prefer lib/ over examples/ and pass path= when ambiguous.",
	},
	"nestjs": {
		"DI edges cover Module providers, ctor/@Inject, useClass/useExisting/useFactory, and inject:[]. Sample monorepos collide on CatsService etc. — pass path=sample/01-… (or the app subtree) on context/impact; same_subtree helps graph resolve but not bare name picks.",
	},
	"fastapi": {
		"Endpoints use Pydantic models for I/O and Depends() for dependency injection; use async def for async I/O paths.",
		"Depends / include_router edges can be sparse — prefer param_functions.Depends and applications.include_router with path=, and use docs for DI patterns.",
	},
	"go": {
		"Methods are stored as bare names (Use, Get, Listen) with recv on the hit — query Type.Method (e.g. App.Use) or pass path=/sym: id when names collide.",
	},
	"node": {
		"Prefer package main/lib entrypoints over examples/ and test/acceptance for orientation; use path= when symbol names collide.",
	},
	"filament": {
		"Filament v3 defines forms/tables in the Resource class; v4+ splits them into separate Schema/Table files (Schemas/XForm.php, Tables/XTable.php) — match the installed major version before editing.",
		"Form field defaults (->default()) apply only on Create pages; Edit pages hydrate from the model. Use ->formatStateUsing() to set a value when the model field is null on Edit.",
		"On a Select/CheckboxList, use EITHER ->relationship() OR ->options() — defining both breaks state.",
		"Standalone Filament forms in a Livewire component need form->fill() in mount(), a ->statePath('data'), and a matching public property — omit any and state silently breaks.",
		"Custom Tailwind classes only work if Tailwind is configured to scan that file (content/source); unseen classes are purged.",
	},
	"livewire": {
		"Component state lives in PUBLIC properties (must be JSON-serializable — no closures/models-with-resources); bind with wire:model. Re-renders run the full render() — keep it cheap.",
		"Use lifecycle hooks (mount, updatedX) not constructors; actions are public methods called via wire:click. Validate in the action, not the template.",
	},
	"inertia": {
		"Inertia is NOT a REST API — pages get props from the controller via Inertia::render(); there's no client fetch. Share global data via HandleInertiaRequests middleware.",
		"Keep the server adapter (inertia-laravel/rails) and client adapter (@inertiajs/react|vue3|svelte) on compatible versions.",
	},
	"symfony": {
		"Clear the cache after config/route changes (`php bin/console cache:clear`). Use the service container / autowiring, not `new` for services.",
		"Routes/config live in config/ (attributes or YAML); doctrine migrations via `make:migration` + `doctrine:migrations:migrate`.",
		"Attribute #[Route] densify is Medium — YAML/XML routes and EventSubscribers are not on the call graph; query Controller actions + path=src/Controller/.",
	},
	"fastify": {
		"Prefer named route handlers (`app.get('/x', listUsers)`) over huge inline lambdas — densify reaches listUsers/getUser from entrypoint sites.",
		"@fastify/* plugins and encapsulate() trees are not modeled — empty impact ≠ safe; query register/plugin files with path=.",
	},
	"hono": {
		"Entry is `new Hono()` + `app.get/post` — densify creates hono_* entrypoint sites with body calls. Middleware chains / JSX are not claimed.",
		"Adapters (Node/Bun/Workers) share handler names — pass path= when collisions appear.",
	},
	"nextjs": {
		"Check App Router (app/) vs Pages Router (pages/) BEFORE editing — they differ fundamentally. In App Router, components are Server Components by default: add 'use client' only for hooks/interactivity, and don't fetch data with useEffect in a server component.",
		"Client-exposed env vars MUST be prefixed NEXT_PUBLIC_. Use the Metadata API (not <head>) in the App Router.",
	},
	"deno": {
		"Permissions are opt-in (`--allow-net`, `--allow-read`, …) — missing flags fail at runtime, not compile time. Prefer `deno.json(c)` tasks over ad-hoc CLI flags.",
		"Imports use URL / `jsr:` / `npm:` specifiers — there is often no node_modules tree; don't invent require()-style paths. Deno.serve → handler densify is lite; empty impact ≠ isolation.",
	},
	"bun": {
		"Prefer `bun.lockb`/`bun.lock` as the install source of truth; `bun test` / `bun run` differ from npm scripts when packageManager is bun.",
		"Bun.serve({ fetch }) densify is lite — query the named handler / fetch export; Node polyfills and native Bun APIs can coexist.",
	},
	"cloudflare_workers": {
		"Entry is usually `export default { fetch }` / ExportedHandler — query `fetch` (role=edge_handler), not Express-style routers. Bindings (KV/R2/D1/env) are runtime-injected.",
		"No Node fs/net by default; check compatibility_date / nodejs_compat. Empty call fanout is common — prefer path= + read_workspace_file.",
	},
	"nuxt": {
		"Auto-imports: components/composables under the conventional dirs are auto-imported — don't add manual imports. Server vs client context matters (useFetch/useAsyncData).",
	},
	"sveltekit": {
		"Data loads in +page.(server).ts load functions, not in components. Server-only code goes in +page.server.ts / $lib/server.",
		"load densifies as role=loader with body call edges — query load / greet, not only +page.svelte.",
	},
	"svelte": {
		"Svelte 5 uses runes ($state/$derived/$effect/$props); only mark a variable $state if it must be reactive. Svelte 4 reactivity uses `let` + `$:`. Match the file's version before editing.",
	},
	"astro": {
		"Components are server-rendered by default; add a client:* directive (client:load/idle/visible) to hydrate an interactive island. Run `astro check` for TS type-checking of .astro files.",
	},
	"angular": {
		"Standalone components vs NgModules differ by version; respect dependency injection and RxJS (unsubscribe). Use the CLI (`ng generate`) for scaffolding.",
	},
	"react": {
		"Follow the Rules of Hooks (call hooks at the top level only); give list items stable keys; list every dependency in useEffect/useMemo/useCallback.",
	},
	"vue": {
		"Vue 3 Composition API (ref/reactive, <script setup>) differs from Options API — match the file's existing style; reactivity is lost when destructuring reactive objects.",
	},
	"django": {
		"Run `python manage.py makemigrations && migrate` after model changes; never edit an applied migration. Respect the settings module split and CSRF on POST.",
	},
	"django-rest-framework": {
		"DRF views inherit APIView/ViewSet; serialize with Serializer/ModelSerializer; prefer routers + viewsets over ad-hoc function views when extending the library.",
		"Permission/authentication classes and parser/renderer settings drive request handling — check DEFAULT_* settings before inventing middleware.",
		"Call graphs for dynamic dispatch can be sparse — orient via APIView/Serializer hubs and rest_framework/ package paths.",
	},
	"rails": {
		"Run `rails db:migrate` after schema changes; use strong params; follow convention-over-configuration (don't fight the file layout).",
		"Routes densify to Controller#action and before_action→filter — query UsersController / show, not only config/routes.rb symbols.",
		"Ruby metaprogramming (concerns, scope, method_missing) stays sparse — empty impact is not isolation proof; prefer path=app/…",
	},
	"sinatra": {
		"Routes are DSL methods (get/post/put/delete) on Sinatra::Base — query the route helper or the Base subclass, not a missing controller layer.",
		"Prefer modular style (subclass Sinatra::Base) over classic for libraries; helpers/extensions live in separate modules.",
		"Ruby require/call graphs are sparse — use imports edges and lib/ paths; don't trust empty impact as 'safe to change'.",
	},
	"remix": {
		"Remix (and @remix-run/*) loaders/actions own server data — mutate via actions, not client fetch to invent REST. Match the package's export paths under packages/.",
		"In monorepos, prefer packages/@remix-run/* source over demos/template for orientation; pass path= when symbol names collide.",
		"loader/action densify as role=loader|action with body calls — query loader / greet under path=app/routes/.",
	},
	"electron": {
		"Keep Node/Electron APIs in the main/preload process; the renderer should use contextBridge over nodeIntegration for security.",
		"ipcMain.handle / contextBridge sites densify to handler leaves — query handleGreet / preload, not only BrowserWindow.",
	},
	"spring-boot": {
		"Prefer constructor injection over field @Autowired; configure via application.properties/yml; beans are singletons by default.",
	},
	"phoenix": {
		"Routes live in the Router pipeline (pipe_through, scope, get/post) — prefer Phoenix.Router hubs over ad-hoc Plug guesses.",
		"Channels use join/handle_in/handle_out; LiveView assigns are the source of truth — avoid inventing REST controllers for LiveView apps.",
		"mix.exs + lib/ define the Elixir stack even when assets/package.json is present for the JS client.",
	},
}

// languageGotchas maps a language id to its pitfalls (used when no framework, or
// as a baseline alongside the framework rules).
var languageGotchas = map[string][]string{
	"go":         {"Handle every returned error explicitly (don't discard with _ unless intentional); run `go vet` + gofmt; pass context.Context through call chains; test with -race for concurrent code.", "Methods are stored as bare names (Use, Get, Listen) with recv on the hit — query Type.Method (e.g. App.Use) or pass path=/sym: id when names collide."},
	"rust":       {"Respect ownership/borrowing and lifetimes; prefer Result over panic!/unwrap in library code; run `cargo clippy` and `cargo check`."},
	"python":     {"Work inside a virtualenv; pin/declare deps; add type hints and run ruff/mypy; avoid mutable default arguments."},
	"php":        {"Escape output and validate input; use prepared statements for SQL; follow PSR-12 + the project's autoloading (composer).", "Call graphs are sparse for PHP — do not trust callers/callees for facades; prefer routes/models/Form Requests + docs."},
	"javascript": {"Prefer package main/lib entrypoints over examples/ and test/acceptance for orientation; use path= when symbol names collide."},
	"gdscript":   {"GDScript is indentation-sensitive; in Godot 4 use @export/@onready/@tool annotations and typed vars; get nodes via $Path or get_node(); connect signals in code or the editor.", "Call graphs come from line-based call extraction — addons/ are demoted vs game scripts; when multiple game `_ready` remain, pass path= or query ClassName._ready (ParentID/recv)."},
	"csharp":     {"Prefer async/await over blocking waits; dispose IDisposable; avoid catching Exception broadly; keep public APIs nullable-aware when nullable reference types are enabled.", "Unity: GetComponent/FindObject*/RequireComponent densify class inbound; Library/ is demoted like vendor noise."},
	"cpp":        {"Unreal projects: UCLASS/UFUNCTION/UPROPERTY/GENERATED_BODY are skipped as symbols; Cast/CreateDefaultSubobject<T> emit type reads. Prefer path=Source/Module/….h for Actor/Component names.", "C++ methods carry class ParentID (headers + out-of-line defs); this->m() emits Class.m typed edges — still prefer path=/Type.Method when free-function names collide."},
	"typescript": {"Enable/respect `strict`; avoid `any` (use `unknown` + narrowing); don't over-annotate what's inferred; prefer type-only imports where the config requires them."},
	"ruby":       {"Call/require graphs can be sparse — prefer imports edges and explicit path=; don't treat empty impact as proof a change is isolated."},
	"java":       {"Prefer constructor injection in Spring; keep packages coherent; watch classpath/resource loading differences between tests and main."},
	"kotlin":     {"Ktor/Kotlin multiplatform: prefer commonMain sources over platform stubs; extension functions are indexed under the function name (not the receiver type)."},
	"elixir":     {"Modules are aliases (Phoenix.Router); defs are def/defp — query the module alias, not a missing class hierarchy."},
}

// libraryGotchas maps a declared dependency (by name) to pitfalls — for tools that
// aren't the project's defining framework but still trip LLMs (ORMs, CSS engines,
// bridges). Scanned against the dependency list, so they apply on top of the
// framework rules.
var libraryGotchas = map[string][]string{
	"tailwindcss":           {"Tailwind only emits classes it finds in scanned files (content/source globs) — dynamically-built class strings get purged. Tailwind v4 changed the import/PostCSS setup (@import \"tailwindcss\"; @tailwindcss/postcss) — check the installed major."},
	"prisma":                {"After editing schema.prisma run `prisma migrate dev` (or `db push`) AND `prisma generate` — the client is generated, not hand-written.", "Graph densify: schema models + prisma.user.findMany edges — query User / listUsers, not only schema.prisma text."},
	"@prisma/client":        {"The Prisma client is generated from schema.prisma; run `prisma generate` after schema changes or types go stale."},
	"typeorm":               {"Entity relations densify @ManyToOne/@OneToMany → related classes; getRepository(User).find edges to User — prefer path=src/entities when names collide."},
	"drizzle-orm":           {"Drizzle migrations: change the schema, then `drizzle-kit generate` + apply — don't hand-edit generated SQL.", "Graph densify: pgTable symbols + db.query.users.findMany / select().from(users) → users/User; relations(users→posts) edges — query listUsers / users, not only schema text."},
	"sequelize":             {"Model.findAll / hasMany densify to model leaves — still prefer path=models/ when User appears in fixtures and services."},
	"@capacitor/core":       {"registerPlugin densifies to role=plugin; with Ionic, Route component={HomePage} wires AppRoutes→pages — prefer path=src/pages when names collide."},
	"@ionic/react":          {"IonPage *Page symbols get role=page; IonRouterOutlet Route densify mirrors React Navigation Screen edges — prefer path=src/routes or src/pages."},
	"@trpc/server":          {"tRPC is end-to-end typed: the client infers types from the server AppRouter — there's no codegen and no manual API types to write."},
	"@inertiajs/react":      {"Inertia: pages receive props from the server controller (Inertia::render), not from client fetch calls."},
	"@inertiajs/vue3":       {"Inertia: pages receive props from the server controller (Inertia::render), not from client fetch calls."},
	"@inertiajs/svelte":     {"Inertia: pages receive props from the server controller (Inertia::render), not from client fetch calls."},
	"livewire/livewire":     {"Livewire state lives in public, JSON-serializable properties bound with wire:model; actions are public methods. The whole render() re-runs on each request."},
	"@tanstack/react-query": {"React Query owns server state — don't duplicate it in useState; mutations should invalidate the relevant query keys."},
	"electron":              {"Keep Node/Electron APIs in the main/preload process; the renderer should use contextBridge over nodeIntegration for security.", "ipcMain.handle densifies to handleGreet leaves — query handleGreet / preload roles."},
	"express":               {"Public APIs are often prototype assigns indexed as dotted aliases (app.use, res.send, exports.Router) under lib/ — prefer lib/ over examples/ and pass path= when ambiguous."},
	"fastify":               {"Route densify is Medium (app.get→handlers); @fastify plugins/encapsulate not on the graph — empty impact ≠ safe."},
	"hono":                  {"Route densify is Medium (app.get→body calls); middleware/JSX/adapters not claimed — pass path= on shared handlers."},
	"symfony":               {"#[Route]+ctor DI densify is Medium; YAML routes/EventSubscribers invisible — prefer path=src/Controller/."},
	"nestjs":                {"DI edges cover Module providers, ctor/@Inject, useClass/useExisting/useFactory, and inject:[]. Sample monorepos collide on CatsService etc. — pass path=sample/01-… (or the app subtree) on context/impact; same_subtree helps graph resolve but not bare name picks."},
	"fastapi":               {"Depends / include_router edges can be sparse — prefer param_functions.Depends and applications.include_router with path=, and use docs for DI patterns."},
	"axum":                  {"Route handlers are often free functions wired via Router::route — query the handler name, not only Router. Prefer reading extractor types on the handler signature."},
	"flask":                 {"Decorator-registered routes may not appear as call-graph edges — query the view function name and open the blueprint module."},
}

// gotchasFor assembles the curated pitfalls for a resolved profile: framework
// rules first (most specific), then dominant-language rules, deduped.
func gotchasFor(p *ProjectProfile) []string {
	var out []string
	seen := map[string]bool{}
	add := func(items []string) {
		for _, s := range items {
			if !seen[s] {
				out = append(out, s)
				seen[s] = true
			}
		}
	}
	// Framework base rules (e.g. p.Framework "wordpress") plus the sub-type rules
	// (e.g. p.ProjectType "wordpress_plugin"/"wordpress_child_theme") — so a WP
	// plugin/theme/child gets both the WordPress base and its own specifics.
	if g, ok := frameworkGotchas[p.Framework]; ok {
		add(g)
	}
	if g, ok := frameworkGotchas[p.ProjectType]; ok {
		add(g)
	}
	if g, ok := languageGotchas[p.PrimaryLanguage]; ok {
		add(g)
	}
	// Dependency-driven library pitfalls (ORMs, CSS engines, bridges) on top.
	for _, d := range p.Dependencies {
		if g, ok := libraryGotchas[d.Name]; ok {
			add(g)
		}
	}
	return out
}
