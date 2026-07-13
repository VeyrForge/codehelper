package ops

import (
	"regexp"
	"strings"
)

var blockedSQL = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|DROP|TRUNCATE|ALTER|CREATE|REPLACE|GRANT|REVOKE|MERGE|CALL|EXEC|EXECUTE|ATTACH|DETACH|PRAGMA\s+\w+\s*=)\b`)

// ValidateReadOnlySQL rejects DDL/DML and dangerous pragmas.
func ValidateReadOnlySQL(sql string) error {
	s := strings.TrimSpace(sql)
	if s == "" {
		return errEmptySQL
	}
	if blockedSQL.MatchString(s) {
		return errWriteSQL
	}
	if strings.Count(s, ";") > 1 {
		return errMultiStatement
	}
	return nil
}

var (
	errEmptySQL       = sqlError("empty sql")
	errWriteSQL       = sqlError("write/DDL SQL blocked — read-only connections only")
	errMultiStatement = sqlError("multi-statement SQL blocked")
)

type sqlError string

func (e sqlError) Error() string { return string(e) }
