//go:build windows

package core

// sshd is a pure-Go SSH server (golang.org/x/crypto/ssh): Windows does not
// need OpenSSH installed. It listens on TCP port 1022 by default (kept away
// from 22), authenticates Windows local users by password (LogonUserW), and
// serves every command winux implements: an exec request runs `winux sh -c
// <cmd>` (so pipes/redirects/&& all work), and a shell request attaches a
// pseudo console (ConPTY) running the winux sh REPL.
//
//	winux sshd                     # foreground server on :1022
//	winux sshd --install           # register a winux-sshd Windows service
//	winux sshd --uninstall
//
// The --auth any mode accepts any password (dev/test only; the default is
// `--auth local` which validates against the local Windows user database).
// The host key is generated on first run and kept in
// %ProgramData%\winux\sshd_host_ed25519.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

var procLogonUser = advapi32.NewProc("LogonUserW")

const (
	logon32LogonNetwork    = 3
	logon32ProviderDefault = 0
)

func init() {
	addCmd("sshd", "pure-Go SSH server on :1022 (no OpenSSH needed; serves the winux commands)", cmdSshd)
}

const sshServiceName = "winux-sshd"

func sshDataDir() string {
	// Prefer %ProgramData%\winux (works when running as a service); fall back
	// to %LOCALAPPDATA%\winux when the machine-level dir is not writable.
	if pd := os.Getenv("ProgramData"); pd != "" {
		p := filepath.Join(pd, "winux")
		if err := os.MkdirAll(p, 0755); err == nil {
			probe := filepath.Join(p, ".write-probe")
			if err := os.WriteFile(probe, nil, 0644); err == nil {
				os.Remove(probe)
				return p
			}
		}
	}
	d := filepath.Join(os.Getenv("LOCALAPPDATA"), "winux")
	os.MkdirAll(d, 0755)
	return d
}

func sshHostKeyPath() string { return filepath.Join(sshDataDir(), "sshd_host_ed25519") }

func sshLoadOrCreateHostKey() (ssh.Signer, error) {
	path := sshHostKeyPath()
	if data, err := os.ReadFile(path); err == nil {
		if k, err := ssh.ParsePrivateKey(data); err == nil {
			return k, nil
		}
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	data := pem.EncodeToMemory(block)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

// sshAuthLocal validates a Windows local user password with LogonUserW.
func sshAuthLocal(user, pass string) bool {
	if user == "" || strings.ContainsAny(user, `"/\[]:;|=,+*?<>`) {
		return false
	}
	u, err := syscall.UTF16PtrFromString(user)
	if err != nil {
		return false
	}
	p, err := syscall.UTF16PtrFromString(pass)
	if err != nil {
		return false
	}
	d := syscall.StringToUTF16Ptr(".")
	var token uintptr
	if r, _, _ := procLogonUser.Call(
		uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(p)),
		logon32LogonNetwork, logon32ProviderDefault, uintptr(unsafe.Pointer(&token))); r == 0 {
		return false
	}
	procCloseHandle.Call(token)
	return true
}

type sshdConfig struct {
	host    string
	port    int
	authAny bool
	logFile string
	service bool
}

func cmdSshd(args []string) int {
	cfg := &sshdConfig{host: "0.0.0.0", port: 1022}
	install, uninstall := false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--port" || a == "-p":
			if i+1 < len(args) {
				i++
				if n, e := strconv.Atoi(args[i]); e == nil && n >= 0 && n < 65536 {
					cfg.port = n
				} else {
					fmt.Fprintf(os.Stderr, "sshd: invalid port %q\n", args[i])
					return 1
				}
			}
		case strings.HasPrefix(a, "--port="):
			n, e := strconv.Atoi(strings.TrimPrefix(a, "--port="))
			if e != nil || n < 0 || n > 65535 {
				fmt.Fprintf(os.Stderr, "sshd: invalid port %q\n", a)
				return 1
			}
			cfg.port = n
		case a == "--listen":
			if i+1 < len(args) {
				i++
				cfg.host = args[i]
			}
		case a == "--auth":
			if i+1 < len(args) {
				i++
				switch args[i] {
				case "local":
					cfg.authAny = false
				case "any":
					cfg.authAny = true
				default:
					fmt.Fprintf(os.Stderr, "sshd: --auth must be local or any\n")
					return 1
				}
			}
		case a == "--log":
			if i+1 < len(args) {
				i++
				cfg.logFile = args[i]
			}
		case a == "--service":
			cfg.service = true
		case a == "--install":
			install = true
		case a == "--uninstall":
			uninstall = true
		case a == "--help" || a == "-h":
			fmt.Println("Usage: winux sshd [--port N] [--listen HOST] [--auth local|any] [--log FILE] [--install|--uninstall]")
			fmt.Println("  pure-Go SSH server on :1022 by default (no OpenSSH needed)")
			fmt.Println("  --install   register the winux-sshd Windows service and start it")
			fmt.Println("  --uninstall remove the winux-sshd service")
			return 0
		case a == "--version" || a == "-v":
			fmt.Printf("winux sshd %s (pure-Go SSH server)\n", Version)
			fmt.Printf("from: %s\n", exePath())
			return 0
		default:
			fmt.Fprintf(os.Stderr, "sshd: unknown option %s\n", a)
			return 2
		}
	}
	if install {
		return sshInstallService(cfg)
	}
	if uninstall {
		return sshUninstallService()
	}
	if cfg.service {
		return sshRunService(cfg)
	}
	return sshRunForeground(cfg)
}

