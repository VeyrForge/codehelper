package ops

import (
	"regexp"
	"strings"
	"unicode"
)

// ValidateReadOnlySQL allows only read-oriented statements:
// SELECT, WITH…SELECT, SHOW, DESCRIBE/DESC, and EXPLAIN…SELECT.
// Shape is enforced with a keyword scanner (allowlist), not a write blocklist.
// Extra rejects cover side-effecting SELECT forms (INTO OUTFILE, LOAD_FILE,
// load_extension/writefile/readfile, locks), nested DML/DDL (writable CTEs /
// subqueries), and MySQL versioned comments (/*!…*/).
func ValidateReadOnlySQL(sql string) error {
	s := strings.TrimSpace(sql)
	if s == "" {
		return errEmptySQL
	}
	// One optional trailing semicolon; any other ';' is multi-statement.
	if strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(s[:len(s)-1])
	}
	if s == "" {
		return errEmptySQL
	}
	if strings.Contains(s, ";") {
		return errMultiStatement
	}
	// MySQL /*!…*/ is NOT a comment — the server may execute the enclosed SQL.
	// Our scanners treat /*…*/ as comments, so reject versioned comments fail-closed.
	if strings.Contains(s, "/*!") {
		return errWriteSQL
	}

	sc := newSQLScan(s)
	kw, ok := sc.nextKeyword()
	if !ok {
		return errEmptySQL
	}
	switch kw {
	case "SELECT", "SHOW", "DESCRIBE", "DESC":
		// allowed
	case "EXPLAIN":
		if err := requireSelectAfterExplain(&sc); err != nil {
			return err
		}
	case "WITH":
		if err := requireSelectAfterWith(&sc); err != nil {
			return err
		}
	default:
		// SET, USE, LOCK, UNLOCK, DO, INSERT, DDL, …
		return errWriteSQL
	}

	if err := rejectUnsafeSQL(s); err != nil {
		return err
	}
	// Nested DML/DDL only for SELECT-family (SHOW CREATE TABLE must stay allowed).
	switch kw {
	case "SELECT", "WITH", "EXPLAIN":
		return rejectNestedWrites(s)
	}
	return nil
}

func requireSelectAfterExplain(sc *sqlScan) error {
	for i := 0; i < 64; i++ {
		kw, ok := sc.nextKeyword()
		if !ok {
			return errWriteSQL
		}
		switch kw {
		case "SELECT":
			return nil
		case "WITH":
			return requireSelectAfterWith(sc)
		case "INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "DROP",
			"ALTER", "TRUNCATE", "MERGE", "CALL", "EXEC", "EXECUTE", "DO",
			"SET", "USE", "LOCK", "UNLOCK", "GRANT", "REVOKE", "ATTACH", "DETACH":
			return errWriteSQL
		default:
			// FORMAT, ANALYZE, VERBOSE, EXTENDED, PARTITIONS, JSON, TRUE, …
			continue
		}
	}
	return errWriteSQL
}

func requireSelectAfterWith(sc *sqlScan) error {
	if kw, ok := sc.peekKeyword(); ok && kw == "RECURSIVE" {
		_, _ = sc.nextKeyword()
	}
	for {
		if _, ok := sc.nextKeyword(); !ok {
			return errWriteSQL
		}
		sc.skipSpaceComments()
		if sc.skipIfRune('(') {
			if err := sc.skipBalanced('(', ')'); err != nil {
				return err
			}
		}
		as, ok := sc.nextKeyword()
		if !ok || as != "AS" {
			return errWriteSQL
		}
		sc.skipSpaceComments()
		if !sc.skipIfRune('(') {
			return errWriteSQL
		}
		if err := sc.skipBalanced('(', ')'); err != nil {
			return err
		}
		sc.skipSpaceComments()
		if sc.skipIfRune(',') {
			continue
		}
		break
	}
	kw, ok := sc.nextKeyword()
	if !ok || kw != "SELECT" {
		return errWriteSQL
	}
	return nil
}

// unsafeSQL matches side-effecting or locking constructs inside otherwise
// allowlisted statements. Applied to SQL with string/comment literals blanked.
var unsafeSQL = regexp.MustCompile(`(?i)(?:` +
	`\bINTO\b|` +
	`\bLOAD_FILE\s*\(|` +
	`\bLOAD_EXTENSION\s*\(|` +
	`\bWRITEFILE\s*\(|` +
	`\bREADFILE\s*\(|` +
	`\bGET_LOCK\s*\(|` +
	`\bRELEASE_LOCK\s*\(|` +
	`\bIS_USED_LOCK\s*\(|` +
	`\bIS_FREE_LOCK\s*\(|` +
	`\bFOR\s+UPDATE\b|` +
	`\bFOR\s+SHARE\b|` +
	`\bLOCK\s+IN\s+SHARE\s+MODE\b` +
	`)`)

func rejectUnsafeSQL(sql string) error {
	flat := blankSQLLiterals(sql)
	if unsafeSQL.MatchString(flat) {
		return errWriteSQL
	}
	return nil
}

// nestedWriteSQL catches DML/DDL keywords inside otherwise-allowlisted SELECT /
// WITH…SELECT / EXPLAIN…SELECT (writable CTEs, DELETE/UPDATE in subqueries, …).
// Not applied to SHOW/DESCRIBE — e.g. SHOW CREATE TABLE must remain allowed.
var nestedWriteSQL = regexp.MustCompile(`(?i)(?:` +
	`\bINSERT\b|` +
	`\bUPDATE\b|` +
	`\bDELETE\b|` +
	`\bREPLACE\b|` +
	`\bMERGE\b|` +
	`\bCREATE\b|` +
	`\bDROP\b|` +
	`\bALTER\b|` +
	`\bTRUNCATE\b|` +
	`\bCALL\b|` +
	`\bEXEC(?:UTE)?\b|` +
	`\bGRANT\b|` +
	`\bREVOKE\b|` +
	`\bATTACH\b|` +
	`\bDETACH\b|` +
	`\bCOPY\b|` +
	`\bLOAD\b|` +
	`\bDO\b` +
	`)`)

