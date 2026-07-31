package mcpsvc

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/connections"
	_ "modernc.org/sqlite"
)

// TestOpsHandlers_Local exercises MCP ops handlers end-to-end on the indexed
// workspace repo (same scoping rules as TestAllToolsSmoke).
func TestOpsHandlers_Local(t *testing.T) {
	reg, repo := liveRegistryWithIndexedRepo(t)
	handlers := AllToolHandlers(reg)
	common := map[string]any{"repo": repo.Name, "format": "json"}

	connPath := connections.Path(repo.RootPath)
	backup, hadBackup := backupFile(t, connPath)

	// Unique per-run dir under the indexed repo (same volume as RootPath) so a
	// prior cleanup, parallel suite noise, or leftover RemoveAll cannot delete
	// fixtures mid-test. Absolute Database/Path avoid RootPath Join ambiguity.
	fixtureID := fmt.Sprintf("%d", time.Now().UnixNano())
	fixtureDir := filepath.Join(repo.RootPath, ".codehelper", "_ops_test_"+fixtureID)
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fixtureDir) })

	dbPath := filepath.Join(fixtureDir, "app.db")
	sqldb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(`DROP TABLE IF EXISTS users;
		CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
		INSERT INTO users (name) VALUES ('alice'), ('bob');`); err != nil {
		sqldb.Close()
		t.Fatal(err)
	}
	if err := sqldb.Close(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dbPath); err != nil || st.Size() == 0 {
		t.Fatalf("fixture db missing or empty after create: path=%s err=%v", dbPath, err)
	}

	logAbs := filepath.Join(fixtureDir, "app.log")
	if err := os.WriteFile(logAbs, []byte("err one\nerr two\nerr three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logAbs); err != nil {
		t.Fatalf("fixture log missing after write: %v", err)
	}

	cfg, err := connections.Load(repo.RootPath)
	if err != nil {
		cfg = connections.Config{}
	}
	// Strip any leftover ops-test profiles from prior dirty runs before upsert.
	_ = cfg.Remove("ops-test-db")
	_ = cfg.RemoveLogSource("ops-test-log")
	_ = cfg.RemoveAlias("ops-test-true")
	_ = cfg.Remove("ops-test-ssh")

	if err := cfg.AddDatabase(connections.DBConn{Name: "ops-test-db", Driver: "sqlite", Database: dbPath, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddLogSource(connections.LogSource{Name: "ops-test-log", Kind: "app", Path: logAbs}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddAlias(connections.CommandAlias{Name: "ops-test-true", Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddSSHHost(connections.SSHHost{Name: "ops-test-ssh", Hostname: "127.0.0.1", AllowedCommands: []string{"tail"}}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddRecipe("ops-test-ssh", connections.Recipe{Name: "tail-log", Argv: []string{"tail", "-n", "5", "/var/log/syslog"}, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := connections.Save(repo.RootPath, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		restoreFile(t, connPath, backup, hadBackup)
		// If backup was already dirty with ops-test_* from an older flake, strip them.
		if c, err := connections.Load(repo.RootPath); err == nil {
			changed := c.Remove("ops-test-db")
			changed = c.RemoveLogSource("ops-test-log") || changed
			changed = c.RemoveAlias("ops-test-true") || changed
			changed = c.Remove("ops-test-ssh") || changed
			if !changed {
				return
			}
			if c.Empty() && !hadBackup {
				_ = os.Remove(connPath)
				return
			}
			_ = connections.Save(repo.RootPath, c)
		}
	})

	t.Run("log_read_ok", func(t *testing.T) {
		if _, err := os.Stat(logAbs); err != nil {
			t.Fatalf("log fixture vanished before log_read: %v", err)
		}
		res, err := callTool(t, nil, handlers, "log_read", mergeArgs(common, map[string]any{"source": "ops-test-log", "lines": float64(2)}))
		mustOK(t, "log_read", res, err)
	})

	t.Run("db_query_ok", func(t *testing.T) {
		if _, err := os.Stat(dbPath); err != nil {
			t.Fatalf("db fixture vanished before db_query: %v", err)
		}
		res, err := callTool(t, nil, handlers, "db_query", mergeArgs(common, map[string]any{
			"connection": "ops-test-db", "sql": "SELECT id, name FROM users ORDER BY id",
		}))
		mustOK(t, "db_query", res, err)
	})

	t.Run("db_schema_ok", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "db_schema", mergeArgs(common, map[string]any{
			"connection": "ops-test-db",
		}))
		mustOK(t, "db_schema", res, err)
	})

	t.Run("db_query_blocks_write", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "db_query", mergeArgs(common, map[string]any{
			"connection": "ops-test-db", "sql": "DELETE FROM users",
		}))
		shouldError(t, "db_query", res, err)
	})

	t.Run("run_alias_local", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "run_alias", mergeArgs(common, map[string]any{"name": "ops-test-true"}))
		mustOK(t, "run_alias", res, err)
	})

	t.Run("remote_exec_ssh_fail", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "remote_exec", mergeArgs(common, map[string]any{
			"host": "ops-test-ssh", "recipe": "tail-log",
		}))
		if err != nil {
			t.Fatalf("handler error: %v", err)
		}
		if res == nil {
			t.Fatal("nil result")
		}
		// SSH to 127.0.0.1 without server: expect failure output, not panic.
		if resultText(res) == "" && !res.IsError {
			t.Fatal("expected SSH failure output")
		}
	})
}

func mergeArgs(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func backupFile(t *testing.T, path string) ([]byte, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

func restoreFile(t *testing.T, path string, data []byte, had bool) {
	t.Helper()
	if had {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Errorf("restore connections: %v", err)
		}
		return
	}
	_ = os.Remove(path)
}
