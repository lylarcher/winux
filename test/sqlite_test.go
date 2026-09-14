package core_test

// sqlite runs against the real pure-Go modernc.org/sqlite engine in temp
// files: schema/data round trips, the output modes (column/json/line/list),
// null rendering, read-only opening, one-shot SQL and error paths.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"winux/core"
)

func sqliteDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.db")
}

func TestSqliteCreateInsertSelect(t *testing.T) {
	db := sqliteDB(t)
	code, out, errOut := core.Execute("sqlite",
		[]string{db},
		strings.NewReader("CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT);\nINSERT INTO users(name) VALUES ('alice');\nINSERT INTO users(name) VALUES ('bob');\nSELECT id, name FROM users;\n"))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "name") || !strings.Contains(out, "alice") || !strings.Contains(out, "bob") {
		t.Fatalf("out=%q", out)
	}
}

func TestSqliteInMemory(t *testing.T) {
	code, out, errOut := core.Execute("sqlite", []string{"--json", ":memory:", "SELECT 1 AS one"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, `"one"`) || !strings.Contains(out, `"1"`) {
		t.Fatalf("out=%q", out)
	}
}

func TestSqliteOneShotSQL(t *testing.T) {
	db := sqliteDB(t)
	code, out, errOut := core.Execute("sqlite",
		[]string{db, "CREATE TABLE t(a); INSERT INTO t VALUES (7); SELECT a FROM t;"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "7") {
		t.Fatalf("out=%q", out)
	}
}

func TestSqliteJSONMode(t *testing.T) {
	db := sqliteDB(t)
	code, out, errOut := core.Execute("sqlite",
		[]string{"--json", db, "CREATE TABLE t(a); INSERT INTO t VALUES (1),(2); SELECT a FROM t;"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, `[`) || !strings.Contains(out, `{"a":"1"}`) {
		t.Fatalf("out=%q", out)
	}
}

func TestSqliteLineMode(t *testing.T) {
	db := sqliteDB(t)
	code, out, errOut := core.Execute("sqlite",
		[]string{"--line", db, "CREATE TABLE t(a); INSERT INTO t VALUES (5); SELECT a FROM t;"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "a = 5") {
		t.Fatalf("out=%q", out)
	}
}

func TestSqliteListSeparator(t *testing.T) {
	db := sqliteDB(t)
	code, out, errOut := core.Execute("sqlite",
		[]string{"--list", "--separator", ",", db, "CREATE TABLE t(a,b); INSERT INTO t VALUES (1,'x'); SELECT * FROM t;"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "1,x\n") {
		t.Fatalf("out=%q", out)
	}
}

func TestSqliteNullValue(t *testing.T) {
	db := sqliteDB(t)
	code, out, errOut := core.Execute("sqlite",
		[]string{"--nullvalue", "NULL", db, "CREATE TABLE t(a); INSERT INTO t VALUES (NULL); SELECT a FROM t;"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "NULL") {
		t.Fatalf("out=%q", out)
	}
}

func TestSqliteReadonlyBlocksWrites(t *testing.T) {
	db := sqliteDB(t)
	if code, _, errOut := core.Execute("sqlite", []string{db, "CREATE TABLE t(a); INSERT INTO t VALUES (1);"}, strings.NewReader("")); code != 0 {
		t.Fatalf("seed failed: %s", errOut)
	}
	code, _, errOut := core.Execute("sqlite",
		[]string{"--readonly", db, "INSERT INTO t VALUES (2);"},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1 (readonly write must fail)", code)
	}
	if !strings.Contains(strings.ToLower(errOut), "readonly") && !strings.Contains(strings.ToLower(errOut), "read-only") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestSqliteReadonlyQuery(t *testing.T) {
	db := sqliteDB(t)
	if code, _, errOut := core.Execute("sqlite", []string{db, "CREATE TABLE t(a); INSERT INTO t VALUES (42);"}, strings.NewReader("")); code != 0 {
		t.Fatalf("seed failed: %s", errOut)
	}
	code, out, errOut := core.Execute("sqlite",
		[]string{"--readonly", db, "SELECT a FROM t;"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "42") {
		t.Fatalf("out=%q", out)
	}
}

func TestSqliteError(t *testing.T) {
	code, _, errOut := core.Execute("sqlite",
		[]string{":memory:", "SELECT * FROM nope;"},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errOut, "no such table") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestSqliteBadArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"unknown option", []string{"--nope"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := core.Execute("sqlite", tc.args, strings.NewReader(""))
			if code != tc.code {
				t.Fatalf("code=%d want %d", code, tc.code)
			}
		})
	}
}

func TestSqliteHelp(t *testing.T) {
	code, out, errOut := core.Execute("sqlite", []string{"--help"}, strings.NewReader(""))
	if code != 0 || !strings.Contains(out, "Usage: sqlite") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestSqliteOpenMissingFile(t *testing.T) {
	code, _, errOut := core.Execute("sqlite",
		[]string{filepath.Join(t.TempDir(), "no-such-dir", "x.db"), "SELECT 1;"},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errOut, "cannot open") {
		t.Fatalf("stderr=%q", errOut)
	}
}

var _ = os.Getenv
