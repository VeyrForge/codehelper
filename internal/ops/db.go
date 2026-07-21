package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
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
// Supported in-process drivers: sqlite, mysql (MariaDB).
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
	db, err := findDB(repoRoot, connName)
	if err != nil {
		return nil, err
	}
	sqldb, _, err := openSQLDB(repoRoot, *db)
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

// SchemaDB returns table/column info for sqlite and mysql connections.
func SchemaDB(ctx context.Context, repoRoot, connName string, tables []string) (*DBSchemaResult, error) {
	db, err := findDB(repoRoot, connName)
	if err != nil {
		return nil, err
	}
	sqldb, driver, err := openSQLDB(repoRoot, *db)
	if err != nil {
		return nil, err
	}
	defer sqldb.Close()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	switch driver {
	case "sqlite":
		return schemaSQLite(cctx, sqldb, db.Name, tables)
	case "mysql":
		return schemaMySQL(cctx, sqldb, db.Name, tables)
	default:
		return nil, fmt.Errorf("schema introspection supported for sqlite|mysql only (got %q)", driver)
	}
}

func findDB(repoRoot, connName string) (*connections.DBConn, error) {
	cfg, err := connections.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	for i := range cfg.Databases {
		if strings.EqualFold(cfg.Databases[i].Name, connName) {
			db := &cfg.Databases[i]
			if db.Disabled {
				return nil, fmt.Errorf("database connection %q is disabled", connName)
			}
			if !db.ReadOnly {
				return nil, fmt.Errorf("database connection %q is not marked read-only — MCP db_query requires read_only=true", connName)
			}
			return db, nil
		}
	}
	return nil, fmt.Errorf("database connection %q not configured", connName)
}

func openSQLDB(repoRoot string, db connections.DBConn) (*sql.DB, string, error) {
	if strings.TrimSpace(db.SSHTunnel) != "" {
		return nil, "", fmt.Errorf("ssh_tunnel on %q is not supported for in-process db_query yet", db.Name)
	}
	switch strings.ToLower(db.Driver) {
	case "sqlite":
		dsn, err := sqliteDSN(repoRoot, db)
		if err != nil {
			return nil, "", err
		}
		sqldb, err := sql.Open("sqlite", dsn)
		return sqldb, "sqlite", err
	case "mysql":
		dsn, err := mysqlDSN(repoRoot, db)
		if err != nil {
			return nil, "", err
		}
		sqldb, err := sql.Open("mysql", dsn)
		if err != nil {
			return nil, "", err
		}
		sqldb.SetConnMaxLifetime(30 * time.Second)
		sqldb.SetMaxOpenConns(2)
		sqldb.SetMaxIdleConns(1)
		return sqldb, "mysql", nil
	default:
		return nil, "", fmt.Errorf("driver %q: in-process query supported for sqlite|mysql in this release", db.Driver)
	}
}

func schemaSQLite(ctx context.Context, sqldb *sql.DB, connName string, tables []string) (*DBSchemaResult, error) {
	names := tables
	if len(names) == 0 {
		rows, err := sqldb.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name LIMIT 50`)
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
	res := &DBSchemaResult{Connection: connName}
	for _, tbl := range names {
		if !safeIdent(tbl) {
			continue
		}
		q := fmt.Sprintf(`PRAGMA table_info(%q)`, tbl)
		rows, err := sqldb.QueryContext(ctx, q)
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

func schemaMySQL(ctx context.Context, sqldb *sql.DB, connName string, tables []string) (*DBSchemaResult, error) {
	names := tables
	if len(names) == 0 {
		rows, err := sqldb.QueryContext(ctx, `SHOW TABLES`)
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
			if len(names) >= 50 {
				break
			}
		}
	}
	res := &DBSchemaResult{Connection: connName}
	for _, tbl := range names {
		if !safeIdent(tbl) {
			continue
		}
		q := fmt.Sprintf("SHOW COLUMNS FROM `%s`", tbl)
		rows, err := sqldb.QueryContext(ctx, q)
		if err != nil {
			continue
		}
		ts := TableSchema{Name: tbl}
		for rows.Next() {
			var field, typ string
			var null, key, extra sql.NullString
			var def sql.NullString
			if err := rows.Scan(&field, &typ, &null, &key, &def, &extra); err != nil {
				rows.Close()
				break
			}
			ts.Columns = append(ts.Columns, field+" "+typ)
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

func mysqlDSN(repoRoot string, db connections.DBConn) (string, error) {
	host := strings.TrimSpace(db.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil {
		if !(ip.IsLoopback() || ip.IsPrivate()) {
			return "", fmt.Errorf("mysql host %q refused — db_query allows loopback/private only", host)
		}
	} else {
		hl := strings.ToLower(host)
		if hl != "localhost" && !strings.HasSuffix(hl, ".local") && !strings.HasSuffix(hl, ".localhost") {
			return "", fmt.Errorf("mysql host %q refused — use 127.0.0.1/localhost/*.local for in-process query", host)
		}
	}
	port := db.Port
	if port <= 0 {
		port = 3306
	}
	user := strings.TrimSpace(db.User)
	if user == "" {
		return "", fmt.Errorf("mysql connection %q needs user=", db.Name)
	}
	dbname := strings.TrimSpace(db.Database)
	if dbname == "" {
		return "", fmt.Errorf("mysql connection %q needs database=", db.Name)
	}
	pass, err := ResolveRef(repoRoot, db.PasswordRef, db.Name)
	if err != nil {
		return "", fmt.Errorf("resolve db password: %w", err)
	}
	cfg := mysql.Config{
		User:                 user,
		Passwd:               pass,
		Net:                  "tcp",
		Addr:                 net.JoinHostPort(host, strconv.Itoa(port)),
		DBName:               dbname,
		ParseTime:            true,
		AllowNativePasswords: true,
		Timeout:              10 * time.Second,
		ReadTimeout:          10 * time.Second,
		WriteTimeout:         10 * time.Second,
	}
	return cfg.FormatDSN(), nil
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