// sshInstallService registers and starts the winux-sshd service.
func sshInstallService(cfg *sshdConfig) int {
	if !isAdmin() {
		fmt.Fprintln(os.Stderr, "sshd --install needs administrator rights")
		return 1
	}
	// Register the service against this unit's own host copy rather than the
	// winux installation: the copy owns its data, so the sshd service keeps
	// starting after winux is moved, upgraded or uninstalled. `sshd --service`
	// speaks the SCM protocol itself, so no wrapper process is needed here.
	host, err := sysMaterializeHost(sshServiceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sshd: %v\n", err)
		return 1
	}
	bin := fmt.Sprintf(`"%s" sshd --service --port %d`, host, cfg.port)
	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sshd: %v\n", err)
		return 1
	}
	defer m.Disconnect()
	if exists, _ := sshServiceExists(m, sshServiceName); exists {
		fmt.Fprintf(os.Stderr, "sshd: service %s already exists; uninstall it first or use winux systemctl restart %s\n", sshServiceName, sshServiceName)
		return 1
	}
	s, err := m.CreateService(sshServiceName, bin, mgr.Config{
		DisplayName: "winux sshd (pure-Go SSH server)",
		Description: "Pure-Go SSH server served by winux; port " + strconv.Itoa(cfg.port) + ". Windows OpenSSH is not required.",
		StartType:   mgr.StartAutomatic,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sshd: create service: %v\n", err)
		return 1
	}
	s.Close()
	if err := s.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sshd: service created but start failed: %v (run 'winux systemctl start %s' later)\n", err, sshServiceName)
		return 1
	}
	fmt.Printf("sshd: service %s installed and started (port %d)\n", sshServiceName, cfg.port)
	fmt.Printf("  manage it with: winux systemctl status %s / stop / restart / journalctl -u %s\n", sshServiceName, sshServiceName)
	return 0
}

func sshServiceExists(m *mgr.Mgr, name string) (bool, error) {
	s, err := m.OpenService(name)
	if err != nil {
		if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
			return false, nil
		}
		return false, err
	}
	s.Close()
	return true, nil
}

func sshUninstallService() int {
	if !isAdmin() {
		fmt.Fprintln(os.Stderr, "sshd --uninstall needs administrator rights")
		return 1
	}
	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sshd: %v\n", err)
		return 1
	}
	defer m.Disconnect()
	s, err := m.OpenService(sshServiceName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sshd: service not installed")
		return 1
	}
	defer s.Close()
	st, _ := s.Query()
	if st.State != svc.Stopped {
		s.Control(svc.Stop)
	}
	s.Delete()
	// the service is gone, so its self-contained host copy is no longer needed
	sysRemoveHost(sshServiceName)
	fmt.Printf("sshd: service %s removed\n", sshServiceName)
	return 0
}

