package core

// sqlite is a SQLite client built on the pure-Go modernc.org/sqlite driver
// (no sqlite3 CLI or CGO needed). It mirrors the sqlite3 shell: a database
// file positional argument (or :memory:), an optional SQL argument, an
// interactive REPL with dot-commands (.tables/.schema/.dump/...), and
// column/list/json/line output modes.
//
//	sqlite test.db "SELECT * FROM users"
//	echo "CREATE TABLE t(a); INSERT INTO t VALUES(1);" | sqlite test.db
//	sqlite -header -column test.db
//
// The database file is opened read-write unless -readonly is given.

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite")
)

func init() {
	addCmd("sqlite", "SQLite client (pure Go, no sqlite3 CLI needed)", cmdSqlite)
}

func cmdSqlite(args []string) int {
	f := NewFlags(
		&Flag{Name: "header", Short: 'H', Long: "header", Help: "show column headers (default on in table mode)"},
		&Flag{Name: "list", Long: "list", Help: "list mode: one field per line separated by | or -separator"},
		&Flag{Name: "json", Long: "json", Help: "JSON output: one array of objects per result set"},
		&Flag{Name: "line", Long: "line", Help: "line mode: one field = value per line"},
		&Flag{Name: "column", Long: "column", Help: "column mode (default): aligned table with headers"},
		&Flag{Name: "separator", Long: "separator", HasArg: true, ArgName: "SEP", Help: "field separator for list mode (default: |)"},
		&Flag{Name: "nullvalue", Long: "nullvalue", HasArg: true, ArgName: "VALUE", Help: "text shown for NULL values (default: empty)"},
		&Flag{Name: "readonly", Short: 'r', Long: "readonly", Help: "open the database read-only"},
		&Flag{Name: "cmd", Long: "cmd", HasArg: true, ArgName: "SQL", Help: "run SQL before the file/stdin input (repeatable)"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "echo every SQL statement to stderr before running it"},
		&Flag{Name: "echo", Short: 'e', Long: "echo", Help: "alias of --verbose"},
		&Flag{Name: "noheader", Short: 'N', Long: "noheader", Help: "suppress column headers in table mode"},
	)
	if !parseOrDie("sqlite", f, args) {
		return 2
	}
	if handleHelpVersion("sqlite", "[OPTIONS] [DBFILE] [SQL]", f) {
		return 0
	}

	pos := f.Positional
	dbFile := ":memory:"
	if len(pos) > 0 {
		dbFile = pos[0]
	}
	oneShotSQL := ""
	if len(pos) > 1 {
		oneShotSQL = strings.Join(pos[1:], " ")
	}

	mode := "column"
	switch {
	case f.Has("list"):
		mode = "list"
	case f.Has("json"):
		mode = "json"
	case f.Has("line"):
		mode = "line"
	}
	separator := "|"
	if f.Has("separator") {
		separator = f.Str("separator")
	}
	nullValue := ""
	if f.Has("nullvalue") {
		nullValue = f.Str("nullvalue")
	}
	header := !f.Has("noheader")
	verbose := f.Has("verbose") || f.Has("echo")
	readOnly := f.Has("readonly")

	dsn := dbFile
	if readOnly {
		if strings.HasPrefix(dbFile, "file:") {
			dsn += "&mode=ro"
		} else {
			dsn = "file:" + dbFile + "?mode=ro"
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		warn("sqlite", "%v", err)
		return 1
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		warn("sqlite", "cannot open %s: %v", dbFile, err)
		return 1
	}

	env := &sqliteEnv{db: db, mode: mode, separator: separator, nullValue: nullValue, header: header, verbose: verbose}

	for _, c := range f.Strs("cmd") {
		if err := sqliteExec(env, c); err != nil {
			fmt.Fprintf(os.Stderr, "sqlite: error: %s\n", err)
			return 1
		}
	}

	if oneShotSQL != "" {
		if err := sqliteExec(env, oneShotSQL); err != nil {
			fmt.Fprintf(os.Stderr, "sqlite: error: %s\n", err)
			return 1
		}
		return 0
	}
	if !isTerminal(os.Stdin) {
		data, err := io.ReadAll(stripBOM(os.Stdin))
		if err != nil {
			warn("sqlite", "%v", err)
			return 1
		}
		if err := sqliteExec(env, string(data)); err != nil {
			fmt.Fprintf(os.Stderr, "sqlite: error: %s\n", err)
			return 1
		}
		return 0
	}
	return sqliteRepl(env)
}

type sqliteEnv struct {
	db        *sql.DB
	mode      string
	separator string
	nullValue string
	header    bool
	verbose   bool
}

// sqliteExec runs one or more statements (quote-aware split, like mysql),
// including a trailing statement that has no terminator.
func sqliteExec(env *sqliteEnv, sql string) error {
	stmts := splitExecStatements(sql)
	if len(stmts) == 0 {
		if strings.TrimSpace(sql) != "" {
			return sqliteRunOne(env, strings.TrimSpace(sql))
		}
		return nil
	}
	for _, st := range stmts {
		if err := sqliteRunOne(env, st.text); err != nil {
			return err
		}
	}
	return nil
}

func sqliteRunOne(env *sqliteEnv, sql string) error {
	if strings.TrimSpace(sql) == "" {
		return nil
	}
	if env.verbose {
		fmt.Fprintf(os.Stderr, "sqlite: %s\n", strings.TrimSpace(sql))
	}
	verb := strings.ToUpper(strings.Fields(sql)[0])
	switch verb {
	case "SELECT", "PRAGMA", "EXPLAIN", "WITH":
		return sqliteQuery(env, sql)
	case "INSERT", "UPDATE", "DELETE", "REPLACE":
		return sqliteExecResult(env, sql)
	default:
		// DDL and everything else runs silently, like the sqlite3 shell.
		_, err := env.db.Exec(sql)
		return err
	}
}

func sqliteQuery(env *sqliteEnv, sql string) error {
	rows, err := env.db.Query(sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name()
	}
	var data [][]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := make([]string, len(vals))
		for i, v := range vals {
			row[i] = sqliteString(env, v)
		}
		data = append(data, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return sqlitePrint(env, names, data)
}

func sqliteExecResult(env *sqliteEnv, sql string) error {
	res, err := env.db.Exec(sql)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		fmt.Fprintln(os.Stdout, "Query OK, 1 row affected")
	} else {
		fmt.Fprintf(os.Stdout, "Query OK, %d rows affected\n", n)
	}
	return nil
}

func sqliteString(env *sqliteEnv, v any) string {
	if v == nil {
		return env.nullValue
	}
	switch t := v.(type) {
	case []byte:
		return string(t)
	case timeType:
		return t.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// timeType lets us render driver-specific time values without importing them.
type timeType interface{ String() string }

func sqlitePrint(env *sqliteEnv, names []string, rows [][]string) error {
	switch env.mode {
	case "json":
		objs := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			m := make(map[string]any, len(names))
			for i, n := range names {
				if i < len(row) {
					m[n] = row[i]
				}
			}
			objs = append(objs, m)
		}
		b, err := json.Marshal(objs)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(b))
	case "line":
		for _, row := range rows {
			for i, n := range names {
				val := ""
				if i < len(row) {
					val = row[i]
				}
				fmt.Fprintf(os.Stdout, "  %s = %s\n", n, val)
			}
			fmt.Fprintln(os.Stdout)
		}
	case "list":
		for _, row := range rows {
			fmt.Fprintln(os.Stdout, strings.Join(row, env.separator))
		}
	default: // column
		pgPrintResult(pgResult{cols: names, rows: rows}, !env.header, false)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Interactive REPL

func sqliteRepl(env *sqliteEnv) int {
	fmt.Fprintf(os.Stderr, "winux sqlite %s (pure-Go driver modernc.org/sqlite) — enter SQL or dot-commands (.help for help)\n", Version)
	in, cleanup := consoleInput()
	defer cleanup()
	rd := bufio.NewReader(stripBOM(in))
	exitCode := 0
	var buf strings.Builder
	for {
		if buf.Len() == 0 {
			fmt.Fprint(os.Stdout, "sqlite> ")
		} else {
			fmt.Fprint(os.Stdout, "   ...> ")
		}
		line, err := rd.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(line)
			if buf.Len() == 0 {
				if strings.HasPrefix(trimmed, ".") {
					if code, done := sqliteDotCommand(env, trimmed); done {
						fmt.Fprintln(os.Stdout)
						return code
					}
					continue
				}
				switch {
				case strings.EqualFold(trimmed, "exit"), strings.EqualFold(trimmed, "quit"):
					fmt.Fprintln(os.Stdout)
					return exitCode
				case trimmed == "":
					continue
				}
			}
			buf.WriteString(line)
			if !strings.HasSuffix(strings.TrimSpace(buf.String()), ";") {
				continue
			}
			sql := strings.TrimSpace(strings.TrimSuffix(buf.String(), ";"))
			buf.Reset()
			if sql != "" {
				if err := sqliteRunOne(env, sql); err != nil {
					fmt.Fprintf(os.Stderr, "sqlite: error: %s\n", err)
					if exitCode == 0 {
						exitCode = 1
					}
				}
			}
		}
		if err == io.EOF {
			fmt.Fprintln(os.Stdout)
			return exitCode
		}
	}
}

// sqliteDotCommand handles a .command line; done means "quit the REPL".
func sqliteDotCommand(env *sqliteEnv, line string) (code int, done bool) {
	fields := strings.Fields(line)
	cmd := fields[0]
	arg := ""
	if len(fields) > 1 {
		arg = fields[1]
	}
	switch cmd {
	case ".quit", ".exit":
		return 0, true
	case ".help":
		fmt.Fprintln(os.Stdout, ".tables [PATTERN]  .schema [TABLE]  .databases  .indexes [TABLE]  .dump [TABLE]")
		fmt.Fprintln(os.Stdout, ".mode column|list|json|line  .headers on|off  .separator SEP  .nullvalue VALUE")
		fmt.Fprintln(os.Stdout, ".read FILE  .quit / exit")
	case ".tables":
		q := "SELECT name FROM sqlite_master WHERE type IN ('table','view')"
		if arg != "" {
			q += " AND name LIKE '" + strings.ReplaceAll(arg, "'", "''") + "'"
		}
		q += " ORDER BY name"
		_ = sqliteQuery(env, q)
	case ".schema":
		q := "SELECT sql FROM sqlite_master"
		if arg != "" {
			q += " WHERE name = '" + strings.ReplaceAll(arg, "'", "''") + "'"
		}
		_ = sqliteQuery(env, q)
	case ".databases":
		_ = sqliteQuery(env, "PRAGMA database_list")
	case ".indexes":
		q := "SELECT name, tbl_name FROM sqlite_master WHERE type = 'index'"
		if arg != "" {
			q += " AND tbl_name = '" + strings.ReplaceAll(arg, "'", "''") + "'"
		}
		_ = sqliteQuery(env, q)
	case ".dump":
		_ = sqliteQuery(env, "SELECT sql FROM sqlite_master WHERE sql IS NOT NULL")
	case ".mode":
		switch arg {
		case "column", "list", "json", "line":
			env.mode = arg
		default:
			fmt.Fprintf(os.Stderr, "sqlite: bad mode '%s' (column|list|json|line)\n", arg)
		}
	case ".headers":
		env.header = arg == "on"
	case ".separator":
		if len(fields) > 1 {
			env.separator = fields[1]
		}
	case ".nullvalue":
		env.nullValue = strings.TrimPrefix(line, ".nullvalue ")
	case ".read":
		if arg == "" {
			fmt.Fprintln(os.Stderr, "sqlite: .read requires a file")
			break
		}
		data, err := os.ReadFile(arg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sqlite: error: %v\n", err)
			break
		}
		if err := sqliteExec(env, string(data)); err != nil {
			fmt.Fprintf(os.Stderr, "sqlite: error: %s\n", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "sqlite: unknown dot-command '%s' (.help for the list)\n", cmd)
	}
	return 0, false
}
