package indexer

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/VeyrForge/codehelper/internal/graph"
	"github.com/VeyrForge/codehelper/internal/paths"
)

// initGitRepo stages and commits everything under dir so the indexer's
// commit-gated freshness logic runs a normal full index.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "app")
}

// resolvedCallers returns the resolved (non-symref) `calls` edges pointing at a
// symbol named name, as caller "path:name" pairs — what `impact` reports.
func resolvedCallers(t *testing.T, dir, repoID, name string) []string {
	t.Helper()
	st, err := graph.Open(paths.DBPath(dir))
	if err != nil {
		t.Fatalf("graph.Open: %v", err)
	}
	defer st.Close()
	rows, err := st.DB().QueryContext(context.Background(), `
		SELECT src.path || ':' || src.name
		FROM edges e
		JOIN symbols dst ON dst.id = e.dst_id AND dst.repo_id = e.repo_id
		JOIN symbols src ON src.id = e.src_id AND src.repo_id = e.repo_id
		WHERE e.repo_id=? AND e.kind='calls' AND dst.name=?`, repoID, name)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func hasCallerIn(callers []string, pathFragment string) bool {
	for _, c := range callers {
		if len(pathFragment) > 0 && indexOf(c, pathFragment) >= 0 {
			return true
		}
	}
	return false
}

// countImportEdges counts file-level imports edges whose src/dst ids contain the
// given path fragments (matches the mod:/file: ids Persist uses).
func countImportEdges(t *testing.T, dir, repoID, srcFrag, dstFrag string) int {
	t.Helper()
	st, err := graph.Open(paths.DBPath(dir))
	if err != nil {
		t.Fatalf("graph.Open: %v", err)
	}
	defer st.Close()
	var n int
	if err := st.DB().QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM edges
		WHERE repo_id=? AND kind='imports'
		  AND src_id LIKE ?
		  AND dst_id LIKE ?`,
		repoID, "%"+srcFrag+"%", "%"+dstFrag+"%").Scan(&n); err != nil {
		t.Fatalf("import query: %v", err)
	}
	return n
}

// TestIndexLaravelAppLayout indexes a realistic Laravel app and asserts the
// graph an agent actually needs: the controller reaches the model and the view,
// the model reaches its related model, and the shared Blade layout has dependents.
func TestIndexLaravelAppLayout(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.json", `{"require":{"laravel/framework":"^11.0"}}`)
	writeFile(t, dir, "artisan", "#!/usr/bin/env php\n<?php\n")
	writeFile(t, dir, "app/Models/User.php", `<?php
namespace App\Models;

use Illuminate\Database\Eloquent\Model;

class User extends Model
{
    public function orders()
    {
        return $this->hasMany(Order::class);
    }
}
`)
	writeFile(t, dir, "app/Models/Order.php", `<?php
namespace App\Models;

use Illuminate\Database\Eloquent\Model;

class Order extends Model
{
    public function total(): int { return 0; }
}
`)
	writeFile(t, dir, "app/Http/Controllers/UserController.php", `<?php
namespace App\Http\Controllers;

use App\Models\User;

class UserController extends Controller
{
    public function index()
    {
        $users = User::where('active', true)->get();
        return view('users.index', compact('users'));
    }
}
`)
	writeFile(t, dir, "routes/web.php", `<?php
use App\Http\Controllers\UserController;

Route::get('/users', [UserController::class, 'index'])->name('users.index');
`)
	writeFile(t, dir, "resources/views/layouts/app.blade.php", `<html><body>@yield('content')</body></html>
`)
	writeFile(t, dir, "resources/views/users/index.blade.php", `@extends('layouts.app')

@section('content')
    @include('users.partials.row')
    <a href="{{ route('users.index') }}">Refresh</a>
@endsection
`)
	writeFile(t, dir, "resources/views/users/partials/row.blade.php", `<tr><td>{{ $user->name }}</td></tr>
`)
	// Vite @ alias → resources/js so the full index expands alias imports.
	writeFile(t, dir, "vite.config.js", `import { defineConfig } from "vite";
export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./resources/js"),
    },
  },
});
`)
	writeFile(t, dir, "resources/js/lib/api.js", `export function fetchUsers() { return []; }
`)
	writeFile(t, dir, "resources/js/app.js", `import { fetchUsers } from "@/lib/api";