// sshRunService runs under the SCM as the winux-sshd service.
func sshRunService(cfg *sshdConfig) int {
	if cfg.logFile != "" {
		if f, err := os.OpenFile(cfg.logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
			log.SetOutput(f)
			defer f.Close()
		}
	} else if f, err := os.OpenFile(filepath.Join(sshDataDir(), "sshd.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}
	log.SetFlags(log.LstdFlags)
	if err := svc.Run(sshServiceName, &sshSvcHandler{cfg: cfg}); err != nil {
		log.Printf("sshd: svc.Run failed: %v", err)
		return 1
	}
	return 0
}

type sshSvcHandler struct {
	cfg   *sshdConfig
	ln    net.Listener
	close chan struct{}
}

func (h *sshSvcHandler) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const cmds = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}
	srv, err := sshNewServer(h.cfg)
	if err != nil {
		log.Printf("sshd: %v", err)
		changes <- svc.Status{State: svc.StopPending}
		return false, 1
	}
	h.ln = srv.ln
	h.close = make(chan struct{})
	go func() {
		srv.serve()
		close(h.close)
	}()
	changes <- svc.Status{State: svc.Running, Accepts: cmds}
	log.Printf("sshd: listening on %s", srv.ln.Addr())
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				log.Printf("sshd: stopping on %v", c.Cmd)
				changes <- svc.Status{State: svc.StopPending}
				h.ln.Close()
				return false, 0
			case svc.Interrogate:
				changes <- c.CurrentStatus
			}
		case <-h.close:
			log.Printf("sshd: listener exited unexpectedly")
			return false, 1
		}
	}
}

// sshRunForeground runs the server in the console (foreground mode).
func sshRunForeground(cfg *sshdConfig) int {
	if cfg.logFile != "" {
		if f, err := os.OpenFile(cfg.logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
			log.SetOutput(f)
			defer f.Close()
		}
	}
	log.SetFlags(log.LstdFlags)
	srv, err := sshNewServer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sshd: %v\n", err)
		return 1
	}
	auth := "local Windows users"
	if cfg.authAny {
		auth = "ANY password (test mode)"
	}
	fmt.Printf("winux sshd %s — listening on %s, authentication: %s\n", Version, srv.ln.Addr(), auth)
	log.Printf("sshd: listening on %s, auth=%s", srv.ln.Addr(), auth)
	if err := srv.serve(); err != nil {
		log.Printf("sshd: serve: %v", err)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// Server core

type sshServer struct {
	ln   net.Listener
	cfg  *sshdConfig
	conf *ssh.ServerConfig
}

func sshNewServer(cfg *sshdConfig) (*sshServer, error) {
	hostKey, err := sshLoadOrCreateHostKey()
	if err != nil {
		return nil, fmt.Errorf("host key: %v", err)
	}
	conf := &ssh.ServerConfig{}
	conf.AddHostKey(hostKey)
	conf.PasswordCallback = func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if cfg.authAny {
			return nil, nil
		}
		if sshAuthLocal(meta.User(), string(password)) {
			return nil, nil
		}
		return nil, fmt.Errorf("authentication failed for user %q", meta.User())
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port)))
	if err != nil {
		return nil, err
	}
	return &sshServer{ln: ln, cfg: cfg, conf: conf}, nil
}

func (s *sshServer) serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			sconn, chans, reqs, err := ssh.NewServerConn(conn, s.conf)
			if err != nil {
				conn.Close()
				return
			}
			log.Printf("sshd: new connection from %s as %q", sconn.RemoteAddr(), sconn.User())
			go ssh.DiscardRequests(reqs)
			for ch := range chans {
				if ch.ChannelType() != "session" {
					ch.Reject(ssh.UnknownChannelType, "only session channels are supported")
					continue
				}
				go sshHandleSession(sconn, ch)
			}
			sconn.Close()
			log.Printf("sshd: connection from %s closed", sconn.RemoteAddr())
		}()
	}
}

