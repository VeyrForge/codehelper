package bench

// MultiBedCoverage documents which methodology-lite paired probes map to which
// indexed OSS beds (or minimal stubs). Kept in-package so `go doc` / BENCHMARK.md
// stay aligned.
//
// One recipe: docs/TESTBEDS.md — scripts/testbeds-all.sh → .testbeds/active + dated report.
// Run: CODEHELPER_TESTBEDS=… scripts/mcp-paired-eval.sh
// Prepare stubs: scripts/ci-prepare-minimal-testbeds.sh [OUT]  (SUITE=ci|extended)
// Prepare OSS:   scripts/prepare-oss-testbeds.sh [OUT]  (.eval-projects cache + stubs)
// OSS pins:      OUT/oss-testbed-pins.json → bench report field oss_testbeds (frozen SHAs)
// CI: job testbeds-paired prepares CI-minimal beds in-job (or uses vars.CODEHELPER_TESTBEDS);
// skip only when vars.CODEHELPER_SKIP_TESTBEDS is set (see .github/workflows/ci.yml).
// Stub sources: testdata/minimal-testbeds/ (see LAYOUT.md there for sibling agents).

// BedTier is a coarse graph-quality expectation for eval sampling
// (mcp-eval-methodology §1.1 hold-out stacks).
type BedTier string

const (
	BedTierStrong BedTier = "strong" // dense call graph (Go/Rust-ish)
	BedTierMedium BedTier = "medium" // framework apps with DI/routes
	BedTierWeak   BedTier = "weak"   // library / sparse inbound
)

// BedSource is how a bed is expected to appear under CODEHELPER_TESTBEDS.
type BedSource string

const (
	BedSourceOSS  BedSource = "oss"  // full hold-out under .eval-projects or pre-indexed tree
	BedSourceStub BedSource = "stub" // testdata/minimal-testbeds/<bed> via prepare script
)

// BedProbe describes one multi-bed coverage slot.
type BedProbe struct {
	Bed    string
	Tier   BedTier
	Kinds  []string  // architecture_qa, fix_bug_orient, feature_orient, …
	Source BedSource // empty = treat as oss (legacy 12-bed rows)
	Notes  string    // optional layout / sibling-agent hint
}

