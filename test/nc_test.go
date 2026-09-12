package core_test

// nc is exercised end to end against in-test TCP/UDP peers: connect/relay,
// listen, zero-I/O scan, idle timeout, UDP datagrams, and the refusal paths.

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"winux/core"
)

func runNc(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	return core.Execute("nc", args, strings.NewReader(stdin))
}

// freePort hands back a port that was free a moment ago (small race, fine for
// loopback tests).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestNcHelpAndAlias(t *testing.T) {
	for _, name := range []string{"nc", "netcat"} {
		code, out, errOut := core.Execute(name, []string{"--help"}, strings.NewReader(""))
		if code != 0 || !strings.Contains(out, "Usage:") {
			t.Fatalf("%s --help: code=%d out=%q stderr=%q", name, code, out, errOut)
		}
		if !strings.Contains(out, "-l") || !strings.Contains(out, "-z") || !strings.Contains(out, "-w") {
			t.Fatalf("%s --help missing key options: %q", name, out)
		}
	}
}

func TestNcConnectEcho(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 64)
		n, err := c.Read(buf) // the payload nc sent
		if err != nil && n == 0 {
			return
		}
		c.Write(buf[:n])       // echo it back
		io.Copy(io.Discard, c) // wait for nc's FIN, then close
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	code, out, errOut := runNc(t, "hello\n", "127.0.0.1", strconv.Itoa(port))
	<-serverDone
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if out != "hello\n" {
		t.Fatalf("out=%q want %q", out, "hello\n")
	}
}

func TestNcZeroScan(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
		ln.Close()
	}()

	code, _, errOut := runNc(t, "", "-z", "-w", "2", "127.0.0.1", strconv.Itoa(port))
	if code != 0 {
		t.Fatalf("open port: code=%d stderr=%q", code, errOut)
	}

	// A port that is actually closed: keep probing until one refuses.
	closed := -1
	for i := 0; i < 10 && closed < 0; i++ {
		p := freePort(t)
		if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), 200*time.Millisecond); err != nil {
			closed = p
		}
	}
	if closed < 0 {
		t.Skip("could not find a closed loopback port")
	}
	code, _, errOut = runNc(t, "", "-z", "-w", "2", "127.0.0.1", strconv.Itoa(closed))
	if code != 1 {
		t.Fatalf("closed port: code=%d stderr=%q want 1", code, errOut)
	}
}

func TestNcListen(t *testing.T) {
	port := freePort(t)
	type result struct {
		code        int
		out, errOut string
	}
	ch := make(chan result, 1)
	go func() {
		c, o, e := core.Execute("nc", []string{"-l", "-p", strconv.Itoa(port), "127.0.0.1"}, strings.NewReader(""))
		ch <- result{c, o, e}
	}()

	var conn net.Conn
	for i := 0; i < 100; i++ {
		var err error
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("nc -l never started listening")
	}
	conn.Write([]byte("ping"))
	conn.Close()

	r := <-ch
	if r.code != 0 {
		t.Fatalf("code=%d stderr=%q", r.code, r.errOut)
	}
	if !strings.Contains(r.out, "ping") {
		t.Fatalf("out=%q missing 'ping'", r.out)
	}
}

func TestNcTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // accept and then never speak
		c, err := ln.Accept()
		if err != nil {
			return
		}
		time.Sleep(3 * time.Second)
		c.Close()
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	start := time.Now()
	code, _, errOut := runNc(t, "", "-w", "1", "127.0.0.1", strconv.Itoa(port))
	elapsed := time.Since(start)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if elapsed < 900*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Fatalf("idle timeout elapsed=%v, want ~1s", elapsed)
	}
}

func TestNcUdpSend(t *testing.T) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port

	code, _, errOut := runNc(t, "hello\n", "-u", "127.0.0.1", strconv.Itoa(port))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, _, err := pc.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("server never received the datagram: %v", err)
	}
	if string(buf[:n]) != "hello\n" {
		t.Fatalf("datagram=%q want %q", buf[:n], "hello\n")
	}
}

func TestNcUdpListen(t *testing.T) {
	port := freePort(t)
	type result struct {
		code        int
		out, errOut string
	}
	ch := make(chan result, 1)
	go func() {
		c, o, e := core.Execute("nc", []string{"-u", "-l", "-w", "1", "-p", strconv.Itoa(port), "127.0.0.1"}, strings.NewReader(""))
		ch <- result{c, o, e}
	}()

	pc, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	// UDP is connectionless: datagrams sent before nc has bound the socket
	// are dropped, so keep retrying until one lands (nc stays listening for
	// up to 1s after the first datagram).
	for i := 0; i < 20; i++ {
		pc.Write([]byte("ping"))
		time.Sleep(50 * time.Millisecond)
	}

	r := <-ch
	if r.code != 0 {
		t.Fatalf("code=%d stderr=%q", r.code, r.errOut)
	}
	if !strings.Contains(r.out, "ping") {
		t.Fatalf("out=%q missing 'ping'", r.out)
	}
}

func TestNcBadArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"missing port", []string{"127.0.0.1"}, 1},
		{"too many args", []string{"a", "b", "c"}, 1},
		{"refuse -e", []string{"-e", "cmd", "127.0.0.1", "80"}, 1},
		{"refuse -c", []string{"-c", "cmd", "127.0.0.1", "80"}, 1},
		{"-4 -6 conflict", []string{"-4", "-6", "127.0.0.1", "80"}, 1},
		{"-z with -l", []string{"-z", "-l", "-p", "0"}, 1},
		{"-k without -l", []string{"-k", "127.0.0.1", "80"}, 1},
		{"-n needs numeric host", []string{"-n", "localhost", "80"}, 1},
		{"bad -p", []string{"-p", "abc", "127.0.0.1", "80"}, 1},
		{"bad -w", []string{"-w", "abc", "127.0.0.1", "80"}, 1},
		{"bad port", []string{"127.0.0.1", "99999"}, 1},
		{"unknown option", []string{"--nope", "127.0.0.1", "80"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := runNc(t, "", tc.args...)
			if code != tc.code {
				t.Fatalf("code=%d want %d (stderr=%q)", code, tc.code, errOut)
			}
		})
	}
}

func TestNcVerboseConnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
		ln.Close()
	}()
	code, _, errOut := runNc(t, "", "-v", "-z", "127.0.0.1", strconv.Itoa(port))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(errOut, "succeeded") {
		t.Fatalf("verbose stderr=%q missing 'succeeded'", errOut)
	}
}