func sshHandleSession(sconn *ssh.ServerConn, newChan ssh.NewChannel) {
	ch, requests, err := newChan.Accept()
	if err != nil {
		return
	}
	defer ch.Close()

	st := &sshSession{cur: nil, mu: &sync.Mutex{}}
	var ptyCols, ptyRows uint16

	for req := range requests {
		switch req.Type {
		case "pty-req":
			// RFC 4254 §6.2: string term, uint32 width, uint32 height,
			// uint32 width_pixels, uint32 height_pixels, string modes.
			if w, h, ok := sshPTYSize(req.Payload); ok {
				ptyCols, ptyRows = w, h
			}
			req.Reply(true, nil)
		case "window-change":
			// RFC 4254 §6.7: uint32 width, uint32 height, uint32 width_px,
			// uint32 height_px.
			if len(req.Payload) >= 8 {
				ptyCols = uint16(uint32(req.Payload[0])<<24 | uint32(req.Payload[1])<<16 |
					uint32(req.Payload[2])<<8 | uint32(req.Payload[3]))
				ptyRows = uint16(uint32(req.Payload[4])<<24 | uint32(req.Payload[5])<<16 |
					uint32(req.Payload[6])<<8 | uint32(req.Payload[7]))
			}
			st.setPTY(func(pt *sshConPTY) { pt.resize(ptyCols, ptyRows) })
			req.Reply(true, nil)
		case "exec":
			if len(req.Payload) > 4 {
				cmd := strings.TrimRight(string(req.Payload[4:]), "\x00")
				req.Reply(true, nil)
				sshRunExec(ch, sconn, cmd)
			} else {
				req.Reply(false, nil)
			}
			return
		case "shell":
			req.Reply(true, nil)
			sshRunShell(ch, sconn, st, ptyCols, ptyRows)
			return
		case "env":
			req.Reply(false, nil)
		default:
			req.Reply(false, nil)
		}
	}
}

// sshSession carries the live pseudo console of a shell session so the
// window-change request can resize it.
type sshSession struct {
	cur *sshConPTY
	mu  *sync.Mutex
}

func (s *sshSession) setPTY(fn func(*sshConPTY)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		fn(s.cur)
	}
}

// sshPTYSize extracts width/height from a pty-req payload (RFC 4254 §6.2):
// string term (NUL-terminated), then two big-endian uint32.
func sshPTYSize(p []byte) (uint16, uint16, bool) {
	i := 0
	for i < len(p) && p[i] != 0 {
		i++
	}
	if i+9 > len(p) {
		return 0, 0, false
	}
	w := uint32(p[i+1])<<24 | uint32(p[i+2])<<16 | uint32(p[i+3])<<8 | uint32(p[i+4])
	h := uint32(p[i+5])<<24 | uint32(p[i+6])<<16 | uint32(p[i+7])<<8 | uint32(p[i+8])
	return uint16(w), uint16(h), true
}

// sshRunExec runs a remote command as `winux sh -c <cmd>` in a child process
// so pipes, redirects and conditionals all work over ssh.
func sshRunExec(ch ssh.Channel, sconn *ssh.ServerConn, cmd string) {
	exe := sshChildExe()
	proc := exec.Command(exe, "sh", "-c", cmd)
	proc.Stdin = ch
	proc.Stdout = ch
	proc.Stderr = ch
	err := proc.Run()
	status := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			status = ee.ExitCode()
		} else {
			status = 1
		}
	}
	ch.SendRequest("exit-status", false, []byte{0, 0, 0, byte(status)})
	log.Printf("sshd: %s@%s exec %q -> exit %d", sconn.User(), sconn.RemoteAddr(), cmd, status)
}

// sshRunShell attaches a ConPTY running winux sh to the session channel.
func sshRunShell(ch ssh.Channel, sconn *ssh.ServerConn, st *sshSession, cols, rows uint16) {
	pt, err := newSSHConPTY(cols, rows)
	if err != nil {
		log.Printf("sshd: conpty: %v", err)
		ch.SendRequest("exit-status", false, []byte{0, 0, 0, 1})
		return
	}
	st.mu.Lock()
	st.cur = pt
	st.mu.Unlock()
	go pt.pipe(ch)
	go pt.writeStdin(ch)
	<-pt.done
	st.mu.Lock()
	st.cur = nil
	st.mu.Unlock()
	ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
	log.Printf("sshd: %s@%s shell session ended", sconn.User(), sconn.RemoteAddr())
}

// sshChildExe returns the winux executable used for remote exec/shell child
// processes; WINUX_EXE overrides it (used by tests).
func sshChildExe() string {
	if e := os.Getenv("WINUX_EXE"); e != "" {
		return e
	}
	exe, err := os.Executable()
	if err != nil {
		return "winux.exe"
	}
	return exe
}
