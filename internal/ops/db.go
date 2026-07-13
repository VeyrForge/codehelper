package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/VeyrForge/codehelper/internal/connections"
	_ "modernc.org/sqlite"
)

// DBQueryResult is a bounded query result set.
type DBQueryResult struct {
	Connection string           `json:"connection"`
	Columns    []string         `json:"columns"`
	Rows       []map[string]any `json:"rows"`
	Truncated  bool             `json:"truncated,omitempty"`
	RowCount   int              `json:"row_count"`
}

// DBSchemaResult describes tables/columns for allowed tables.
type DBSchemaResult struct {
	Connection string        `json:"connection"`
	Tables     []TableSchema `json:"tables"`
}

// TableSchema is one table's column list.
type TableSchema struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

// QueryDB runs a read-only SQL query against a named connection profile.
func QueryDB(ctx context.Context, repoRoot, connName, sqlText string, maxRows int) (*DBQueryResult, error) {
	if err := ValidateReadOnlySQL(sqlText); err != nil {
		return nil, err
	}
	if maxRows <= 0 {
		maxRows = maxDBRows
	}
	if maxRows > maxDBRows {
		maxRows = maxDBRows
	}
	cfg, err := connections.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	var db *connections.DBConn
	for i := range cfg.Databases {
		if strings.EqualFold(cfg.Databases[i].Name, connName) {
			db = &cfg.Databases[i]
			break
		}
	}
	if db == nil {
		return nil, fmt.Errorf("database connection %q not configured", connName)
	}
	if db.Disabled {
		return nil, fmt.Errorf("database connection %q is disabled", connName)
	}
	if !db.ReadOnly {
		return nil, fmt.Errorf("database connection %q is not marked read-only — MCP db_query requires read_only=true", connName)
	}
	dsn, err := sqliteDSN(repoRoot, *db)
	if err != nil {
		return nil, err
	}
	if strings.ToLower(db.Driver) != "sqlite" {
		return nil, fmt.Errorf("driver %q: in-process query supported for sqlite only in this release", db.Driver)
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer sqldb.Close()
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rows, err := sqldb.QueryContext(cctx, sqlText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := &DBQueryResult{Connection: db.Name, Columns: cols}
	for rows.Next() {
		if len(out.Rows) >= maxRows {
			out.Truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, c := range cols {
			row[c] = normalizeCell(vals[i])
		}
		out.Rows = append(out.Rows, row)
	}
	out.RowCount = len(out.Rows)
	return out, rows.Err()
}

// SchemaDB returns table/column info for sqlite connections.
func SchemaDB(ctx context.Context, repoRoot, connName string, tables []string) (*DBSchemaResult, error) {
	cfg, err := connections.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	var db *connections.DBConn
	for i := range cfg.Databases {
		if strings.EqualFold(cfg.Databases[i].Name, connName) {
			db = &cfg.Databases[i]
			break
		}
	}
	if db == nil {
		return nil, fmt.Errorf("database connection %q not configured", connName)
	}
	if db.Disabled {
		return nil, fmt.Errorf("database connection %q is disabled", connName)
	}
	if !db.ReadOnly {
		return nil, fmt.Errorf("database connection %q is not marked read-only", connName)
	}
	if strings.ToLower(db.Driver) != "sqlite" {
		return nil, fmt.Errorf("schema introspection supported for sqlite only in this release")
	}
	dsn, err := sqliteDSN(repoRoot, *db)
	if err != nil {
		return nil, err
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer sqldb.Close()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	names := tables
	if len(names) == 0 {
		rows, err := sqldb.QueryContext(cctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name LIMIT 50`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return nil, err
			}
			names = append(names, n)
		}
	}
	res := &DBSchemaResult{Connection: db.Name}
	for _, tbl := range names {
		if !safeIdent(tbl) {
			continue
		}
		q := fmt.Sprintf(`PRAGMA table_info(%q)`, tbl)
		rows, err := sqldb.QueryContext(cctx, q)
		if err != nil {
			continue
		}
		ts := TableSchema{Name: tbl}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				break
			}
			ts.Columns = append(ts.Columns, name+" "+typ)
		}
		rows.Close()
		if len(ts.Columns) > 0 {
			res.Tables = append(res.Tables, ts)
		}
	}
	return res, nil
}

func sqliteDSN(repoRoot string, db connections.DBConn) (string, error) {
	path := strings.TrimSpace(db.Database)
	if path == "" {
		path = strings.TrimSpace(db.Host)
	}
	if path == "" {
		return "", fmt.Errorf("sqlite connection needs database= path to .db file")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, path)
	}
	return "file:" + filepath.ToSlash(path) + "?mode=ro", nil
}

func safeIdent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func normalizeCell(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		return x
	}
}

// MarshalJSON helper.
func MarshalJSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return string(b), err
}
