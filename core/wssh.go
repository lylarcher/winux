//go:build windows

package core

// wssh is a pure-Go SSH client (golang.org/x/crypto/ssh): it connects to a
// winux sshd (or any SSH server) without needing OpenSSH installed.
//
//	winux wssh user@host                 # interactive shell (pty)
//	winux wssh user@host "ls -l | wc -l" # run one command
//	winux wssh -p secret host:1022
//
// The default port is 1022 (the winux sshd port); use -P 22 or host:22 to
// reach an OpenSSH server. Password comes from -p/--password or an
// interactive prompt. Host keys are not verified (winux ecosystem default).

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func init() {
	addCmd("wssh", "pure-Go SSH client (no OpenSSH needed): winux wssh [user@]host[:port] [command]", cmdWssh)
}

func cmdWssh(args []string) int {
	password := ""
	passwordSet := false
	port := 1022
	user := ""
	timeout := 15 * time.Second
	verbose := false

	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--password":
			if i+1 < len(args) {
				i++
				password = args[i]
				passwordSet = true
			}
		case strings.HasPrefix(a, "--password="):
			password = strings.TrimPrefix(a, "--password=")
			passwordSet = true
		case strings.HasPrefix(a, "--password "):
			password = strings.TrimPrefix(a, "--password ")
			passwordSet = true
		case a == "-P" || a == "--port":
			if i+1 < len(args) {
				i++
				if n, e := strconv.Atoi(args[i]); e == nil && n > 0 && n < 65536 {
					port = n
				} else {
					fmt.Fprintf(os.Stderr, "wssh: invalid port %q\n", args[i])
					return 1
				}
			}
		case strings.HasPrefix(a, "--port="):
			if n, e := strconv.Atoi(strings.TrimPrefix(a, "--port=")); e == nil && n > 0 && n < 65536 {
				port = n
			}
		case a == "-l" || a == "--login":
			if i+1 < len(args) {
				i++
				user = args[i]
			}
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "--timeout":
			if i+1 < len(args) {
				i++
				if n, e := strconv.Atoi(args[i]); e == nil && n > 0 {
					timeout = time.Duration(n) * time.Second
				}
			}
		case a == "--help" || a == "-h":
			fmt.Println("Usage: winux wssh [OPTIONS] [user@]host[:port] [command]")
			fmt.Println("  pure-Go SSH client (no OpenSSH needed); default port 1022")
			fmt.Println("  options: -p/--password PWD  -P/--port N  -l/--login USER  -v  --timeout SECS")
			return 0
		case a == "--version" || a == "-V":
			fmt.Printf("winux wssh %s (pure-Go SSH client)\n", Version)
			fmt.Printf("from: %s\n", exePath())
			return 0
		case strings.HasPrefix(a, "-") && len(a) > 1:
			fmt.Fprintf(os.Stderr, "wssh: unknown option %s\n", a)
			return 2
		default:
			positionals = append(positionals, a)
		}
	}
	if len(positionals) == 0 {
		fmt.Fprintln(os.Stderr, "wssh: missing host. usage: winux wssh [user@]host[:port] [command]")
		return 2
	}
	target := positionals[0]
	command := ""
	if len(positionals) > 1 {
		command = strings.Join(positionals[1:], " ")
	}

	// parse [user@]host[:port]
	if at := strings.LastIndex(target, "@"); at >= 0 {
		if user == "" {
			user = target[:at]
		}
		target = target[at+1:]
	}
	if colon := strings.LastIndex(target, ":"); colon >= 0 && !strings.Contains(target[colon:], "]") {
		if n, e := strconv.Atoi(target[colon+1:]); e == nil && n > 0 && n < 65536 {
			port = n
			target = target[:colon]
		}
	}
	if user == "" {
		user = os.Getenv("USERNAME")
		if user == "" {
			user = "postgres" // never reached in practice on Windows
		}
	}

	addr := netJoinHostPort(target, port)
	if verbose {
		fmt.Fprintf(os.Stderr, "wssh: connecting to %s as %s\n", addr, user)
	}
	conn, err := dialSSH(addr, user, timeout, verbose, func() (string, error) {
		if passwordSet {
			return password, nil
		}
		if !isTerminal(os.Stdin) {
			return "", fmt.Errorf("no password given (use -p/--password or run interactively)")
		}
		line, _ := readPassword(fmt.Sprintf("%s@%s's password: ", user, target))
		fmt.Fprintln(os.Stderr)
		return line, nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "wssh: %v\n", err)
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "refused"):
			fmt.Fprintln(os.Stderr, "wssh: the target refused the connection — is sshd running on that port, and is the port reachable (firewall/security group)?")
		case strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
			fmt.Fprintln(os.Stderr, "wssh: connection timed out — check the host/port; a firewall may be silently dropping packets")
		}
		return 1
	}
	defer conn.Close()

	session, err := conn.NewSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wssh: %v\n", err)
		return 1
	}
	defer session.Close()

	if command != "" {
		return wsshRunCommand(session, command)
	}
	return wsshInteractive(session)
}

