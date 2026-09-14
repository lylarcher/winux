package core_test

// clickhouse is exercised against an in-test fake ClickHouse HTTP endpoint
// (httptest): request shape (POST body, auth headers, database/default_format
// parameters), stdin/--query batch modes, multi-statement splitting, the
// server-error path and argument validation. No real ClickHouse is needed.

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"winux/core"
)

type chFakeState struct {
	mu       sync.Mutex
	requests int
	body     string
	user     string
	key      string
	database string
	format   string
}

func newClickhouseFake(t *testing.T) (*httptest.Server, *chFakeState) {
	t.Helper()
	st := &chFakeState{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		st.mu.Lock()
		st.requests++
		st.body = string(body)
		st.user = r.Header.Get("X-ClickHouse-User")
		st.key = r.Header.Get("X-ClickHouse-Key")
		st.database = r.URL.Query().Get("database")
		st.format = r.URL.Query().Get("default_format")
		st.mu.Unlock()
		if strings.Contains(string(body), "FAIL") {
			w.Header().Set("X-ClickHouse-Exception-Code", "60")
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Code: 60. DB::Exception: Table does not exist."))
			return
		}
		w.Write([]byte("1\n"))
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

func chPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
}

func TestClickhouseQuery(t *testing.T) {
	srv, st := newClickhouseFake(t)
	code, out, errOut := core.Execute("clickhouse",
		[]string{"-h", "127.0.0.1", "--port", chPort(t, srv), "-u", "alice", "--password", "s3cret", "-d", "analytics", "-q", "SELECT 1"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if out != "1\n" {
		t.Fatalf("out=%q want %q", out, "1\n")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.requests != 1 || st.body != "SELECT 1" {
		t.Fatalf("requests=%d body=%q", st.requests, st.body)
	}
	if st.user != "alice" || st.key != "s3cret" {
		t.Fatalf("auth headers user=%q key=%q", st.user, st.key)
	}
	if st.database != "analytics" {
		t.Fatalf("database param=%q", st.database)
	}
	if st.format != "TabSeparated" {
		t.Fatalf("format param=%q want TabSeparated (non-tty)", st.format)
	}
}

func TestClickhouseQueryRepeatable(t *testing.T) {
	srv, st := newClickhouseFake(t)
	code, out, errOut := core.Execute("clickhouse",
		[]string{"--port", chPort(t, srv), "-q", "SELECT 1", "-q", "SELECT 2"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if out != "1\n1\n" {
		t.Fatalf("out=%q", out)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.requests != 2 {
		t.Fatalf("requests=%d want 2", st.requests)
	}
}

func TestClickhouseMultiStatement(t *testing.T) {
	srv, st := newClickhouseFake(t)
	code, _, errOut := core.Execute("clickhouse",
		[]string{"--port", chPort(t, srv), "-q", "SELECT 1; SELECT 'a;b'; SELECT 3"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.requests != 3 {
		t.Fatalf("requests=%d want 3 (quote-aware split)", st.requests)
	}
}

func TestClickhouseStdin(t *testing.T) {
	srv, st := newClickhouseFake(t)
	code, out, errOut := core.Execute("clickhouse",
		[]string{"-h", "127.0.0.1", "--port", chPort(t, srv)},
		strings.NewReader("SELECT 7;\n"))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if out != "1\n" {
		t.Fatalf("out=%q", out)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.body != "SELECT 7" {
		t.Fatalf("body=%q", st.body)
	}
}

func TestClickhouseClientSubcommand(t *testing.T) {
	srv, st := newClickhouseFake(t)
	code, _, errOut := core.Execute("clickhouse",
		[]string{"client", "--port", chPort(t, srv), "--query", "SELECT 1"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.requests != 1 {
		t.Fatalf("requests=%d want 1", st.requests)
	}
}

func TestClickhouseError(t *testing.T) {
	srv, _ := newClickhouseFake(t)
	code, _, errOut := core.Execute("clickhouse",
		[]string{"--port", chPort(t, srv), "-q", "SELECT FAIL"},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errOut, "code 60") || !strings.Contains(errOut, "Table does not exist") {
		t.Fatalf("stderr=%q missing the exception details", errOut)
	}
}

func TestClickhouseConnectionRefused(t *testing.T) {
	code, _, errOut := core.Execute("clickhouse",
		[]string{"-h", "127.0.0.1", "--port", "1", "--timeout", "2", "-q", "SELECT 1"},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errOut, "clickhouse:") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestClickhouseBadArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"unknown option", []string{"--nope"}, 2},
		{"port missing value", []string{"--port"}, 2},
		{"bad port", []string{"--port", "abc"}, 1},
		{"unexpected positional", []string{"client", "extra", "-q", "SELECT 1"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := core.Execute("clickhouse", tc.args, strings.NewReader(""))
			if code != tc.code {
				t.Fatalf("code=%d want %d", code, tc.code)
			}
		})
	}
}

func TestClickhouseHelp(t *testing.T) {
	code, out, errOut := core.Execute("clickhouse", []string{"--help"}, strings.NewReader(""))
	if code != 0 || !strings.Contains(out, "Usage: clickhouse") || !strings.Contains(out, "--query") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}
