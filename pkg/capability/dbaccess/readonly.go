package dbaccess

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"modernc.org/sqlite"
)

// QueryReadOnly runs query on db for a db-read grant and hands its rows
// to read. What holds the query to reading is the database, not the
// text: ValidateReadOnlyQuery is only the friendly early refusal, and a
// query it wrongly passed must still not change anything. So the query
// runs
//
//   - on a connection of its own, which is never handed back to the
//     pool: whatever connection state the query manages to set (a
//     PRAGMA, a SET SESSION) dies with it instead of reaching the next
//     caller;
//   - in a transaction opened read-only -- BEGIN READ ONLY on Postgres,
//     which refuses any write inside it -- and always rolled back;
//   - on SQLite, which has no read-only transaction, with PRAGMA
//     query_only on and a commit hook that turns any commit on the
//     connection into a rollback, so not even a statement that ends the
//     transaction and writes in autocommit can make a change stick.
func QueryReadOnly(ctx context.Context, db *sql.DB, query string, params []any, read func(*sql.Rows) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	isSQLite := false
	defer func() {
		// driver.ErrBadConn from Raw is how database/sql is told to close
		// the connection rather than pool it.
		_ = conn.Raw(func(dc any) error {
			if h, ok := dc.(sqlite.HookRegisterer); ok {
				h.RegisterCommitHook(nil)
			}
			return driver.ErrBadConn
		})
		_ = conn.Close()
	}()
	if err := conn.Raw(func(dc any) error {
		if h, ok := dc.(sqlite.HookRegisterer); ok {
			isSQLite = true
			h.RegisterCommitHook(func() int32 { return 1 })
		}
		return nil
	}); err != nil {
		return err
	}
	if isSQLite {
		if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
			return fmt.Errorf("making the connection read-only: %w", err)
		}
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("opening a read-only transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, query, params...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return read(rows)
}

// ValidateReadOnlyQuery checks that query is a single read-only
// statement. It reads the query as tokens rather than as text, so
// keywords are found whatever whitespace separates them and a keyword
// inside a string literal does not count. Where SQLite and Postgres
// would read the text differently it refuses rather than pick one.
//
// It is a friendly early error, not the enforcement: QueryReadOnly is
// what keeps a db-read query from writing.
func ValidateReadOnlyQuery(query string) error {
	if strings.TrimSpace(query) == "" {
		return errors.New("empty query")
	}
	toks, err := tokenizeSQL(query)
	if err != nil {
		return err
	}
	if len(toks) == 0 {
		return errors.New("query contains only comments")
	}
	for i, t := range toks {
		if t.punct != ';' {
			continue
		}
		for _, rest := range toks[i+1:] {
			if rest.punct != ';' {
				return errors.New("only one statement is permitted in a read-only query")
			}
		}
		toks = toks[:i]
		break
	}
	if len(toks) == 0 {
		return errors.New("empty query")
	}

	switch toks[0].word {
	case "SELECT", "WITH", "EXPLAIN":
	case "PRAGMA":
		return validatePragma(toks[1:])
	default:
		return errors.New("only read-only queries (SELECT, WITH, EXPLAIN, PRAGMA) are permitted with db-read capability")
	}

	for i, t := range toks {
		if !disallowedWords[t.word] {
			continue
		}
		// replace(x, y, z) is a string function, not REPLACE INTO.
		if t.word == "REPLACE" && i+1 < len(toks) && toks[i+1].punct == '(' {
			continue
		}
		return fmt.Errorf("mutating keyword %q is not permitted in read-only queries", t.word)
	}
	return nil
}

// disallowedWords are the keywords that can put a write, or a statement
// other than a read, inside a query that starts out as a read: a
// data-modifying CTE (WITH x AS (...) DELETE ...), EXPLAIN ANALYZE of a
// write, SELECT ... INTO a new table.
var disallowedWords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "REPLACE": true,
	"DROP": true, "ALTER": true, "CREATE": true, "TRUNCATE": true, "INTO": true,
	"ATTACH": true, "DETACH": true, "GRANT": true, "REVOKE": true, "VACUUM": true,
	"REINDEX": true, "EXECUTE": true, "DECLARE": true, "COPY": true, "CALL": true,
	"LOAD_EXTENSION": true,
}

// readOnlyPragmas are the PRAGMAs a db-read query may run: the ones
// that only report. A PRAGMA not listed here -- wal_checkpoint,
// incremental_vacuum, optimize -- can change the database or the
// connection even with no argument, so it is refused rather than
// guessed at. The value is whether it takes a parenthesised argument,
// which for these names is a table, index or schema to report on.
var readOnlyPragmas = map[string]bool{
	"TABLE_INFO": true, "TABLE_XINFO": true, "TABLE_LIST": true,
	"INDEX_LIST": true, "INDEX_INFO": true, "INDEX_XINFO": true,
	"FOREIGN_KEY_LIST": true, "FOREIGN_KEY_CHECK": true,
	"INTEGRITY_CHECK": true, "QUICK_CHECK": true,
	"DATABASE_LIST": false, "COLLATION_LIST": false, "FUNCTION_LIST": false,
	"MODULE_LIST": false, "PRAGMA_LIST": false, "COMPILE_OPTIONS": false,
	"USER_VERSION": false, "SCHEMA_VERSION": false, "APPLICATION_ID": false,
	"DATA_VERSION": false, "PAGE_COUNT": false, "PAGE_SIZE": false,
	"FREELIST_COUNT": false, "JOURNAL_MODE": false, "ENCODING": false,
	"FOREIGN_KEYS": false,
}

