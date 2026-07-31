package ops

import "testing"

func TestValidateReadOnlySQL_AllowsSelect(t *testing.T) {
	if err := ValidateReadOnlySQL("SELECT id, name FROM users WHERE id = 1"); err != nil {
		t.Fatalf("expected select allowed: %v", err)
	}
}

func TestValidateReadOnlySQL_BlocksInsert(t *testing.T) {
	if err := ValidateReadOnlySQL("INSERT INTO users VALUES (1)"); err == nil {
		t.Fatal("expected insert blocked")
	}
}

func TestValidateReadOnlySQL_BlocksMultiStatement(t *testing.T) {
	if err := ValidateReadOnlySQL("SELECT 1; SELECT 2;"); err == nil {
		t.Fatal("expected multi-statement blocked")
	}
	if err := ValidateReadOnlySQL("SELECT 1; SELECT 2"); err == nil {
		t.Fatal("expected multi-statement without trailing semicolon blocked")
	}
}

func TestValidateReadOnlySQL_Empty(t *testing.T) {
	if err := ValidateReadOnlySQL("  "); err == nil {
		t.Fatal("expected empty blocked")
	}
}

func TestValidateReadOnlySQL_Allowlist(t *testing.T) {
	allow := []string{
		"SELECT id, name FROM users WHERE id = 1",
		"  select 1  ;  ",
		"WITH cte AS (SELECT 1 AS x) SELECT x FROM cte",
		"WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM t WHERE n < 3) SELECT n FROM t",
		"SHOW TABLES",
		"SHOW COLUMNS FROM users",
		"SHOW CREATE TABLE users",
		"DESCRIBE users",
		"DESC users",
		"EXPLAIN SELECT 1",
		"EXPLAIN FORMAT=JSON SELECT id FROM users",
		"EXPLAIN ANALYZE SELECT 1",
		"-- comment\nSELECT 1",
		"/* c */ SELECT 'INSERT' AS s",
		"/*+ INDEX(t idx) */ SELECT 1",
		"SELECT * FROM users WHERE note = 'INTO OUTFILE'",
		"SELECT * FROM users WHERE note = 'DELETE FROM t'",
	}
	for _, q := range allow {
		if err := ValidateReadOnlySQL(q); err != nil {
			t.Fatalf("allowed %q: %v", q, err)
		}
	}

	block := []struct {
		sql string
		why string
	}{
		{"SET GLOBAL general_log = 'ON'", "SET GLOBAL"},
		{"SET SESSION sql_mode=''", "SET SESSION"},
		{"USE another_database", "USE"},
		{"LOCK TABLES users WRITE", "LOCK TABLES"},
		{"UNLOCK TABLES", "UNLOCK"},
		{"DO SLEEP(1)", "DO"},
		{"SELECT 1 INTO OUTFILE '/tmp/codehelper-output'", "INTO OUTFILE"},
		{"SELECT 1 INTO DUMPFILE '/tmp/codehelper-dump'", "INTO DUMPFILE"},
		{"SELECT LOAD_FILE('/etc/passwd')", "LOAD_FILE"},
		{"SELECT load_extension('evil')", "load_extension"},
		{"SELECT writefile('/tmp/x', 'x')", "writefile"},
		{"SELECT readfile('/etc/passwd')", "readfile"},
		{"SELECT GET_LOCK('codehelper', 60)", "GET_LOCK"},
		{"SELECT RELEASE_LOCK('codehelper')", "RELEASE_LOCK"},
		{"SELECT id INTO @x FROM users", "SELECT INTO var"},
		{"SELECT 1 FOR UPDATE", "FOR UPDATE"},
		{"SELECT 1 FOR SHARE", "FOR SHARE"},
		{"SELECT 1 LOCK IN SHARE MODE", "LOCK IN SHARE MODE"},
		{"INSERT INTO users VALUES (1)", "INSERT"},
		{"UPDATE users SET name='x'", "UPDATE"},
		{"DELETE FROM users", "DELETE"},
		{"DROP TABLE users", "DROP"},
		{"CREATE TABLE t (id INT)", "CREATE"},
		{"ALTER TABLE users ADD COLUMN x INT", "ALTER"},
		{"TRUNCATE users", "TRUNCATE"},
		{"REPLACE INTO users VALUES (1)", "REPLACE"},
		{"GRANT ALL ON *.* TO 'u'@'%'", "GRANT"},
		{"CALL some_proc()", "CALL"},
		{"WITH t AS (SELECT 1 AS x) INSERT INTO u SELECT x FROM t", "WITH INSERT"},
		{"WITH x AS (DELETE FROM t) SELECT 1", "writable CTE DELETE"},
		{"WITH x AS (UPDATE t SET a=1) SELECT 1", "writable CTE UPDATE"},
		{"WITH x AS (CREATE TABLE u(id INT)) SELECT 1", "writable CTE CREATE"},
		{"WITH x AS (DROP TABLE t) SELECT 1", "writable CTE DROP"},
		{"WITH x AS (TRUNCATE t) SELECT 1", "writable CTE TRUNCATE"},
		{"EXPLAIN WITH x AS (DELETE FROM t) SELECT 1", "EXPLAIN writable CTE"},
		{"SELECT * FROM t WHERE id IN (SELECT id FROM (DELETE FROM u RETURNING id) z)", "subquery DELETE"},
		{"/*!50000 INSERT INTO users VALUES (1) */ SELECT 1", "MySQL versioned comment INSERT"},
		{"/*!50000 UPDATE users SET x=1 */ SELECT 1", "MySQL versioned comment UPDATE"},
		{"/*!50000 DELETE FROM users */ SELECT 1", "MySQL versioned comment DELETE"},
		{"EXPLAIN DELETE FROM users", "EXPLAIN DELETE"},
		{"EXPLAIN UPDATE users SET name='x'", "EXPLAIN UPDATE"},
		{"PRAGMA table_info(users)", "PRAGMA"},
		{"ATTACH DATABASE 'x.db' AS other", "ATTACH"},
	}
	for _, tc := range block {
		if err := ValidateReadOnlySQL(tc.sql); err == nil {
			t.Fatalf("expected blocked (%s): %q", tc.why, tc.sql)
		}
	}
}
