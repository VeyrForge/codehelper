package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit is a minimal helper that wires the repo with an initial commit so the
// guard tools can compute a diff between HEAD~1 and the working tree.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func newGitFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "seed")
	return dir
}

func writeAndStage(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "--", rel)
	runGit(t, dir, "commit", "-q", "-m", "change")
}

func TestPerfGuard_NPlusOneInLoop(t *testing.T) {
	dir := newGitFixture(t)
	writeAndStage(t, dir, "app/Http/Controllers/UserController.php", `<?php
class UserController {
    public function index() {
        $users = User::all();
        foreach ($users as $user) {
            $u = User::find($user->id);
            echo $u->profile->name;
        }
    }
}
`)
	res, err := PerfGuard(context.Background(), dir, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PerfRisks) == 0 {
		t.Fatalf("expected at least one perf finding for foreach+find, got 0")
	}
	found := false
	for _, f := range res.PerfRisks {
		if strings.Contains(f.Message, "N+1") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected an N+1 finding, got %#v", res.PerfRisks)
	}
}

func TestPerfGuard_EmptyOnHarmlessDiff(t *testing.T) {
	dir := newGitFixture(t)
	writeAndStage(t, dir, "docs/notes.md", "Hello world\nNo code here.\n")
	res, err := PerfGuard(context.Background(), dir, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PerfRisks) != 0 {
		t.Fatalf("expected zero perf risks for docs change, got %d: %#v", len(res.PerfRisks), res.PerfRisks)
	}
}

func TestMigrationGuard_DropTable(t *testing.T) {
	dir := newGitFixture(t)
	writeAndStage(t, dir, "db/migrations/2024_01_01_drop_users.sql", `
DROP TABLE users;
ALTER TABLE orders DROP COLUMN legacy_id;
`)
	res, err := MigrationGuard(context.Background(), dir, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DestructiveRisks) < 2 {
		t.Fatalf("expected 2 destructive findings, got %d: %#v", len(res.DestructiveRisks), res.DestructiveRisks)
	}
}

func TestMigrationGuard_NoSchemaFiles(t *testing.T) {
	dir := newGitFixture(t)
	writeAndStage(t, dir, "src/util.ts", "export const x = 1\n")
	res, err := MigrationGuard(context.Background(), dir, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DestructiveRisks) != 0 {
		t.Fatalf("expected no migration findings, got %d", len(res.DestructiveRisks))
	}
}

func TestMigrationGuard_IgnoresSourceCodeWithDDLLiterals(t *testing.T) {
	dir := newGitFixture(t)
	writeAndStage(t, dir, "internal/review/migration_guard.go", `package review
// pattern: regexp.MustCompile("(?i)\\bdrop\\s+table\\b")
// pattern: regexp.MustCompile("(?i)\\btruncate\\s+(table\\s+)?[\\w]+")
`)
	res, err := MigrationGuard(context.Background(), dir, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DestructiveRisks) != 0 {
		t.Fatalf("migration_guard must not flag its own Go source file: %#v", res.DestructiveRisks)
	}
}

func TestArchitectureLint_ControllerImportsDB(t *testing.T) {
	dir := newGitFixture(t)
	writeAndStage(t, dir, "app/Http/Controllers/PostController.php", `<?php
use Illuminate\Database\Eloquent\Model as Eloquent;
class PostController {}
`)
	res, err := ArchitectureLint(context.Background(), dir, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) == 0 {
		t.Fatalf("expected at least one architecture violation, got 0")
	}
}

func TestArchitectureLint_NoFalsePositiveForServiceLayer(t *testing.T) {
	dir := newGitFixture(t)
	writeAndStage(t, dir, "app/Services/PostService.php", `<?php
use Illuminate\Database\Eloquent\Model as Eloquent;
class PostService {}
`)
	res, err := ArchitectureLint(context.Background(), dir, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("expected no violations for service layer, got %#v", res.Violations)
	}
}
