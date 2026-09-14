package core_test

// psql is exercised against an in-test fake PostgreSQL v3 wire server: it
// performs the startup handshake (cleartext / MD5 / SCRAM-SHA-256 auth), then
// answers simple queries with RowDescription/DataRow/CommandComplete and
// ErrorResponse paths.

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"winux/core"
)

type pgFake struct {
	ln       net.Listener
	mu       sync.Mutex
	queries  []string
	authMode byte // 0 = none, 3 = cleartext, 5 = md5, 10 = scram
	password string
}

func newPgFake(t *testing.T, authMode byte, password string) *pgFake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &pgFake{ln: ln, authMode: authMode, password: password}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *pgFake) port(t *testing.T) string {
	t.Helper()
	_, port, err := net.SplitHostPort(f.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func (f *pgFake) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func pgSend(conn net.Conn, typ byte, payload []byte) {
	msg := []byte{typ}
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(payload)+4))
	msg = append(msg, l[:]...)
	msg = append(msg, payload...)
	conn.Write(msg)
}

func pgSendRaw(conn net.Conn, payload []byte) { conn.Write(payload) }

func pgReadMsg(conn net.Conn) (byte, []byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(hdr[1:]))
	payload := make([]byte, n-4)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

func pgRowDesc(cols ...string) []byte {
	var b []byte
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(cols)))
	b = append(b, l[:]...)
	for _, c := range cols {
		b = append(b, c...)
		b = append(b, 0)
		b = append(b, make([]byte, 18)...) // type info, unused by the client
	}
	return b
}

func pgDataRow(vals ...string) []byte {
	var b []byte
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(vals)))
	b = append(b, l[:]...)
	for _, v := range vals {
		var vl [4]byte
		if v == "" {
			binary.BigEndian.PutUint32(vl[:], 0)
		} else {
			binary.BigEndian.PutUint32(vl[:], uint32(len(v)))
			b = append(b, vl[:]...)
			b = append(b, v...)
			continue
		}
		b = append(b, vl[:]...)
	}
	return b
}

func (f *pgFake) handle(conn net.Conn) {
	defer conn.Close()
	// StartupMessage has no type byte: int32 len + int32 protocol + params.
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return
	}
	n := int(binary.BigEndian.Uint32(hdr))
	body := make([]byte, n-4)
	if _, err := io.ReadFull(conn, body); err != nil {
		return
	}
	params := map[string]string{}
	pos := 4 // skip protocol version
	for pos < len(body) {
		i := strings.IndexByte(string(body[pos:]), 0)
		if i < 0 {
			break
		}
		k := string(body[pos : pos+i])
		pos += i + 1
		j := strings.IndexByte(string(body[pos:]), 0)
		if j < 0 {
			break
		}
		v := string(body[pos : pos+j])
		pos += j + 1
		params[k] = v
	}
	user := params["user"]

	// Authentication.
	switch f.authMode {
	case 3:
		pgSend(conn, 'R', []byte{0, 0, 0, 3})
		typ, payload, err := pgReadMsg(conn)
		if err != nil || typ != 'p' {
			return
		}
		if got := strings.TrimRight(string(payload), "\x00"); got != f.password {
			pgSend(conn, 'E', pgErrFields("ERROR", "28P01", "password authentication failed for user \""+user+"\""))
			return
		}
	case 5:
		salt := []byte{1, 2, 3, 4}
		pgSend(conn, 'R', append([]byte{0, 0, 0, 5}, salt...))
		typ, payload, err := pgReadMsg(conn)
		if err != nil || typ != 'p' {
			return
		}
		h := md5.Sum([]byte(f.password + user))
		inner := hex.EncodeToString(h[:])
		h2 := md5.Sum(append([]byte(inner), salt...))
		want := "md5" + hex.EncodeToString(h2[:])
		if strings.TrimRight(string(payload), "\x00") != want {
			pgSend(conn, 'E', pgErrFields("ERROR", "28P01", "password authentication failed"))
			return
		}
	case 10:
		pgSend(conn, 'R', append([]byte{0, 0, 0, 10}, []byte("SCRAM-SHA-256\x00SCRAM-SHA-256-PLUS\x00")...))
		typ, payload, err := pgReadMsg(conn)
		if err != nil || typ != 'p' {
			return
		}
		// SASLInitialResponse: mechanism\0 + int32 len + client-first
		z := strings.IndexByte(string(payload), 0)
		cfLen := int(binary.BigEndian.Uint32(payload[z+1:]))
		clientFirst := string(payload[z+5 : z+5+cfLen])
		sf, verifier := fakeSCRAMStep(clientFirst)
		pgSend(conn, 'R', append([]byte{0, 0, 0, 11}, []byte(sf)...))
		typ, payload, err = pgReadMsg(conn)
		if err != nil || typ != 'p' {
			return
		}
		v := verifier(string(payload))
		if v == "" {
			pgSend(conn, 'E', pgErrFields("ERROR", "28P01", "password authentication failed"))
			return
		}
		pgSend(conn, 'R', append([]byte{0, 0, 0, 12}, []byte(v)...))
	}
	pgSend(conn, 'R', []byte{0, 0, 0, 0}) // AuthenticationOk
	pgSend(conn, 'S', []byte("server_version\x0015.3\x00"))
	pgSend(conn, 'Z', []byte{'I'})

	for {
		typ, payload, err := pgReadMsg(conn)
		if err != nil {
			return
		}
		switch typ {
		case 'Q':
			sql := strings.TrimRight(string(payload), "\x00")
			f.mu.Lock()
			f.queries = append(f.queries, sql)
			f.mu.Unlock()
			switch {
			case strings.Contains(sql, "nope"):
				pgSend(conn, 'E', pgErrFields("ERROR", "42P01", "relation \"nope\" does not exist"))
			case strings.Contains(sql, "SELECT 1; SELECT 2"):
				pgSend(conn, 'T', pgRowDesc("?column?"))
				pgSend(conn, 'D', pgDataRow("1"))
				pgSend(conn, 'C', []byte("SELECT 1\x00"))
				pgSend(conn, 'T', pgRowDesc("?column?"))
				pgSend(conn, 'D', pgDataRow("2"))
				pgSend(conn, 'C', []byte("SELECT 1\x00"))
			case strings.Contains(sql, "SELECT 1"):
				pgSend(conn, 'T', pgRowDesc("?column?"))
				pgSend(conn, 'D', pgDataRow("1"))
				pgSend(conn, 'C', []byte("SELECT 1\x00"))
			case strings.Contains(sql, "SELECT 2"):
				pgSend(conn, 'T', pgRowDesc("?column?"))
				pgSend(conn, 'D', pgDataRow("2"))
				pgSend(conn, 'C', []byte("SELECT 1\x00"))
			case strings.Contains(sql, "INSERT"):
				pgSend(conn, 'C', []byte("INSERT 0 1\x00"))
			default:
				pgSend(conn, 'C', []byte("SELECT 0\x00"))
			}
			pgSend(conn, 'Z', []byte{'I'})
		case 'X':
			return
		}
	}
}