// validatePragma checks the tokens after PRAGMA: an optional schema
// prefix, a reporting PRAGMA's name, and at most one argument in
// parentheses for the ones that take a table. An assignment (= value)
// or a call form on any other PRAGMA sets it, and is refused.
func validatePragma(toks []sqlToken) error {
	if len(toks) >= 2 && toks[0].word != "" && toks[1].punct == '.' {
		toks = toks[2:]
	}
	if len(toks) == 0 || toks[0].word == "" {
		return errors.New("PRAGMA needs a name")
	}
	name := toks[0].word
	takesArg, ok := readOnlyPragmas[name]
	if !ok {
		return fmt.Errorf("PRAGMA %s is not permitted in read-only queries", strings.ToLower(name))
	}
	rest := toks[1:]
	for _, t := range rest {
		if t.punct == '=' {
			return fmt.Errorf("setting PRAGMA %s is not permitted in read-only queries", strings.ToLower(name))
		}
	}
	switch {
	case len(rest) == 0:
		return nil
	case takesArg && len(rest) == 3 && rest[0].punct == '(' && rest[2].punct == ')':
		return nil
	case takesArg && len(rest) == 2 && rest[0].punct == '(' && rest[1].punct == ')':
		return nil
	}
	return fmt.Errorf("PRAGMA %s may only be read in a read-only query", strings.ToLower(name))
}

// sqlToken is one token of a query as ValidateReadOnlyQuery reads it:
// a word -- keyword or bare identifier, upper-cased -- or one
// punctuation byte. A string literal or quoted identifier is a single
// token with neither set, and comments are dropped.
type sqlToken struct {
	word  string
	punct byte
}

// tokenizeSQL splits q into tokens. It refuses the constructs on which
// SQLite and Postgres disagree about where a string or comment ends
// (backslash escapes, dollar quoting, nested comments, a carriage
// return in a line comment), because a validator that reads them one
// way can be walked past by a database reading them the other.
func tokenizeSQL(q string) ([]sqlToken, error) {
	var toks []sqlToken
	n := len(q)
	for i := 0; i < n; {
		c := q[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < n && q[i+1] == '-':
			end := strings.IndexByte(q[i:], '\n')
			if end == -1 {
				end = n - i
			}
			if strings.IndexByte(q[i:i+end], '\r') != -1 {
				return nil, errors.New("carriage return inside a -- comment")
			}
			i += end
		case c == '/' && i+1 < n && q[i+1] == '*':
			end := strings.Index(q[i+2:], "*/")
			if end == -1 {
				return nil, errors.New("unclosed comment in query")
			}
			if strings.Contains(q[i+2:i+2+end], "/*") {
				return nil, errors.New("nested comments are not permitted in read-only queries")
			}
			i += 2 + end + 2
		case c == '\'' || c == '"':
			end, err := quotedEnd(q, i, c)
			if err != nil {
				return nil, err
			}
			toks = append(toks, sqlToken{})
			i = end
		case c == '`' || c == '[':
			// SQLite quotes identifiers these ways too. What is inside
			// is skipped as a name, so it must hold nothing that could
			// start a string, a comment or a second statement.
			closer := byte('`')
			if c == '[' {
				closer = ']'
			}
			end := strings.IndexByte(q[i+1:], closer)
			if end == -1 {
				return nil, fmt.Errorf("unclosed %c in query", c)
			}
			if strings.ContainsAny(q[i+1:i+1+end], "'\"`[;$-/") {
				return nil, fmt.Errorf("unsupported characters inside %c...%c", c, closer)
			}
			toks = append(toks, sqlToken{})
			i += 1 + end + 1
		case c == '$':
			// $1 and $name are parameters; $tag$ (or $$) opens a
			// Postgres dollar-quoted string, which SQLite does not have.
			// Any $...$ is refused, a few odd parameter spellings with it.
			k := i + 1
			for k < n && isWordByte(q[k]) {
				k++
			}
			if k < n && q[k] == '$' {
				return nil, errors.New("dollar-quoted strings are not permitted in read-only queries")
			}
			toks = append(toks, sqlToken{punct: c})
			i++
		case isWordByte(c):
			start := i
			for i < n && isWordByte(q[i]) {
				i++
			}
			word := strings.ToUpper(q[start:i])
			if word == "E" && i < n && q[i] == '\'' {
				// A Postgres E'...' string, where a backslash escapes
				// the quote; SQLite reads the same backslash literally.
				end, err := quotedEnd(q, i, '\'')
				if err != nil {
					return nil, err
				}
				if strings.IndexByte(q[i:end], '\\') != -1 {
					return nil, errors.New("backslash escapes in E'...' strings are not permitted in read-only queries")
				}
				toks = append(toks, sqlToken{})
				i = end
				continue
			}
			toks = append(toks, sqlToken{word: word})
		default:
			toks = append(toks, sqlToken{punct: c})
			i++
		}
	}
	return toks, nil
}

// quotedEnd returns the index just past the string or identifier that
// opens with quote at q[start], where a doubled quote is an escaped one.
func quotedEnd(q string, start int, quote byte) (int, error) {
	for j := start + 1; j < len(q); j++ {
		if q[j] != quote {
			continue
		}
		if j+1 < len(q) && q[j+1] == quote {
			j++
			continue
		}
		return j + 1, nil
	}
	return 0, fmt.Errorf("unterminated %c in query", quote)
}

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isWordByte reports whether c can be part of a keyword or bare
// identifier. Bytes of a multi-byte UTF-8 character count, as both
// databases allow them in identifiers.
func isWordByte(c byte) bool {
	return isAlpha(c) || (c >= '0' && c <= '9') || c == '_' || c >= 0x80
}
