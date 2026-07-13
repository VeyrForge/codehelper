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
	"laravel": {
		"Use Eloquent/Query Builder, not raw SQL. Run `php artisan migrate` after model/schema changes; never edit an applied migration.",
		"Config is cached: run `php artisan config:clear` after editing .env. Routes may be cached too (`route:clear`).",
		"Respect mass-assignment ($fillable/$guarded) and validate via Form Requests.",
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
	},
	"nextjs": {
		"Check App Router (app/) vs Pages Router (pages/) BEFORE editing — they differ fundamentally. In App Router, components are Server Components by default: add 'use client' only for hooks/interactivity, and don't fetch data with useEffect in a server component.",
		"Client-exposed env vars MUST be prefixed NEXT_PUBLIC_. Use the Metadata API (not <head>) in the App Router.",
	},
	"nuxt": {
		"Auto-imports: components/composables under the conventional dirs are auto-imported — don't add manual imports. Server vs client context matters (useFetch/useAsyncData).",
	},
	"sveltekit": {
		"Data loads in +page.(server).ts load functions, not in components. Server-only code goes in +page.server.ts / $lib/server.",
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
	"flask": {
		"Use the application/request context correctly; register blueprints; don't share mutable globals across requests.",
	},
	"fastapi": {
		"Endpoints use Pydantic models for I/O and Depends() for dependency injection; use async def for async I/O paths.",
	},
	"rails": {
		"Run `rails db:migrate` after schema changes; use strong params; follow convention-over-configuration (don't fight the file layout).",
	},
	"spring-boot": {
		"Prefer constructor injection over field @Autowired; configure via application.properties/yml; beans are singletons by default.",
	},
}

// languageGotchas maps a language id to its pitfalls (used when no framework, or
// as a baseline alongside the framework rules).
var languageGotchas = map[string][]string{
	"go":         {"Handle every returned error explicitly (don't discard with _ unless intentional); run `go vet` + gofmt; pass context.Context through call chains; test with -race for concurrent code."},
	"rust":       {"Respect ownership/borrowing and lifetimes; prefer Result over panic!/unwrap in library code; run `cargo clippy` and `cargo check`."},
	"python":     {"Work inside a virtualenv; pin/declare deps; add type hints and run ruff/mypy; avoid mutable default arguments."},
	"php":        {"Escape output and validate input; use prepared statements for SQL; follow PSR-12 + the project's autoloading (composer)."},
	"gdscript":   {"GDScript is indentation-sensitive; in Godot 4 use @export/@onready/@tool annotations and typed vars; get nodes via $Path or get_node(); connect signals in code or the editor."},
	"csharp":     {"Unity: use the MonoBehaviour lifecycle (Awake/Start/Update); don't allocate or GetComponent in Update; expose fields with [SerializeField]; scene/prefab changes are data, not code."},
	"cpp":        {"Unreal: use UPROPERTY/UFUNCTION reflection macros and the Unreal types (FString/TArray); respect the Blueprint↔C++ boundary; regenerate project files after adding classes."},
	"typescript": {"Enable/respect `strict`; avoid `any` (use `unknown` + narrowing); don't over-annotate what's inferred; prefer type-only imports where the config requires them."},
}

// libraryGotchas maps a declared dependency (by name) to pitfalls — for tools that
// aren't the project's defining framework but still trip LLMs (ORMs, CSS engines,
// bridges). Scanned against the dependency list, so they apply on top of the
// framework rules.
var libraryGotchas = map[string][]string{
	"tailwindcss":           {"Tailwind only emits classes it finds in scanned files (content/source globs) — dynamically-built class strings get purged. Tailwind v4 changed the import/PostCSS setup (@import \"tailwindcss\"; @tailwindcss/postcss) — check the installed major."},
	"prisma":                {"After editing schema.prisma run `prisma migrate dev` (or `db push`) AND `prisma generate` — the client is generated, not hand-written."},
	"@prisma/client":        {"The Prisma client is generated from schema.prisma; run `prisma generate` after schema changes or types go stale."},
	"drizzle-orm":           {"Drizzle migrations: change the schema, then `drizzle-kit generate` + apply — don't hand-edit generated SQL."},
	"@trpc/server":          {"tRPC is end-to-end typed: the client infers types from the server AppRouter — there's no codegen and no manual API types to write."},
	"@inertiajs/react":      {"Inertia: pages receive props from the server controller (Inertia::render), not from client fetch calls."},
	"@inertiajs/vue3":       {"Inertia: pages receive props from the server controller (Inertia::render), not from client fetch calls."},
	"@inertiajs/svelte":     {"Inertia: pages receive props from the server controller (Inertia::render), not from client fetch calls."},
	"livewire/livewire":     {"Livewire state lives in public, JSON-serializable properties bound with wire:model; actions are public methods. The whole render() re-runs on each request."},
	"@tanstack/react-query": {"React Query owns server state — don't duplicate it in useState; mutations should invalidate the relevant query keys."},
	"electron":              {"Keep Node/Electron APIs in the main/preload process; the renderer should use contextBridge over nodeIntegration for security."},
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