func pgErrFields(severity, code, msg string) []byte {
	var b []byte
	b = append(b, 'S')
	b = append(b, severity...)
	b = append(b, 0)
	b = append(b, 'C')
	b = append(b, code...)
	b = append(b, 0)
	b = append(b, 'M')
	b = append(b, msg...)
	b = append(b, 0)
	b = append(b, 0)
	return b
}

func TestPsqlQuery(t *testing.T) {
	f := newPgFake(t, 0, "")
	code, out, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-U", "postgres", "-q", "-c", "SELECT 1"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "?column?") || !strings.Contains(out, "1") {
		t.Fatalf("out=%q", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) != 1 || f.queries[0] != "SELECT 1" {
		t.Fatalf("queries=%v", f.queries)
	}
}

func TestPsqlTuplesOnly(t *testing.T) {
	f := newPgFake(t, 0, "")
	code, out, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q", "-t", "-c", "SELECT 1"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if out != "1\n" {
		t.Fatalf("out=%q want %q", out, "1\n")
	}
}

func TestPsqlNoAlign(t *testing.T) {
	f := newPgFake(t, 0, "")
	code, out, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q", "-A", "-c", "SELECT 1"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if out != "1\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestPsqlInsertTag(t *testing.T) {
	f := newPgFake(t, 0, "")
	code, out, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q", "-c", "INSERT INTO t VALUES (1)"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "INSERT 0 1") {
		t.Fatalf("out=%q", out)
	}
}

func TestPsqlError(t *testing.T) {
	f := newPgFake(t, 0, "")
	code, _, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q", "-c", "SELECT * FROM nope"},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errOut, "does not exist") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestPsqlStdin(t *testing.T) {
	f := newPgFake(t, 0, "")
	code, out, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q"},
		strings.NewReader("SELECT 1;\n"))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "1") {
		t.Fatalf("out=%q", out)
	}
}

func TestPsqlMD5Auth(t *testing.T) {
	t.Setenv("PGPASSWORD", "hunter2")
	f := newPgFake(t, 5, "hunter2")
	code, _, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-U", "postgres", "-q", "-c", "SELECT 1"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestPsqlMD5AuthBadPassword(t *testing.T) {
	f := newPgFake(t, 5, "hunter2")
	code, _, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-U", "postgres", "-q", "-w", "-c", "SELECT 1"},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errOut, "password required") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestPsqlScramAuth(t *testing.T) {
	t.Setenv("PGPASSWORD", "secretpw")
	f := newPgFake(t, 10, "secretpw")
	code, _, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-U", "alice", "-q", "-c", "SELECT 1"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestPsqlConnectionRefused(t *testing.T) {
	code, _, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", "1", "--timeout", "2", "-q", "-c", "SELECT 1"},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errOut, "psql:") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestPsqlBadArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"unknown option", []string{"--nope"}, 2},
		{"bad port", []string{"--port", "abc"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := core.Execute("psql", tc.args, strings.NewReader(""))
			if code != tc.code {
				t.Fatalf("code=%d want %d", code, tc.code)
			}
		})
	}
}

func TestPsqlMultipleResults(t *testing.T) {
	f := newPgFake(t, 0, "")
	code, out, errOut := core.Execute("psql",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q", "-c", "SELECT 1; SELECT 2"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "?column?") || !strings.Contains(out, "1") || !strings.Contains(out, "2") {
		t.Fatalf("out=%q", out)
	}
}

var _ = fmt.Sprintf
