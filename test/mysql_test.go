package core_test

// A small in-test MySQL server speaking just enough of the client/server
// protocol (greeting, auth, COM_QUERY result sets) to exercise the pure-Go
// winux mysql client end to end without installing a real server.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"strings"
	"testing"

	"winux/core"
)

type mockMySQL struct {
	ln       net.Listener
	password string
	plugin   string // mysql_native_password | caching_sha2_password
	seed     []byte
}

const mockSeed = "0123456789abcdef0123"

func newMockMySQL(t *testing.T, password, plugin string) *mockMySQL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock listen: %v", err)
	}
	m := &mockMySQL{ln: ln, password: password, plugin: plugin, seed: []byte(mockSeed)}
	go m.serve()
	t.Cleanup(func() { ln.Close() })
	return m
}

func (m *mockMySQL) addr() string { return m.ln.Addr().String() }

func putLenenc(b []byte, v uint64) []byte {
	switch {
	case v < 0xfb:
		return append(b, byte(v))
	case v <= 0xffff:
		return append(b, 0xfc, byte(v), byte(v>>8))
	case v <= 0xffffff:
		return append(b, 0xfd, byte(v), byte(v>>8), byte(v>>16))
	default:
		return append(b, 0xfe,
			byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
			byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
	}
}

type mconn struct {
	nc  net.Conn
	seq byte
}

func (c *mconn) send(payload []byte) error {
	n := len(payload)
	hdr := []byte{byte(n), byte(n >> 8), byte(n >> 16), c.seq}
	c.seq++
	if _, err := c.nc.Write(hdr); err != nil {
		return err
	}
	_, err := c.nc.Write(payload)
	return err
}

func (c *mconn) recv() ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(c.nc, hdr[:]); err != nil {
		return nil, err
	}
	n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	c.seq = hdr[3]
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.nc, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (c *mconn) sendOK(affected uint64) error {
	p := []byte{0x00}
	p = putLenenc(p, affected)
	p = putLenenc(p, 0)
	p = append(p, 0x02, 0x00, 0x00, 0x00) // status + warnings
	return c.send(p)
}

func (c *mconn) sendErr(code uint16, sqlState, msg string) error {
	p := []byte{0xff, byte(code), byte(code >> 8)}
	p = append(p, '#')
	p = append(p, sqlState...)
	p = append(p, msg...)
	return c.send(p)
}

func (c *mconn) sendResultSet(names []string, rows [][]*string) error {
	if err := c.send([]byte{byte(len(names))}); err != nil {
		return err
	}
	for _, name := range names {
		var col []byte
		for _, s := range []string{"def", "", "", "", name, ""} {
			col = putLenenc(col, uint64(len(s)))
			col = append(col, s...)
		}
		col = putLenenc(col, 0x0c)
		col = append(col, 33, 0, 0, 0, 0, 0, 0xfd, 0, 0, 0, 0, 0, 0) // charset, length, VAR_STRING, flags, decimals, filler
		if err := c.send(col); err != nil {
			return err
		}
	}
	eof := []byte{0xfe, 0x00, 0x00, 0x00, 0x00}
	if err := c.send(eof); err != nil {
		return err
	}
	for _, row := range rows {
		var rp []byte
		for _, v := range row {
			if v == nil {
				rp = append(rp, 0xfb)
			} else {
				rp = putLenenc(rp, uint64(len(*v)))
				rp = append(rp, *v...)
			}
		}
		if err := c.send(rp); err != nil {
			return err
		}
	}
	return c.send(eof)
}

func (m *mockMySQL) serve() {
	for {
		nc, err := m.ln.Accept()
		if err != nil {
			return
		}
		go m.handle(nc)
	}
}

func (m *mockMySQL) handle(nc net.Conn) {
	defer nc.Close()
	c := &mconn{nc: nc, seq: 0}
	// greeting
	var g []byte
	g = append(g, 0x0a)
	g = append(g, []byte("8.0.41-mock")...)
	g = append(g, 0)
	g = append(g, 0x2a, 0x00, 0x00, 0x00) // thread id
	g = append(g, m.seed[:8]...)
	g = append(g, 0)
	caps := uint32(0x00000200 | 0x00008000 | 0x00080000 | 0x00000008) // PROTOCOL_41|SECURE_CONNECTION|PLUGIN_AUTH|CONNECT_WITH_DB
	g = append(g, byte(caps), byte(caps>>8))
	g = append(g, 33, 0x02, 0x00) // charset, status
	g = append(g, byte(caps>>16), byte(caps>>24))
	g = append(g, 21) // auth data length
	g = append(g, make([]byte, 10)...)
	g = append(g, m.seed[8:]...)
	g = append(g, 0)
	g = append(g, []byte(m.plugin)...)
	g = append(g, 0)
	if err := c.send(g); err != nil {
		return
	}
	// client handshake response
	resp, err := c.recv()
	if err != nil {
		return
	}
	off := 4 + 4 + 1 + 23
	userEnd := off
	for resp[userEnd] != 0 {
		userEnd++
	}
	user := string(resp[off:userEnd])
	off = userEnd + 1
	authLen := int(resp[off])
	off++
	scramble := resp[off : off+authLen]
	off += authLen
	// db
	for off < len(resp) && resp[off] != 0 {
		off++
	}
	off++
	plugin := ""
	if off < len(resp) {
		plugin = string(resp[off:])
		if i := strings.IndexByte(plugin, 0); i >= 0 {
			plugin = plugin[:i]
		}
	}

	want := computeScramble(m.password, m.seed, m.plugin)
	if !strings.EqualFold(plugin, m.plugin) || user != "tester" || !equalBytes(scramble, want) {
		c.sendErr(1045, "28000", "Access denied for user 'tester'")
		return
	}
	if m.plugin == "caching_sha2_password" {
		// force the full-auth exchange so the RSA path is exercised
		if err := c.send([]byte{0x04}); err != nil {
			return
		}
		req, err := c.recv()
		if err != nil || len(req) != 1 || req[0] != 0x02 {
			return
		}
		key, _ := rsa.GenerateKey(rand.Reader, 1024)
		der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
		keyPkt := append([]byte{0x01}, pemBytes...)
		if err := c.send(keyPkt); err != nil {
			return
		}
		enc, err := c.recv()
		if err != nil {
			return
		}
		plain, err := rsa.DecryptPKCS1v15(rand.Reader, key, enc)
		for i := range plain { // de-obfuscate: XOR with the seed again
			plain[i] ^= m.seed[i%len(m.seed)]
		}
		if err != nil || !equalBytes(plain, []byte(m.password+"\x00")) {
			c.sendErr(1045, "28000", "Access denied (rsa)")
			return
		}
	}
	if err := c.sendOK(0); err != nil {
		return
	}
	// command loop
	for {
		pkt, err := c.recv()
		if err != nil {
			return
		}
		switch pkt[0] {
		case 0x01: // COM_QUIT
			return
		case 0x0e: // COM_PING
			if c.sendOK(0) != nil {
				return
			}
		case 0x02: // COM_INIT_DB
			if c.sendOK(0) != nil {
				return
			}
		case 0x03: // COM_QUERY
			sql := string(pkt[1:])
			switch {
			case strings.HasPrefix(sql, "SELECT 1"):
				one := "1"
				if c.sendResultSet([]string{"1"}, [][]*string{{&one}}) != nil {
					return
				}
			case strings.HasPrefix(sql, "SELECT name, val"):
				a, b := "alpha", "42"
				var nul *string
				if c.sendResultSet([]string{"name", "val"}, [][]*string{{&a, &b}, {&a, nul}}) != nil {
					return
				}
			case strings.HasPrefix(sql, "INSERT"):
				if c.sendOK(1) != nil {
					return
				}
			case strings.Contains(sql, "bad"):
				if c.sendErr(1064, "42000", "You have an error in your SQL syntax") != nil {
					return
				}
			case strings.HasPrefix(sql, "SHOW DATABASES"):
				db := "testdb"
				if c.sendResultSet([]string{"Database"}, [][]*string{{&db}}) != nil {
					return
				}
			default:
				if c.sendOK(0) != nil {
					return
				}
			}
		default:
			return
		}
	}
}

func computeScramble(password string, seed []byte, plugin string) []byte {
	p := []byte(password)
	switch plugin {
	case "caching_sha2_password":
		h1 := sha256.Sum256(p)
		h2 := sha256.Sum256(h1[:])
		inner := sha256.New()
		inner.Write(h2[:])
		inner.Write(seed)
		digest := inner.Sum(nil)
		out := make([]byte, len(h1))
		for i := range h1 {
			out[i] = h1[i] ^ digest[i]
		}
		return out
	default:
		s1 := sha1.Sum(p)
		s2 := sha1.Sum(s1[:])
		h := sha1.New()
		h.Write(seed)
		h.Write(s2[:])
		digest := h.Sum(nil)
		out := make([]byte, len(s1))
		for i := range s1 {
			out[i] = s1[i] ^ digest[i]
		}
		return out
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func runMysql(t *testing.T, m *mockMySQL, args ...string) (int, string, string) {
	t.Helper()
	full := append([]string{"-h", "127.0.0.1", "-P", strings.TrimPrefix(m.addr(), "127.0.0.1:")}, args...)
	return core.Execute("mysql", full, strings.NewReader(""))
}

func TestMysqlNativeAuthAndQuery(t *testing.T) {
	m := newMockMySQL(t, "secret", "mysql_native_password")
	code, out, errOut := runMysql(t, m, "-u", "tester", "-psecret", "-e", "SELECT 1")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "| 1 |") || !strings.Contains(out, "1 row in set") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestMysqlCachingSha2FullAuth(t *testing.T) {
	m := newMockMySQL(t, "secret", "caching_sha2_password")
	code, out, errOut := runMysql(t, m, "-u", "tester", "-psecret", "-e", "SELECT 1")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "| 1 |") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestMysqlWrongPassword(t *testing.T) {
	m := newMockMySQL(t, "secret", "mysql_native_password")
	code, _, errOut := runMysql(t, m, "-u", "tester", "-pwrong", "-e", "SELECT 1")
	if code != 1 || !strings.Contains(errOut, "1045") || !strings.Contains(errOut, "Access denied") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestMysqlBatchNullAndErrors(t *testing.T) {
	m := newMockMySQL(t, "secret", "mysql_native_password")
	// batch mode: tab-separated, NULL rendered
	code, out, errOut := runMysql(t, m, "-u", "tester", "-psecret", "-B", "-e", "SELECT name, val")
	if code != 0 || !strings.Contains(out, "alpha\t42") || !strings.Contains(out, "alpha\tNULL") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	// server error surfaces as ERROR 1064 and exit code 1
	code, _, errOut = runMysql(t, m, "-u", "tester", "-psecret", "-e", "SELECT bad syntax")
	if code != 1 || !strings.Contains(errOut, "ERROR 1064 (42000)") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	// multiple statements in one -e
	code, out, errOut = runMysql(t, m, "-u", "tester", "-psecret", "-e", "SELECT 1; INSERT INTO t VALUES (1)")
	if code != 0 || !strings.Contains(out, "Query OK, 1 row(s) affected") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	// skip column names: values print, the "Database" header does not
	code, out, _ = runMysql(t, m, "-u", "tester", "-psecret", "-N", "-e", "SHOW DATABASES")
	if code != 0 || !strings.Contains(out, "testdb") || strings.Contains(out, "Database") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	// vertical output via -e ...\G
	code, out, _ = runMysql(t, m, "-u", "tester", "-psecret", "-e", "SELECT name, val\\G")
	if code != 0 || !strings.Contains(out, "1. row") || !strings.Contains(out, "name: alpha") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestMysqlVerticalAndDatabase(t *testing.T) {
	m := newMockMySQL(t, "secret", "mysql_native_password")
	code, out, errOut := runMysql(t, m, "-u", "tester", "-psecret", "-D", "testdb", "-e", "SELECT name, val\\G")
	if code != 0 || !strings.Contains(out, "1. row") || !strings.Contains(out, "name: alpha") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestMysqlConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strings.TrimPrefix(ln.Addr().String(), "127.0.0.1:")
	ln.Close()
	code, _, errOut := core.Execute("mysql", []string{"-h", "127.0.0.1", "-P", port, "-u", "x", "-e", "SELECT 1"}, strings.NewReader(""))
	if code != 1 || errOut == "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}
