package ops

import (
	"strings"
	"testing"
)

func FuzzValidateReadOnlySQL(f *testing.F) {
	seeds := []string{
		"",
		"SELECT 1",
		"SELECT id FROM users",
		"INSERT INTO users VALUES (1)",
		"UPDATE users SET name='x'",
		"DELETE FROM users",
		"DROP TABLE users",
		"SELECT 1; SELECT 2",
		"WITH cte AS (SELECT 1 AS x) SELECT x FROM cte",
		"EXPLAIN SELECT 1",
		"EXPLAIN DELETE FROM users",
		"SHOW TABLES",
		"DESCRIBE users",
		"SELECT LOAD_FILE('/etc/passwd')",
		"SELECT load_extension('evil')",
		"SELECT writefile('/tmp/x', 'x')",
		"SELECT readfile('/etc/passwd')",
		"SELECT 1 INTO OUTFILE '/tmp/x'",
		"SELECT 1 FOR UPDATE",
		"-- comment\nSELECT 1",
		"/* c */ SELECT 'INSERT' AS s",
		"PRAGMA table_info(users)",
		"ATTACH DATABASE 'x.db' AS other",
		"CALL some_proc()",
		"SET GLOBAL general_log='ON'",
		"USE other_db",
		"WITH x AS (DELETE FROM t) SELECT 1",
		"WITH x AS (UPDATE t SET a=1) SELECT 1",
		"SELECT * FROM (DELETE FROM u RETURNING id) z",
		"/*!50000 INSERT INTO users VALUES (1) */ SELECT 1",
		"SHOW CREATE TABLE users",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		err := ValidateReadOnlySQL(sql)
		trimmed := strings.TrimSpace(sql)
		if trimmed == "" {
			if err == nil {
				t.Fatal("empty SQL must error")
			}
			return
		}
		body := trimmed
		if strings.HasSuffix(body, ";") {
			body = strings.TrimSpace(body[:len(body)-1])
		}
		if strings.Contains(body, ";") && err == nil {
			t.Fatalf("multi-statement must be blocked: %q", sql)
		}
		// Leading write/DDL keyword (after comments/whitespace) must fail closed.
		sc := newSQLScan(trimmed)
		kw, ok := sc.nextKeyword()
		if !ok {
			if err == nil {
				t.Fatalf("no keyword must error: %q", sql)
			}
			return
		}
		switch kw {
		case "INSERT", "UPDATE", "DELETE", "DROP", "TRUNCATE", "ALTER", "CREATE",
			"REPLACE", "GRANT", "REVOKE", "MERGE", "CALL", "EXEC", "EXECUTE",
			"ATTACH", "DETACH", "SET", "USE", "LOCK", "UNLOCK", "DO", "PRAGMA":
			if err == nil {
				t.Fatalf("leading %s must be blocked: %q", kw, sql)
			}
		}
		_ = err
	})
}
