package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// allCallTargets collects every symref call target name in a parse result.
func allCallTargets(res *ParseResult) map[string]bool {
	out := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls {
			out[symrefName(e.TargetID)] = true
		}
	}
	return out
}

func TestParsePHP_EloquentRelationsReachRelatedModels(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App\Models;

use Illuminate\Database\Eloquent\Model;

class User extends Model
{
    public function orders()
    {
        return $this->hasMany(Order::class);
    }

    public function profile()
    {
        return $this->hasOne(Profile::class);
    }

    public function roles()
    {
        return $this->belongsToMany(Role::class);
    }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Models/User.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	// The relation method reaches the related model.
	var ordersID string
	for _, s := range res.Symbols {
		if s.Name == "orders" && s.Kind == types.SymbolKindMethod {
			ordersID = s.ID
		}
	}
	if ordersID == "" {
		t.Fatalf("missing orders() method; symbols=%#v", res.Symbols)
	}
	if got := callTargetsFrom(res, ordersID); !got["Order"] {
		t.Errorf("User::orders should reach Order; got %#v", got)
	}
	// The owning model reaches every related model, so impact on Order finds User.
	user := symbolByName(res, "User")
	if user == nil {
		t.Fatal("missing User class")
	}
	ownerCalls := callTargetsFrom(res, user.ID)
	for _, want := range []string{"Order", "Profile", "Role"} {
		if !ownerCalls[want] {
			t.Errorf("User model should reach %q via relations; got %#v", want, ownerCalls)
		}
	}
}

func TestParsePHP_ModelStaticQueriesReachModel(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App\Http\Controllers;

use App\Models\User;
use App\Jobs\SendWelcome;

class UserController extends Controller
{
    public function index()
    {
        $users = User::where('active', true)->paginate(20);
        return view('users.index', compact('users'));
    }

    public function store()
    {
        $user = User::create(request()->all());
        SendWelcome::dispatch($user);
        return redirect()->route('users.index');
    }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Http/Controllers/UserController.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	var indexID, storeID string
	for _, s := range res.Symbols {
		if s.Kind != types.SymbolKindMethod {
			continue
		}
		switch s.Name {
		case "index":
			indexID = s.ID
		case "store":
			storeID = s.ID
		}
	}
	if indexID == "" || storeID == "" {
		t.Fatalf("missing index/store; symbols=%#v", res.Symbols)
	}
	indexCalls := callTargetsFrom(res, indexID)
	for _, want := range []string{"User", "User.where", "users.index"} {
		if !indexCalls[want] {
			t.Errorf("index() missing %q; got %#v", want, indexCalls)
		}
	}
	storeCalls := callTargetsFrom(res, storeID)
	for _, want := range []string{"User", "User.create", "SendWelcome", "route_name_users.index"} {
		if !storeCalls[want] {
			t.Errorf("store() missing %q; got %#v", want, storeCalls)
		}
	}
}

func TestParsePHP_RouteNameLinksHelperToAction(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
use App\Http\Controllers\UserController;
use App\Http\Middleware\EnsureUserIsAdmin;

Route::get('/users', [UserController::class, 'index'])
    ->middleware([EnsureUserIsAdmin::class])
    ->name('users.index');

Route::prefix('admin')->name('admin.')->group(function () {
    Route::get('/stats', [UserController::class, 'stats'])->name('stats');
});
`)
	res, err := ParsePHP(context.Background(), "repo", "routes/web.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	nameSym := symbolByName(res, "route_name_users.index")
	if nameSym == nil {
		t.Fatalf("expected route_name_users.index symbol; symbols=%#v", res.Symbols)
	}
	if !strings.Contains(nameSym.Signature, "role=route_name") {
		t.Errorf("route name signature %q missing role=route_name", nameSym.Signature)
	}
	// The route-name symbol points at the route site, which points at the action.
	var routeSiteID string
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "route_get_") {
			routeSiteID = s.ID
			break
		}
	}
	if routeSiteID == "" {
		t.Fatalf("missing route_get_* site; symbols=%#v", res.Symbols)
	}
	linked := false
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == nameSym.ID && e.TargetID == routeSiteID {
			linked = true
		}
	}
	if !linked {
		t.Errorf("route name should point at its route site; edges=%#v", res.Edges)
	}
	// Middleware referenced as ::class on the route chain becomes an edge.
	if got := callTargetsFrom(res, routeSiteID); !got["EnsureUserIsAdmin"] {
		t.Errorf("route missing middleware class edge; got %#v", got)
	}
	// A group name PREFIX ('admin.') is not a route name.
	if symbolByName(res, "route_name_admin.") != nil || symbolByName(res, "route_name_admin") != nil {
		t.Errorf("group name prefix must not become a route name; symbols=%#v", res.Symbols)
	}
}

func TestParsePHP_ConfigClassRefsAndArtisanSignature(t *testing.T) {
	t.Parallel()
	cfg := []byte(`<?php
return [
    'defaults' => ['guard' => 'web'],
    'providers' => [
        'users' => [
            'driver' => 'eloquent',
            'model' => App\Models\User::class,
        ],
    ],
];
`)
	res, err := ParsePHP(context.Background(), "repo", "config/auth.php", cfg)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	if got := allCallTargets(res); !got["User"] {
		t.Errorf("config/auth.php should reference the User model; got %#v", got)
	}
	if symbolByName(res, "laravel_wiring_auth") == nil {
		t.Errorf("expected a per-file wiring site; symbols=%#v", res.Symbols)
	}

	cmd := []byte(`<?php
namespace App\Console\Commands;

use Illuminate\Console\Command;

class SyncInvoices extends Command
{
    protected $signature = 'app:sync-invoices {--force}';

    public function handle(): int
    {
        return 0;
    }
}
`)
	cres, err := ParsePHP(context.Background(), "repo", "app/Console/Commands/SyncInvoices.php", cmd)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	sig := symbolByName(cres, "artisan_app_sync_invoices")
	if sig == nil {
		t.Fatalf("expected artisan_app_sync_invoices entrypoint; symbols=%#v", cres.Symbols)
	}
	if !strings.Contains(sig.Signature, "role=entrypoint") {
		t.Errorf("artisan signature %q missing role=entrypoint", sig.Signature)
	}
	if got := callTargetsFrom(cres, sig.ID); !got["handle"] {
		t.Errorf("artisan command should reach handle(); got %#v", got)
	}
}

// A plain-PHP library file must not be dressed up with Laravel edges.
func TestParsePHP_NonLaravelFileSkipsLaravelDensify(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace Acme\Math;

class Calculator
{
    public function add(int $a, int $b): int
    {
        return Helper::create($a + $b);
    }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "src/Math/Calculator.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	if got := allCallTargets(res); got["Helper.create"] {
		t.Errorf("non-Laravel file should not get model-static densify; got %#v", got)
	}
}

func TestLaravelFrameworkDetectionOnRealLayout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, body string }{
		{"app/Models/User.php", "<?php\nclass User extends Model {}\n"},
		{"app/Http/Middleware/Authenticate.php", "<?php\nclass Authenticate {}\n"},
		{"app/Providers/AppServiceProvider.php", "<?php\nclass AppServiceProvider {}\n"},
		{"database/migrations/2024_01_01_create_users_table.php", "<?php\nreturn new class {};\n"},
		{"resources/views/welcome.blade.php", "<html></html>"},
		{"app/Services/Billing.php", "<?php\nuse Illuminate\\Support\\Facades\\Log;\n"},
	} {
		packs := DetectFrameworkPacks(tc.path, nil, tc.body)
		if !containsFramework(packs, string(FrameworkLaravel)) {
			t.Errorf("expected laravel pack for %s; got %v", tc.path, packs)
		}
	}
	// A non-Laravel PHP file stays untagged.
	if packs := DetectFrameworkPacks("src/Math/Calculator.php", nil, "<?php\nclass Calculator {}\n"); containsFramework(packs, string(FrameworkLaravel)) {
		t.Errorf("plain PHP must not be tagged laravel; got %v", packs)
	}
}

func TestParsePHP_LaravelContainerMakeAndMacros(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App\Http\Controllers;

use App\Services\BillingService;
use App\Contracts\Notifier;
use App\Services\MailNotifier;
use Illuminate\Support\Str;
use Illuminate\Support\Collection;

class CheckoutController extends Controller
{
    public function pay()
    {
        $billing = app()->make(BillingService::class);
        $alt = resolve(BillingService::class);
        $via = app(BillingService::class);
        return $billing->charge();
    }
}

class AppServiceProvider extends ServiceProvider
{
    public function register()
    {
        app()->singleton(Notifier::class, MailNotifier::class);
        Str::macro('slugify', function (string $v) {
            return Str::slug($v);
        });
        Collection::macro('toInvoice', function () {
            return $this;
        });
    }

    public function boot()
    {
        Event::listen(OrderShipped::class, SendShipmentNotification::class);
        Gate::policy(Post::class, PostPolicy::class);
        Schedule::job(PruneOldOrders::class);
        Schedule::command('app:sync-invoices');
        Route::model('user', User::class);
    }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Providers/AppServiceProvider.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	var payID, registerID, bootID string
	for _, s := range res.Symbols {
		if s.Kind != types.SymbolKindMethod {
			continue
		}
		switch s.Name {
		case "pay":
			payID = s.ID
		case "register":
			registerID = s.ID
		case "boot":
			bootID = s.ID
		}
	}
	if payID == "" || registerID == "" || bootID == "" {
		t.Fatalf("missing pay/register/boot; symbols=%#v", res.Symbols)
	}
	payCalls := callTargetsFrom(res, payID)
	for _, want := range []string{"BillingService"} {
		if !payCalls[want] {
			t.Errorf("pay() missing container resolve to %q; got %#v", want, payCalls)
		}
	}
	// Dynamic string keys must stay undensified.
	if payCalls["mailer"] {
		t.Errorf("string container key must not densify; got %#v", payCalls)
	}
	regCalls := callTargetsFrom(res, registerID)
	for _, want := range []string{"Notifier", "MailNotifier", "Str", "Str.slugify", "Collection", "Collection.toInvoice"} {
		if !regCalls[want] {
			t.Errorf("register() missing %q; got %#v", want, regCalls)
		}
	}
	var macroSym bool
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "laravel_macro_str_slugify_") {
			macroSym = true
			if !strings.Contains(s.Signature, "role=macro") {
				t.Errorf("macro signature %q missing role=macro", s.Signature)
			}
		}
	}
	if !macroSym {
		t.Errorf("expected laravel_macro_str_slugify_* symbol; symbols=%#v", res.Symbols)
	}
	bootCalls := callTargetsFrom(res, bootID)
	for _, want := range []string{
		"OrderShipped", "SendShipmentNotification",
		"Post", "PostPolicy",
		"PruneOldOrders",
		"artisan_app_sync_invoices",
		"User",
	} {
		if !bootCalls[want] {
			t.Errorf("boot() missing %q; got %#v", want, bootCalls)
		}
	}
}

func TestParsePHP_LaravelListenMapAndInvokableRoute(t *testing.T) {
	t.Parallel()
	provider := []byte(`<?php
namespace App\Providers;

use App\Events\OrderShipped;
use App\Listeners\SendShipmentNotification;
use Illuminate\Foundation\Support\Providers\EventServiceProvider as ServiceProvider;

class EventServiceProvider extends ServiceProvider
{
    protected $listen = [
        OrderShipped::class => [
            SendShipmentNotification::class,
        ],
    ];
}
`)
	pres, err := ParsePHP(context.Background(), "repo", "app/Providers/EventServiceProvider.php", provider)
	if err != nil {
		t.Fatalf("parse provider: %v", err)
	}
	if got := allCallTargets(pres); !got["OrderShipped"] || !got["SendShipmentNotification"] {
		t.Errorf("$listen map should reach event+listener; got %#v", got)
	}

	routes := []byte(`<?php
use App\Http\Controllers\ShowPost;

Route::get('/posts/{post}', ShowPost::class);
`)
	rres, err := ParsePHP(context.Background(), "repo", "routes/web.php", routes)
	if err != nil {
		t.Fatalf("parse routes: %v", err)
	}
	var routeID string
	for _, s := range rres.Symbols {
		if strings.HasPrefix(s.Name, "route_get_") {
			routeID = s.ID
			break
		}
	}
	if routeID == "" {
		t.Fatalf("missing route_get_*; symbols=%#v", rres.Symbols)
	}
	if got := callTargetsFrom(rres, routeID); !got["ShowPost"] {
		t.Errorf("invokable route should reach ShowPost; got %#v", got)
	}
}

func TestParsePHP_LaravelContainerStringKeySkipped(t *testing.T) {
	t.Parallel()
	src := []byte(`<?php
namespace App\Http\Controllers;
class MailController {
    public function send() {
        $mailer = app()->make('mailer');
        return $mailer;
    }
}
`)
	res, err := ParsePHP(context.Background(), "repo", "app/Http/Controllers/MailController.php", src)
	if err != nil {
		t.Fatalf("parse php: %v", err)
	}
	var sendID string
	for _, s := range res.Symbols {
		if s.Name == "send" && s.Kind == types.SymbolKindMethod {
			sendID = s.ID
		}
	}
	if sendID == "" {
		t.Fatalf("missing send(); symbols=%#v", res.Symbols)
	}
	if got := callTargetsFrom(res, sendID); got["mailer"] {
		t.Errorf("string app()->make key must not densify; got %#v", got)
	}
}