// DefaultMultiBedCoverage is the methodology-lite suite used by
// internal/mcpsvc.TestPairedMCPLiteTestbeds (soft-skips beds that are not indexed).
// Includes the original 12 hold-outs plus stub-capable extensions (C#/Unity/Godot/C++,
// multi-repo pair, PHP/Ruby stubs).
func DefaultMultiBedCoverage() []BedProbe {
	return []BedProbe{
		// --- original 12-bed lite (OSS or CI stubs for gin/nest/express) ---
		{Bed: "axum", Tier: BedTierStrong, Kinds: []string{"architecture_qa"}, Source: BedSourceOSS},
		{Bed: "gin", Tier: BedTierStrong, Kinds: []string{"architecture_qa"}, Source: BedSourceStub},
		{Bed: "fiber", Tier: BedTierStrong, Kinds: []string{"feature_orient"}, Source: BedSourceOSS},
		{Bed: "echo", Tier: BedTierStrong, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Echo stub: e.GET/POST → HealthHandler/ListUsers call edges"},
		{Bed: "chi", Tier: BedTierStrong, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Chi stub: r.Get/Method → HealthHandler/ListUsers call edges"},
		{Bed: "beego", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Beego stub: InsertFilter→AuthFilter + Router→UserController→UserService + Get→HealthHandler"},
		{Bed: "fastapi", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "FastAPI stub: Depends+route→list_users→UserService; swap for full OSS under .eval-projects"},
		{Bed: "flask", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Flask stub: class Flask + @app.route→list_users→UserService"},
		{Bed: "djangorest", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "DRF stub: APIView/ViewSet + router.register→UserViewSet.list→UserService"},
		{Bed: "nest", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Dense RealWorld (nestjs-sample→nest) when staged; paired gold ArticleService. CatsService stub → nest-starter"},
		{Bed: "laravel", Tier: BedTierMedium, Kinds: []string{"feature_orient"}, Source: BedSourceStub,
			Notes: "PHP stub User model; swap for full Laravel under .eval-projects"},
		{Bed: "symfony", Tier: BedTierMedium, Kinds: []string{"feature_orient", "architecture_qa"}, Source: BedSourceStub,
			Notes: "Symfony stub: #[Route]→show + ctor UserService DI; full demo under .eval-projects/symfony-demo"},
		{Bed: "wordpress", Tier: BedTierMedium, Kinds: []string{"feature_orient", "architecture_qa"}, Source: BedSourceStub,
			Notes: "WP plugin+theme stub: add_action/add_filter/do_action→callbacks; real core subset under .eval-projects/wordpress"},
		{Bed: "sinatra", Tier: BedTierMedium, Kinds: []string{"feature_orient"}, Source: BedSourceStub,
			Notes: "Ruby stub Sinatra::Base; swap for full Rails/Sinatra OSS"},
		{Bed: "rails", Tier: BedTierMedium, Kinds: []string{"feature_orient", "architecture_qa"}, Source: BedSourceStub,
			Notes: "Rails app stub: routes→UsersController#show + before_action; full Rails gem under .eval-projects/rails"},
		{Bed: "spring-petclinic", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceOSS,
			Notes: "Full Spring Boot OSS under .eval-projects; stub bed spring when offline"},
		{Bed: "spring", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Spring stub: @RestController→PetService ctor DI + typed pets.greet; probe OwnerController"},
		{Bed: "hibernate", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Hibernate/JPA stub: @Entity Owner→Pet/Account + OwnerRepository @Query/JpaRepository + EntityManager.find"},
		{Bed: "svelte", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Svelte stub Toggle/Page; swap for full OSS under .eval-projects/svelte"},
		{Bed: "express", Tier: BedTierWeak, Kinds: []string{"fix_bug_orient"}, Source: BedSourceStub,
			Notes: "Express stub: lib createApplication + examples/hello vs auth collision"},
		{Bed: "fastify", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Fastify stub: app.get→listUsers/getUser entrypoints; plugin encapsulate graph not claimed"},
		{Bed: "hono", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Hono stub: app.get→listUsers/greet; middleware/jsx graph not claimed"},

		// --- extended stub / lite beds (prepare with SUITE=extended) ---
		{Bed: "vue", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Vue SFC Greeter: script-setup + @click/defineProps + ref/computed template reads"},
		{Bed: "angular", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Angular stub: @NgModule/@Component + HeroService ctor DI on TS graph"},
		{Bed: "nextjs", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Dense playground (nextjs-app-router-playground→nextjs) when staged; Page/greet stub → nextjs-starter"},
		{Bed: "nuxt", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Nuxt stub: composables + pages + middleware + server/api defineEventHandler→healthPayload/listUsers"},
		{Bed: "sveltekit", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "SvelteKit stub: +page.server load→greet/healthPayload + +page.svelte + +server GET"},
		{Bed: "remix", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Remix stub: app/routes loader/action→greet/saveGreeting on TS/React graph"},
		{Bed: "electron", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Electron stub: main ipcMain.handle→handleGreet + preload contextBridge + renderer"},
		{Bed: "deno", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Deno stub: Deno.serve(handler)→route→healthHandler/greetHandler→greet"},
		{Bed: "bun", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Bun stub: Bun.serve({fetch,error})→route→handlers→greet"},
		{Bed: "cloudflare-worker", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Workers stub: wrangler.toml + export default { fetch } edge_handler role"},
		{Bed: "astro", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Astro Index frontmatter getStaticPaths + Card client:load island markers"},
		{Bed: "mdx", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "MDX Intro: JS islands + imported/expression component densify (no prose graph)"},
		{Bed: "csharp", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "ASP.NET Core stub: UsersController→UserService ctor+[FromServices] + MapGet/MapPost; Greeter kept"},
		{Bed: "unity", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Unity-lite Health + GetComponent/FindObjectOfType inbound (no Editor download)"},
		{Bed: "godot", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Godot-lite: Player→Enemy.take_hit + HealthBar inbound; addons/_ready demoted"},
		{Bed: "unreal", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Unreal-lite: UHealthComponent inbound via Cast/CreateDefaultSubobject + typed ApplyDamage; Config soft paths"},
		{Bed: "cpp", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "C++ Widget::resize→this->draw ParentID + typed field/local Class.method edges"},
		{Bed: "swift", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Swift Greeter.greet→format + import edges (lite/heuristic calls)"},
		{Bed: "elixir", Tier: BedTierWeak, Kinds: []string{"feature_orient"}, Source: BedSourceStub,
			Notes: "Elixir Demo.Greeter + alias/def; not High confidence"},
		{Bed: "phoenix", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Phoenix stub: Router get/live/resources→PageController/DashboardLive + LiveView handle_event"},
		{Bed: "dart", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Dart densify: Greeter methods/fields/ctors + Auditable mixin + Helpers.tag receivers"},
		{Bed: "flutter", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Flutter stub: HomeScreen/GreetingCard widgets + GoRouter appRouter→route:home; no SDK download"},
		{Bed: "react-native", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "RN stub: HomeScreen+Greeting + RootNavigator/createNativeStackNavigator; no Metro download"},
		{Bed: "zig", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Zig densify: Greeter methods + Tone/Stats/fields + helpers.upper receivers + @import"},
		{Bed: "solidity", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Solidity lite regex Greeter.greet→format + import paths"},
		{Bed: "clojure", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Clojure lite regex: ns/defn greet→format + :require (no tree-sitter)"},
		{Bed: "erlang", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Erlang lite regex (separate from Elixir): -module/greet→format + -include"},
		{Bed: "fsharp", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "F# lite regex: module/type/let Greet→format + open (no tree-sitter)"},
		{Bed: "r", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "R lite regex: greet←function→format + source/library (no tree-sitter)"},
		{Bed: "perl", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Perl lite regex: package/sub greet→format + use (no tree-sitter)"},
		{Bed: "ocaml", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "OCaml lite regex: module/let Greeter.greet→format + open (paren calls)"},
		{Bed: "haskell", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Haskell lite regex: module/greet→format + import (paren calls; no space-app)"},
		{Bed: "shaders", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "GLSL/HLSL lite: #include + main→tonemap / frag→ApplyFog call edges"},
		{Bed: "terraform", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Terraform/OpenTofu HCL: aws_instance.web reads module.vpc + data.aws_ami.ubuntu + local.name_prefix"},
		{Bed: "devops", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "DevOps lite: Dockerfile stages (runtime←builder), compose web→api→db, Makefile test→build→deps — Low/lite locate only"},
		{Bed: "kubernetes", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Kubernetes YAML lite: Deployment/Service/Ingress api + api-ingress→api reads — Low/lite locate only"},
		{Bed: "ansible", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Ansible lite: playbooks/site Configure web→role web + roles/web tasks — Low/lite locate only"},
		{Bed: "powershell", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "PowerShell lite: Write-Info→Deploy-App→Prepare-Env in-file calls — Low/lite locate only"},
		{Bed: "protobuf", Tier: BedTierWeak, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Protobuf/gRPC stub: UserService.GetUser + import common/types.proto; contracts DiscoverProtobuf"},
		{Bed: "prisma", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Prisma stub: User↔Post↔Comment+Profile + listUsers include→Post/Profile + getUsers controller"},
		{Bed: "typeorm", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "TypeORM stub: @Entity User↔Post↔Comment+Profile + listUsers relations + getUsers controller"},
		{Bed: "drizzle", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Drizzle stub: pgTable users/posts + relations + db.query.users.findMany({ with }) → User/Post"},
		{Bed: "swiftui", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "SwiftUI stub: HomeView→DetailView/GreetingView NavigationLink + DetailScreen role=screen"},
		{Bed: "capacitor", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Capacitor/Ionic stub: AppRoutes→HomePage/SettingsPage + DevicePlugin registerPlugin"},
		{Bed: "lua", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Lua greet→format + require(\"helpers\") imports"},
		{Bed: "scala", Tier: BedTierWeak, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "Scala Greeter.greet ParentID + Format import + LoggedGreeter extends/with"},
		{Bed: "kotlin", Tier: BedTierMedium, Kinds: []string{"architecture_qa", "feature_orient"}, Source: BedSourceStub,
			Notes: "Kotlin Spring stub: @RestController OwnerController→PetService DI + typed pets.greet"},
		{Bed: "multi-repo-a", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "pair with multi-repo-b; two indexed roots under same CODEHELPER_TESTBEDS"},
		{Bed: "multi-repo-b", Tier: BedTierMedium, Kinds: []string{"architecture_qa"}, Source: BedSourceStub,
			Notes: "pair with multi-repo-a; locate CheckoutService across sibling bed"},
	}
}

// CIMinimalBedNames are the beds scripts/ci-prepare-minimal-testbeds.sh builds
// when SUITE=ci (default). Kept small for GitHub-hosted runners.
func CIMinimalBedNames() []string {
	return []string{"gin", "nest", "express"}
}

// StubBedNames returns beds that have testdata/minimal-testbeds/<name> fixtures
// (SUITE=extended prepares all of these).
func StubBedNames() []string {
	var out []string
	for _, b := range DefaultMultiBedCoverage() {
		if b.Source == BedSourceStub {
			out = append(out, b.Bed)
		}
	}
	return out
}