func rejectNestedWrites(sql string) error {
	flat := blankSQLLiterals(sql)
	if nestedWriteSQL.MatchString(flat) {
		return errWriteSQL
	}
	return nil
}

// blankSQLLiterals replaces string/quoted-ident literals and comments with spaces
// so keyword checks do not match inside user data.
func blankSQLLiterals(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '-' && s[i+1] == '-' {
			for i < len(s) && s[i] != '\n' {
				b.WriteByte(' ')
				i++
			}
			continue
		}
		if s[i] == '#' {
			for i < len(s) && s[i] != '\n' {
				b.WriteByte(' ')
				i++
			}
			continue
		}
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			b.WriteByte(' ')
			b.WriteByte(' ')
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				b.WriteByte(' ')
				i++
			}
			if i+1 < len(s) {
				b.WriteByte(' ')
				b.WriteByte(' ')
				i += 2
			}
			continue
		}
		if s[i] == '\'' || s[i] == '"' || s[i] == '`' {
			quote := s[i]
			b.WriteByte(' ')
			i++
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) {
					b.WriteByte(' ')
					b.WriteByte(' ')
					i += 2
					continue
				}
				if s[i] == quote {
					// SQL escaped quote: '' or ""
					if i+1 < len(s) && s[i+1] == quote {
						b.WriteByte(' ')
						b.WriteByte(' ')
						i += 2
						continue
					}
					b.WriteByte(' ')
					i++
					break
				}
				b.WriteByte(' ')
				i++
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

type sqlScan struct {
	s string
	i int
}

func newSQLScan(s string) sqlScan { return sqlScan{s: s} }

func (sc *sqlScan) skipSpaceComments() {
	for sc.i < len(sc.s) {
		c := sc.s[sc.i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			sc.i++
			continue
		}
		if sc.i+1 < len(sc.s) && sc.s[sc.i] == '-' && sc.s[sc.i+1] == '-' {
			sc.i += 2
			for sc.i < len(sc.s) && sc.s[sc.i] != '\n' {
				sc.i++
			}
			continue
		}
		if c == '#' {
			for sc.i < len(sc.s) && sc.s[sc.i] != '\n' {
				sc.i++
			}
			continue
		}
		if sc.i+1 < len(sc.s) && sc.s[sc.i] == '/' && sc.s[sc.i+1] == '*' {
			sc.i += 2
			for sc.i+1 < len(sc.s) && !(sc.s[sc.i] == '*' && sc.s[sc.i+1] == '/') {
				sc.i++
			}
			if sc.i+1 < len(sc.s) {
				sc.i += 2
			}
			continue
		}
		return
	}
}

func (sc *sqlScan) skipString(quote byte) {
	sc.i++ // opening quote
	for sc.i < len(sc.s) {
		if sc.s[sc.i] == '\\' && sc.i+1 < len(sc.s) {
			sc.i += 2
			continue
		}
		if sc.s[sc.i] == quote {
			if sc.i+1 < len(sc.s) && sc.s[sc.i+1] == quote {
				sc.i += 2
				continue
			}
			sc.i++
			return
		}
		sc.i++
	}
}

func (sc *sqlScan) skipIfRune(r byte) bool {
	sc.skipSpaceComments()
	if sc.i < len(sc.s) && sc.s[sc.i] == r {
		sc.i++
		return true
	}
	return false
}

func (sc *sqlScan) skipBalanced(open, close byte) error {
	// Caller consumed the opening delimiter; sc.i is just past it.
	depth := 1
	for sc.i < len(sc.s) && depth > 0 {
		c := sc.s[sc.i]
		if c == '\'' || c == '"' || c == '`' {
			sc.skipString(c)
			continue
		}
		if c == open {
			depth++
		} else if c == close {
			depth--
		}
		sc.i++
	}
	if depth != 0 {
		return errWriteSQL
	}
	return nil
}

func (sc *sqlScan) nextKeyword() (string, bool) {
	for {
		sc.skipSpaceComments()
		if sc.i >= len(sc.s) {
			return "", false
		}
		c := sc.s[sc.i]
		if c == '\'' || c == '"' || c == '`' {
			sc.skipString(c)
			continue
		}
		if isSQLIdentStart(rune(c)) {
			start := sc.i
			sc.i++
			for sc.i < len(sc.s) && isSQLIdentCont(rune(sc.s[sc.i])) {
				sc.i++
			}
			return strings.ToUpper(sc.s[start:sc.i]), true
		}
		// Punctuation / operators — skip one byte and keep scanning for a keyword.
		sc.i++
	}
}

func (sc *sqlScan) peekKeyword() (string, bool) {
	save := sc.i
	kw, ok := sc.nextKeyword()
	sc.i = save
	return kw, ok
}

func isSQLIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_' || r == '$'
}

func isSQLIdentCont(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
}

var (
	errEmptySQL       = sqlError("empty sql")
	errWriteSQL       = sqlError("SQL not allowed — db_query permits only SELECT, WITH…SELECT, SHOW, DESCRIBE/DESC, or EXPLAIN…SELECT")
	errMultiStatement = sqlError("multi-statement SQL blocked")
)

type sqlError string

func (e sqlError) Error() string { return string(e) }
