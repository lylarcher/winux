package core_test

// A tiny in-test RESP server (the subset redis-cli exercises) so the pure-Go
// client is verified end to end without installing a real Redis.

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"

	"winux/core"
)

type mockRedis struct {
	ln       net.Listener
	password string
}

func newMockRedis(t *testing.T, password string) *mockRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock listen: %v", err)
	}
	m := &mockRedis{ln: ln, password: password}
	go m.serve()
	t.Cleanup(func() { ln.Close() })
	return m
}

func (m *mockRedis) addr() string { return m.ln.Addr().String() }

func (m *mockRedis) serve() {
	for {
		nc, err := m.ln.Accept()
		if err != nil {
			return
		}
		go m.handle(nc)
	}
}

func (m *mockRedis) handle(nc net.Conn) {
	defer nc.Close()
	rd := bufio.NewReader(nc)
	authed := m.password == ""
	for {
		args, err := readRespCommand(rd)
		if err != nil {
			return
		}
		if len(args) == 0 {
			return
		}
		cmd := strings.ToUpper(args[0])
		switch cmd {
		case "AUTH":
			if len(args) == 2 && args[1] == m.password {
				authed = true
				fmt.Fprint(nc, "+OK\r\n")
			} else {
				fmt.Fprint(nc, "-ERR invalid password\r\n")
			}
		case "PING":
			if len(args) > 1 {
				fmt.Fprintf(nc, "$%d\r\n%s\r\n", len(args[1]), args[1])
			} else {
				fmt.Fprint(nc, "+PONG\r\n")
			}
		case "SELECT":
			fmt.Fprint(nc, "+OK\r\n")
		case "SET":
			fmt.Fprint(nc, "+OK\r\n")
		case "GET":
			if !authed {
				fmt.Fprint(nc, "-NOAUTH Authentication required.\r\n")
			} else if len(args) == 2 && args[1] == "key" {
				fmt.Fprint(nc, "$3\r\nbar\r\n")
			} else {
				fmt.Fprint(nc, "$-1\r\n")
			}
		case "INCR":
			fmt.Fprint(nc, ":1\r\n")
		case "LRANGE":
			fmt.Fprint(nc, "*3\r\n$2\r\na1\r\n$2\r\na2\r\n$2\r\na3\r\n")
		case "MGET":
			fmt.Fprint(nc, "*-1\r\n")
		case "BADCMD":
			fmt.Fprint(nc, "-ERR unknown command 'BADCMD'\r\n")
		case "QUIT":
			fmt.Fprint(nc, "+OK\r\n")
			return
		default:
			fmt.Fprint(nc, "-ERR unknown command\r\n")
		}
	}
}

func readRespCommand(rd *bufio.Reader) ([]string, error) {
	line, err := rd.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if len(line) == 0 || line[0] != '*' {
		return nil, fmt.Errorf("bad command header %q", line)
	}
	var cnt int
	if _, err := fmt.Sscanf(line[1:], "%d", &cnt); err != nil {
		return nil, fmt.Errorf("bad count %q", line)
	}
	var args []string
	for i := 0; i < cnt; i++ {
		hdr, err := rd.ReadString('\n')
		if err != nil {
			return nil, err
		}
		var blen int
		if _, err := fmt.Sscanf(strings.TrimSuffix(strings.TrimSuffix(hdr, "\n"), "\r"), "$%d", &blen); err != nil {
			return nil, fmt.Errorf("bad bulk header %q", hdr)
		}
		buf := make([]byte, blen+2)
		if _, err := readFull(rd, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:blen]))
	}
	return args, nil
}

func readFull(rd *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := rd.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func runRedis(t *testing.T, m *mockRedis, args ...string) (int, string, string) {
	t.Helper()
	full := append([]string{"-h", "127.0.0.1", "-p", strings.TrimPrefix(m.addr(), "127.0.0.1:")}, args...)
	return core.Execute("redis-cli", full, strings.NewReader(""))
}

func TestRedisGetAndAuth(t *testing.T) {
	m := newMockRedis(t, "secret")
	code, out, errOut := runRedis(t, m, "-a", "secret", "GET", "key")
	if code != 0 || strings.TrimSpace(out) != "bar" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	// wrong auth
	code, _, errOut = runRedis(t, m, "-a", "wrong", "PING")
	if code != 1 || !strings.Contains(errOut, "invalid password") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	// no auth → NOAUTH error, exit 1
	code, _, errOut = runRedis(t, m, "GET", "key")
	if code != 1 || !strings.Contains(errOut, "NOAUTH") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestRedisReplies(t *testing.T) {
	m := newMockRedis(t, "")
	code, out, errOut := runRedis(t, m, "PING")
	if code != 0 || strings.TrimSpace(out) != "PONG" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	// integer
	code, out, _ = runRedis(t, m, "INCR", "counter")
	if code != 0 || strings.TrimSpace(out) != "1" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	// nil bulk → empty line
	code, out, _ = runRedis(t, m, "GET", "missing")
	if code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	// array → one element per line
	code, out, _ = runRedis(t, m, "LRANGE", "l", "0", "-1")
	if code != 0 || out != "a1\na2\na3\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	// delimiter
	code, out, _ = runRedis(t, m, "-d", "|", "LRANGE", "l", "0", "-1")
	if code != 0 || strings.TrimSpace(out) != "a1|a2|a3" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	// unknown command → error exit 1
	code, _, errOut = runRedis(t, m, "BADCMD")
	if code != 1 || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestRedisSelectAndRepeat(t *testing.T) {
	m := newMockRedis(t, "")
	code, out, errOut := runRedis(t, m, "-n", "3", "-r", "2", "PING")
	if code != 0 || strings.TrimSpace(out) != "PONG\nPONG" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	// -x reads the last argument from stdin
	code, out, errOut = core.Execute("redis-cli", []string{"-h", "127.0.0.1", "-p", strings.TrimPrefix(m.addr(), "127.0.0.1:"), "-x", "SET", "key"}, strings.NewReader("from-stdin"))
	if code != 0 || strings.TrimSpace(out) != "OK" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestRedisConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strings.TrimPrefix(ln.Addr().String(), "127.0.0.1:")
	ln.Close()
	code, _, errOut := core.Execute("redis-cli", []string{"-h", "127.0.0.1", "-p", port, "PING"}, strings.NewReader(""))
	if code != 1 || errOut == "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}