fetchUsers();
`)
	initGitRepo(t, dir)

	if err := Run(context.Background(), dir, Options{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	repoID := mustRepoID(t, dir)

	// The controller's model query makes it a caller of the User model.
	if c := resolvedCallers(t, dir, repoID, "User"); !hasCallerIn(c, "UserController.php") {
		t.Errorf("User model should have UserController as a caller; got %v", c)
	}
	// The Eloquent relation makes User a caller of Order.
	if c := resolvedCallers(t, dir, repoID, "Order"); !hasCallerIn(c, "app/Models/User.php") {
		t.Errorf("Order should have User (hasMany) as a caller; got %v", c)
	}
	// The controller's view() call reaches the Blade view.
	if c := resolvedCallers(t, dir, repoID, "users.index"); !hasCallerIn(c, "UserController.php") {
		t.Errorf("Blade view users.index should have UserController as a caller; got %v", c)
	}
	// The shared layout has dependents — the whole point of impact on a layout.
	if c := resolvedCallers(t, dir, repoID, "layouts.app"); !hasCallerIn(c, "users/index.blade.php") {
		t.Errorf("layout should have the page that @extends it as a caller; got %v", c)
	}
	// A partial has dependents too.
	if c := resolvedCallers(t, dir, repoID, "users.partials.row"); !hasCallerIn(c, "users/index.blade.php") {
		t.Errorf("partial should have its includer as a caller; got %v", c)
	}
	// route('users.index') from the Blade template reaches the named route.
	if c := resolvedCallers(t, dir, repoID, "route_name_users.index"); len(c) == 0 {
		t.Errorf("route name should have callers (blade + controller); got %v", c)
	}
	// Blade @extends/@include also emit file-level imports to the .blade.php paths.
	if n := countImportEdges(t, dir, repoID, "users/index.blade.php", "resources/views/layouts/app.blade.php"); n == 0 {
		t.Error("@extends should create a file-level import edge to the layout blade file")
	}
	if n := countImportEdges(t, dir, repoID, "users/index.blade.php", "resources/views/users/partials/row.blade.php"); n == 0 {
		t.Error("@include should create a file-level import edge to the partial blade file")
	}
	// Alias import is kept, and ExpandAliasImportEdges adds the repo-relative target.
	if n := countImportEdges(t, dir, repoID, "resources/js/app.js", "@/lib/api"); n == 0 {
		t.Error("raw Vite alias import edge (@/lib/api) should be preserved")
	}
	if n := countImportEdges(t, dir, repoID, "resources/js/app.js", "resources/js/lib/api"); n == 0 {
		t.Error("alias expansion should add an imports edge to resources/js/lib/api")
	}
}

// TestIndexWordPressPluginLayout indexes a realistic WordPress plugin (+ theme
// template) and asserts the include graph, template-part imports, and the
// REST/admin callback graph resolve.
func TestIndexWordPressPluginLayout(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "probe.php", `<?php
/**
 * Plugin Name: Probe
 * Version: 1.2.0
 */
require_once __DIR__ . '/includes/class-probe-rest.php';

add_action( 'rest_api_init', array( 'Probe_REST', 'register_routes' ) );
`)
	writeFile(t, dir, "includes/class-probe-rest.php", `<?php

class Probe_REST
{
    public static function register_routes()
    {
        register_rest_route(
            'probe/v1',
            '/things',
            array(
                'methods'             => 'GET',
                'callback'            => array( __CLASS__, 'get_things' ),
                'permission_callback' => '__return_true',
            )
        );
    }

    public static function get_things( $request )
    {
        return array();
    }
}
`)
	writeFile(t, dir, "wp-content/themes/probe/style.css", `/*
Theme Name: Probe
*/
`)
	writeFile(t, dir, "wp-content/themes/probe/header.php", `<?php /* header */ ?>
`)
	writeFile(t, dir, "wp-content/themes/probe/footer.php", `<?php /* footer */ ?>
`)
	writeFile(t, dir, "wp-content/themes/probe/template-parts/content-page.php", `<?php /* content-page */ ?>
`)
	writeFile(t, dir, "wp-content/themes/probe/template-parts/content.php", `<?php /* content fallback */ ?>
`)
	writeFile(t, dir, "wp-content/themes/probe/page.php", `<?php
get_header();
while ( have_posts() ) : the_post();
    get_template_part( 'template-parts/content', 'page' );
endwhile;
get_footer();
`)
	initGitRepo(t, dir)

	if err := Run(context.Background(), dir, Options{}); err != nil {
		t.Fatalf("index: %v", err)
	}
	repoID := mustRepoID(t, dir)

	// The REST handler is reachable from the registration site, so it is not
	// misreported as dead code and impact on it is non-empty.
	if c := resolvedCallers(t, dir, repoID, "get_things"); len(c) == 0 {
		t.Errorf("REST callback get_things should have callers; got %v", c)
	}
	// require_once wires the plugin entry file to the included class file.
	if n := countImportEdges(t, dir, repoID, "probe.php", "includes/class-probe-rest.php"); n == 0 {
		t.Error("require_once should create a file-level import edge to the included class file")
	}
	// Theme template-part / header / footer loads are file-level imports too.
	page := "wp-content/themes/probe/page.php"
	for _, dst := range []string{
		"template-parts/content-page.php",
		"template-parts/content.php",
		"header.php",
		"footer.php",
	} {
		if n := countImportEdges(t, dir, repoID, page, dst); n == 0 {
			t.Errorf("theme page should import %q; got 0 edges", dst)
		}
	}
}
