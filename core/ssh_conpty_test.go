//go:build windows

package core

// Direct ConPTY tests: a pseudo console is wired the way sshd wires it and then
// driven the way a human drives it - keystroke by keystroke, Enter being a
// carriage return. The echo is the real assertion here: a console that never
// sees its input echoes nothing, which makes an interactive remote shell look
// frozen even though the connection itself is fine.

import (
	"bufio"
	"bytes"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestSSHConsoleInputTranslatesLineFeed(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"echo hi\n", "echo hi\r"},         // a script's LF must end the line
		{"echo hi\r\n", "echo hi\r\n"},     // a real terminal's CRLF is left alone
		{"echo hi\rmore", "echo hi\rmore"}, // a lone CR is already a line end
		{"a\nb\n", "a\rb\r"},               // every LF, not just the last one
		{"\x1b[A", "\x1b[A"},               // arrow keys carry no newline at all
	} {
		if got := string(sshConsoleInput([]byte(c.in))); got != c.want {
			t.Errorf("sshConsoleInput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A pseudo console created with the documented handle ends (the console reads
// the read end of the input pipe and writes the write end of the output pipe)
// must echo typed characters and run a line when Enter - a carriage return -
// arrives. Wiring the pipes the other way round produced output but swallowed
// every keystroke.
func TestSSHConPTYRoundtrip(t *testing.T) {
	if !conptyAvailable() {
		t.Skip("pseudo console not available (need Windows 10 1809+)")
	}
	// The ConPTY child must be the real winux.exe, not the test binary.
	exe := filepath.Join(t.TempDir(), "winux.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "..").CombinedOutput(); err != nil {
		t.Skipf("cannot build winux.exe: %v (%s)", err, out)
	}
	t.Setenv("WINUX_EXE", exe)

	// Mimic the sshd structure: everything happens in goroutines.
	type res struct {
		pt  *sshConPTY
		err error
	}
	rc := make(chan res, 1)
	go func() {
		pt, err := newSSHConPTY(100, 30)
		rc <- res{pt, err}
	}()
	r := <-rc
	if r.err != nil {
		t.Fatalf("newSSHConPTY: %v", r.err)
	}
	pt := r.pt
	var out bytes.Buffer
	go pt.pipe(&out)

	text := func() string { return out.String() }
	waitFor := func(want string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if strings.Contains(text(), want) {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		return false
	}

	// 1) typing without pressing Enter must be echoed by the console
	for _, r := range "echo conpty-roundtrip-ok" {
		if err := pt.writeInput(sshConsoleInput([]byte{byte(r)})); err != nil {
			t.Fatalf("writeInput: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !waitFor("echo conpty-roundtrip-ok", 10*time.Second) {
		pt.kill()
		t.Fatalf("the pseudo console did not echo the typed characters; output=%q", text())
	}
	// 2) Enter (CR) runs the line, so the command's own output appears too
	if err := pt.writeInput([]byte("\r")); err != nil {
		t.Fatalf("writeInput CR: %v", err)
	}
	if !waitFor("\r\nconpty-roundtrip-ok\r\n", 15*time.Second) {
		pt.kill()
		t.Fatalf("the line did not run after Enter; output=%q", text())
	}
	// 3) a line-feed terminated script works too, then the shell exits
	pt.writeInput(sshConsoleInput([]byte("echo lf-line-ok\nexit\n")))

	select {
	case <-pt.done:
	case <-time.After(20 * time.Second):
		pt.kill()
		t.Fatalf("conpty child did not exit; output so far: %q", text())
	}
	if !waitFor("lf-line-ok", 3*time.Second) {
		t.Fatalf("the LF-terminated line did not run; output=%q", text())
	}
}

// The whole user-facing path in one test: winux sshd in the background, and
// `winux.exe wssh` running inside a REAL console (a pseudo console of our own,
// so stdin is a console exactly as it is for a user typing in cmd.exe). The
// keystrokes typed here travel client -> sshd -> remote console and are echoed
// back. This is the case that used to freeze at "sh>" with nothing happening:
// the client sat in raw mode while the remote console never saw its input.
func TestWsshInteractiveInRealConsole(t *testing.T) {
	if !conptyAvailable() {
		t.Skip("pseudo console not available (need Windows 10 1809+)")
	}
	exe := filepath.Join(t.TempDir(), "winux.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "..").CombinedOutput(); err != nil {
		t.Skipf("cannot build winux.exe: %v (%s)", err, out)
	}

	// winux sshd --auth any on an ephemeral port
	sshd := exec.Command(exe, "sshd", "--auth", "any", "--port", "0")
	stdout, err := sshd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	sshd.Stderr = sshd.Stdout
	if err := sshd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sshd.Process.Kill()
		sshd.Wait()
	})
	portCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		re := regexp.MustCompile(`listening on ([^\s,]+)`)
		for sc.Scan() {
			m := re.FindStringSubmatch(sc.Text())
			if m == nil {
				continue
			}
			if _, p, err := net.SplitHostPort(m[1]); err == nil {
				select {
				case portCh <- p:
				default:
				}
			}
		}
	}()
	var port string
	select {
	case port = <-portCh:
	case <-time.After(20 * time.Second):
		t.Fatal("sshd did not report a listening port")
	}

	// the client itself, attached to a console we own
	pt, err := newConPTYCmd(`"`+exe+`" wssh -p x tester@127.0.0.1:`+port, 100, 30)
	if err != nil {
		t.Fatalf("newConPTYCmd: %v", err)
	}
	var out bytes.Buffer
	go pt.pipe(&out)
	text := func() string { return out.String() }
	waitFor := func(want string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if strings.Contains(text(), want) {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		return false
	}

	if !waitFor("sh>", 25*time.Second) {
		pt.kill()
		t.Fatalf("no remote prompt through wssh; output=%q", text())
	}
	// type a command one keystroke at a time, then Enter (a carriage return)
	for _, r := range "echo wssh-interactive-ok" {
		if err := pt.writeInput(sshConsoleInput([]byte{byte(r)})); err != nil {
			pt.kill()
			t.Fatalf("writeInput: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !waitFor("echo wssh-interactive-ok", 20*time.Second) {
		pt.kill()
		t.Fatalf("the remote console never echoed the keystrokes; output=%q", text())
	}
	if err := pt.writeInput([]byte("\r")); err != nil {
		pt.kill()
		t.Fatalf("writeInput CR: %v", err)
	}
	if !waitFor("\r\nwssh-interactive-ok\r\n", 25*time.Second) {
		pt.kill()
		t.Fatalf("Enter did not run the line through wssh; output=%q", text())
	}
	// quitting the remote shell must end the client too
	pt.writeInput(sshConsoleInput([]byte("exit\r")))
	select {
	case <-pt.done:
	case <-time.After(25 * time.Second):
		pt.kill()
		t.Fatalf("wssh did not exit after the remote shell ended; output=%q", text())
	}
}