func netJoinHostPort(host string, port int) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host + ":" + strconv.Itoa(port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + strconv.Itoa(port)
	}
	return host + ":" + strconv.Itoa(port)
}

func dialSSH(addr, user string, timeout time.Duration, verbose bool, askPassword func() (string, error)) (*ssh.Client, error) {
	// Dial the TCP socket with the timeout, then keep that same deadline on the
	// connection for the whole handshake (version exchange, algorithm
	// negotiation AND authentication). x/crypto's ClientConfig.Timeout only
	// covers the TCP dial, so without this a server that stalls mid-handshake
	// (dropped packets, slow DNS/PAM) would hang wssh forever.
	netConn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	if timeout > 0 {
		if err := netConn.SetDeadline(time.Now().Add(timeout)); err != nil {
			netConn.Close()
			return nil, err
		}
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "wssh: tcp connected to %s, starting ssh handshake (deadline %s)\n", addr, timeout)
	}
	// Ask for the password only after the TCP connection is up, so an
	// unreachable server fails immediately instead of first prompting.
	password := ""
	if askPassword != nil {
		p, err := askPassword()
		if err != nil {
			netConn.Close()
			return nil, err
		}
		password = p
	}
	var gotKey ssh.PublicKey
	conf := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
			ssh.KeyboardInteractive(sshKbdInteractive(password)),
		},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			gotKey = key
			return nil
		},
	}
	c, chans, reqs, err := ssh.NewClientConn(netConn, addr, conf)
	if err != nil {
		netConn.Close()
		return nil, err
	}
	if timeout > 0 {
		netConn.SetDeadline(time.Time{}) // clear the handshake deadline
	}
	if verbose {
		if gotKey != nil {
			fmt.Fprintf(os.Stderr, "wssh: server host key: %s (%s)\n", ssh.FingerprintSHA256(gotKey), gotKey.Type())
		}
		fmt.Fprintf(os.Stderr, "wssh: authenticated as %s\n", user)
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// sshKbdInteractive answers keyboard-interactive auth challenges (used by
// OpenSSH servers that disable the plain "password" method, e.g.
// KbdInteractiveAuthentication only). The first question is normally the
// password we already collected; echo=true questions fall back to reading a
// line from the console.
func sshKbdInteractive(password string) ssh.KeyboardInteractiveChallenge {
	return func(user, instruction string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range questions {
			if len(echos) > i && echos[i] {
				fmt.Fprint(os.Stderr, questions[i]+": ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				answers[i] = strings.TrimRight(line, "\r\n")
			} else {
				answers[i] = password
			}
		}
		return answers, nil
	}
}

func wsshRunCommand(session *ssh.Session, command string) int {
	out, err := session.CombinedOutput(command)
	os.Stdout.Write(out)
	if err != nil {
		if ee, ok := err.(*ssh.ExitError); ok {
			return ee.ExitStatus()
		}
		return 1
	}
	return 0
}

func wsshInteractive(session *ssh.Session) int {
	// Request a pty sized to the local console, enter raw mode, then bridge
	// bytes both ways. terminalSize reports rows first.
	rows, cols := terminalSize()
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm", rows, cols, modes); err != nil {
		fmt.Fprintf(os.Stderr, "wssh: pty request: %v\n", err)
		return 1
	}

	stdinPipe, _ := session.StdinPipe()
	stdout, err := session.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wssh: %v\n", err)
		return 1
	}

	if err := session.Shell(); err != nil {
		fmt.Fprintf(os.Stderr, "wssh: %v\n", err)
		return 1
	}

	restore := enterRawMode(os.Stdin)
	enableVTOutput(os.Stdout)
	defer restore()

	// size watcher: push window-change when the console resizes
	stopWatch := make(chan struct{})
	go func() {
		lastCols, lastRows := cols, rows
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-t.C:
				r, c := terminalSize()
				if c > 0 && r > 0 && (c != lastCols || r != lastRows) {
					session.WindowChange(r, c)
					lastCols, lastRows = c, r
				}
			}
		}
	}()
	defer close(stopWatch)

	// stdin -> remote. The reader simply ends at EOF (a piped client, e.g.
	// `echo ls | wssh host`); nothing waits on it, and the session ends when
	// the remote shell does.
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if _, werr := stdinPipe.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	copyDone := make(chan struct{})
	go func() {
		io.Copy(os.Stdout, stdout)
		close(copyDone)
	}()

	session.Wait()
	// let the output pump flush whatever the remote shell sent on its way out
	select {
	case <-copyDone:
	case <-time.After(time.Second):
	}
	return 0
}

var _ = bufio.NewReader
