package ops

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/VeyrForge/codehelper/internal/connections"
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
