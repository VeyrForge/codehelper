package ops

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/connections"
	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

func TestQueryDB_SQLiteSelect(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "test.db")
	sqldb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(`CREATE TABLE items (id INTEGER, name TEXT); INSERT INTO items VALUES (1, 'a'), (2, 'b');`); err != nil {
		t.Fatal(err)
	}
	sqldb.Close()

	cfg := connections.Config{
		Databases: []connections.DBConn{{Name: "local", Driver: "sqlite", Database: "test.db", ReadOnly: true}},
	}
	if err := connections.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	out, err := QueryDB(context.Background(), root, "local", "SELECT id, name FROM items ORDER BY id", 10)
	if err != nil {
		t.Fatal(err)
	}
	if out.RowCount != 2 {
		t.Fatalf("row_count=%d want 2", out.RowCount)
	}
}

func TestQueryDB_BlocksWrite(t *testing.T) {
	root := t.TempDir()
	cfg := connections.Config{
		Databases: []connections.DBConn{{Name: "local", Driver: "sqlite", Database: "test.db"}},
	}
	_ = connections.Save(root, cfg)
	_, err := QueryDB(context.Background(), root, "local", "DELETE FROM items", 10)
	if err == nil {
		t.Fatal("expected write blocked")
	}
	_ = os.Remove(filepath.Join(root, "test.db"))
}

func TestMySQLDSN_RefusesPublicHost(t *testing.T) {
	root := t.TempDir()
	_, err := mysqlDSN(root, connections.DBConn{
		Name: "x", Driver: "mysql", Host: "8.8.8.8", User: "u", Database: "d", PasswordRef: "env:NONE",
	})
	if err == nil {
		t.Fatal("expected public host refused")
	}
}

func TestQueryDB_MySQLLocal(t *testing.T) {
	if !mysqlFixtureAvailable() {
		t.Skip("no local mysql fixture (wp_test)")
	}
	root := t.TempDir()
	pass := os.Getenv("CODEHELPER_TEST_MYSQL_PASS")
	if pass == "" {
		pass = "wp_pass"
	}
	t.Setenv("WP_TEST_DB_PASS", pass)
	cfg := connections.Config{
		Databases: []connections.DBConn{{
			Name: "wp", Driver: "mysql", Host: "127.0.0.1", Port: 3306,
			Database: "wp_test", User: "wp", PasswordRef: "env:WP_TEST_DB_PASS", ReadOnly: true,
		}},
	}
	if err := connections.Save(root, cfg); err != nil {
		t.Fatal(err)
	}
	out, err := QueryDB(context.Background(), root, "wp", "SELECT COUNT(*) AS c FROM wp_posts", 5)
	if err != nil {
		t.Fatalf("mysql query: %v", err)
	}
	if out.RowCount != 1 {
		t.Fatalf("row_count=%d want 1", out.RowCount)
	}
}

func mysqlFixtureAvailable() bool {
	sqldb, err := sql.Open("mysql", "wp:wp_pass@tcp(127.0.0.1:3306)/wp_test?timeout=2s")
	if err != nil {
		return false
	}
	defer sqldb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return sqldb.PingContext(ctx) == nil
}
