package mcpsvc

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

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

	fixtureRoot := t.TempDir()
	dbPath := filepath.Join(fixtureRoot, "app.db")
	sqldb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
		INSERT INTO users (name) VALUES ('alice'), ('bob');`); err != nil {
		t.Fatal(err)
	}
	sqldb.Close()

	logRel := filepath.Join(".codehelper", "_ops_test", "app.log")
	logAbs := filepath.Join(repo.RootPath, logRel)
	if err := os.MkdirAll(filepath.Dir(logAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logAbs, []byte("err one\nerr two\nerr three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(logAbs)) })

	dbRel, _ := filepath.Rel(repo.RootPath, dbPath)
	cfg, err := connections.Load(repo.RootPath)
	if err != nil {
		cfg = connections.Config{}
	}
	_ = cfg.AddDatabase(connections.DBConn{Name: "ops-test-db", Driver: "sqlite", Database: dbRel, ReadOnly: true})
	_ = cfg.AddLogSource(connections.LogSource{Name: "ops-test-log", Kind: "app", Path: logRel})
	_ = cfg.AddAlias(connections.CommandAlias{Name: "ops-test-true", Argv: []string{"true"}})
	_ = cfg.AddSSHHost(connections.SSHHost{Name: "ops-test-ssh", Hostname: "127.0.0.1", AllowedCommands: []string{"tail"}})
	_ = cfg.AddRecipe("ops-test-ssh", connections.Recipe{Name: "tail-log", Argv: []string{"tail", "-n", "5", "/var/log/syslog"}, ReadOnly: true})
	if err := connections.Save(repo.RootPath, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restoreFile(t, connPath, backup, hadBackup) })

	t.Run("log_read_ok", func(t *testing.T) {
		res, err := callTool(t, nil, handlers, "log_read", mergeArgs(common, map[string]any{"source": "ops-test-log", "lines": float64(2)}))
		mustOK(t, "log_read", res, err)
	})

	t.Run("db_query_ok", func(t *testing.T) {
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
