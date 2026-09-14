package core_test

// sshd/wssh are exercised end to end: a real winux.exe sshd process runs with
// --auth any on an ephemeral port, then (a) a golang.org/x/crypto/ssh client
// runs an exec and an interactive shell (ConPTY) session, and (b) the wssh
// CLI connects and runs a command.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"winux/core"
)

// buildWinuxExe builds a fresh winux.exe from the module root (a stale
// bin\winux.exe may predate the sshd command, so always build).
func buildWinuxExe(t *testing.T) string {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "winux.exe")
	cmd := exec.Command("go", "build", "-o", tmp, "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build winux.exe for the ssh test: %v (%s)", err, out)
	}
	return tmp
}

// startSSHD launches winux sshd in the background and returns its TCP address
// plus a cleanup function.
func startSSHD(t *testing.T) (addr string, stop func()) {
	t.Helper()
	exe := buildWinuxExe(t)
	cmd := exec.Command(exe, "sshd", "--auth", "any", "--port", "0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			cmd.Process.Kill()
			cmd.Wait()
		}
	}
	t.Cleanup(stop)

	re := regexp.MustCompile(`listening on ([^\s,]+)`)
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			lines <- "[stderr] " + sc.Text()
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	var collected []string
	for time.Now().Before(deadline) {
		select {
		case l := <-lines:
			collected = append(collected, l)
			if m := re.FindStringSubmatch(l); m != nil {
				return m[1], stop
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	stop()
	t.Fatalf("sshd did not report a listening port (exe=%s); output: %q", exe, collected)
	return "", stop
}

func sshDial(t *testing.T, addr string) *ssh.Client {
	t.Helper()
	conf := &ssh.ClientConfig{
		User:            "tester",
		Auth:            []ssh.AuthMethod{ssh.Password("whatever")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", addr, conf)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", addr, err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestSSHDExec(t *testing.T) {
	addr, _ := startSSHD(t)
	client := sshDial(t, addr)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	out, err := session.Output("echo hello from sshd")
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello from sshd" {
		t.Fatalf("out=%q", out)
	}
}

func TestSSHDExecPipeline(t *testing.T) {
	addr, _ := startSSHD(t)
	client := sshDial(t, addr)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	out, err := session.Output("echo a; echo b | sort -r")
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	if strings.TrimSpace(string(out)) != "a\nb" {
		t.Fatalf("out=%q", out)
	}
}

func TestSSHDExecExitCode(t *testing.T) {
	addr, _ := startSSHD(t)
	client := sshDial(t, addr)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	err = session.Run("exit 7")
	if err == nil {
		t.Fatal("expected an exit error")
	}
	if ee, ok := err.(*ssh.ExitError); !ok || ee.ExitStatus() != 7 {
		t.Fatalf("exit error = %v (want status 7)", err)
	}
}

func TestSSHShellPTY(t *testing.T) {
	addr, _ := startSSHD(t)
	client := sshDial(t, addr)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	stdin, _ := session.StdinPipe()
	out, err := session.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	// line feeds end the lines here (a script-style client); the server has to
	// translate them into the carriage returns a console expects
	if _, err := stdin.Write([]byte("echo pty session works\nexit\n")); err != nil {
		t.Fatal(err)
	}
	stdin.Close()

	// Read the output concurrently: if the client stops reading, the server's
	// ConPTY output pipe fills up and the remote shell stalls.
	var b strings.Builder
	readDone := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := out.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if err != nil {
				close(readDone)
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("shell session did not finish within 20s (output so far: %q)", b.String())
	}
	<-readDone
	// "\r\n<text>\r\n" is the command's own output; the bare text also shows up
	// in the console's echo of the typed line, so assert on the whole line
	if !strings.Contains(b.String(), "\r\npty session works\r\n") {
		t.Fatalf("output=%q", b.String())
	}
}

// sshPTY is an interactive pty session whose output is drained continuously
// (a client that stops reading would stall the remote ConPTY).
type sshPTY struct {
	session *ssh.Session
	stdin   io.WriteCloser
	done    chan error // session.Wait() may only be called once
	mu      sync.Mutex
	out     strings.Builder
}

func openSSHPTY(t *testing.T, client *ssh.Client) *sshPTY {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	if err := session.RequestPty("xterm", 30, 100, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		t.Fatal(err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := session.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	p := &sshPTY{session: session, stdin: stdin, done: make(chan error, 1)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := out.Read(buf)
			if n > 0 {
				p.mu.Lock()
				p.out.Write(buf[:n])
				p.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { p.done <- session.Wait() }()
	return p
}

// waitExit reports whether the session ended within timeout.
func (p *sshPTY) waitExit(timeout time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (p *sshPTY) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

func (p *sshPTY) waitFor(t *testing.T, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(p.text(), want) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// Typing must work the way a human does it: one keystroke at a time, with
// Enter sending a carriage return. The remote console has to echo the
// characters (otherwise the session looks frozen even though it is fine) and
// the line has to run when Enter arrives.
func TestSSHShellPTYInteractive(t *testing.T) {
	addr, _ := startSSHD(t)
	client := sshDial(t, addr)
	p := openSSHPTY(t, client)
	if !p.waitFor(t, "sh>", 15*time.Second) {
		t.Fatalf("no shell prompt; output=%q", p.text())
	}

	const marker = "winux-pty-interactive"
	for _, r := range "echo " + marker {
		if _, err := p.stdin.Write([]byte{byte(r)}); err != nil {
			t.Fatalf("stdin write: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !p.waitFor(t, "echo "+marker, 15*time.Second) {
		t.Fatalf("keystrokes were not echoed by the remote console; output=%q", p.text())
	}
	if _, err := p.stdin.Write([]byte("\r")); err != nil {
		t.Fatalf("stdin CR: %v", err)
	}
	if !p.waitFor(t, "\r\n"+marker+"\r\n", 15*time.Second) {
		t.Fatalf("Enter did not run the typed line; output=%q", p.text())
	}

	// and the session still ends on request
	if _, err := p.stdin.Write([]byte("exit\r")); err != nil {
		t.Fatalf("stdin exit: %v", err)
	}
	if !p.waitExit(20 * time.Second) {
		t.Fatalf("shell did not exit after 'exit'; output=%q", p.text())
	}
}

func TestWsshCLI(t *testing.T) {
	addr, _ := startSSHD(t)
	code, out, errOut := core.Execute("wssh",
		[]string{"-p", "secret", "tester@" + addr, "echo wssh cli works"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "wssh cli works") {
		t.Fatalf("out=%q", out)
	}
}

func TestWsshBadPasswordAuthAny(t *testing.T) {
	addr, _ := startSSHD(t)
	code, out, errOut := core.Execute("wssh",
		[]string{"-p", "wrong", "tester@" + addr, "echo still works"},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "still works") {
		t.Fatalf("out=%q", out)
	}
}

func TestWsshConnectionRefused(t *testing.T) {
	code, _, errOut := core.Execute("wssh",
		[]string{"-p", "x", "tester@127.0.0.1:1", "--timeout", "3", "echo hi"},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errOut, "wssh:") {
		t.Fatalf("stderr=%q", errOut)
	}
}

// A server that accepts the TCP connection but never speaks SSH must not hang
// wssh forever: the handshake deadline (--timeout) has to kick in.
func TestWsshHandshakeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				time.Sleep(30 * time.Second) // hold the connection open, stay silent
				conn.Close()
			}(c)
		}
	}()

	start := time.Now()
	code, _, errOut := core.Execute("wssh",
		[]string{"-p", "x", "tester@" + ln.Addr().String(), "--timeout", "2", "echo hi"},
		strings.NewReader(""))
	elapsed := time.Since(start)
	if code != 1 {
		t.Fatalf("code=%d want 1 stderr=%q", code, errOut)
	}
	if !strings.Contains(errOut, "wssh:") {
		t.Fatalf("stderr=%q", errOut)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("wssh hung for %s before timing out (stderr=%q)", elapsed, errOut)
	}
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("wssh failed too fast (%s); the handshake deadline did not apply", elapsed)
	}
}

func TestWsshHelp(t *testing.T) {
	code, out, errOut := core.Execute("wssh", []string{"--help"}, strings.NewReader(""))
	if code != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

var _ = fmt.Sprintf
