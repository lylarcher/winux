package core

// nc (netcat) is a pure-Go TCP/UDP client and listener with the GNU/OpenBSD
// option set people expect from netcat. There is no native nc on Windows, so
// this is a straight implementation: stdin is relayed to the peer, the peer's
// data is relayed to stdout, and -l switches the direction into listen mode.
// Only the common options are implemented; -e/-c (execute a command after
// connecting) are refused because Windows has no sane way to bind a socket to
// a child's stdio here and it is a classic RCE foot-gun.

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"
)

func init() {
	addCmd("nc", "arbitrary TCP/UDP connections and listeners (netcat)", cmdNc, "netcat")
}

type ncOpts struct {
	listen   bool
	udp      bool
	verbose  int
	numeric  bool
	zero     bool
	keep     bool
	ipv4     bool
	ipv6     bool
	srcPort  int
	srcAddr  string
	timeout  int // seconds; 0 = none
	quit     int // seconds to linger after EOF on stdin
	interval int // seconds between sent lines
	host     string
	port     int
}

func cmdNc(args []string) int {
	f := NewFlags(
		&Flag{Name: "listen", Short: 'l', Long: "listen", Help: "listen for an incoming connection"},
		&Flag{Name: "udp", Short: 'u', Long: "udp", Help: "use UDP instead of TCP"},
		&Flag{Name: "source-port", Short: 'p', Long: "source-port", HasArg: true, ArgName: "PORT", Help: "local source port (-l: the port to listen on)"},
		&Flag{Name: "source", Short: 's', Long: "source", HasArg: true, ArgName: "ADDR", Help: "local source address"},
		&Flag{Name: "timeout", Short: 'w', Long: "timeout", HasArg: true, ArgName: "SECS", Help: "connect and idle timeout in seconds (0 = none)"},
		&Flag{Name: "quit", Short: 'q', Long: "quit", HasArg: true, ArgName: "SECS", Help: "linger SECS after EOF on stdin before closing the connection"},
		&Flag{Name: "no-dns", Short: 'n', Long: "no-dns", Help: "treat the host as a numeric address (no DNS lookup)"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "report connection details on stderr (repeat for more)"},
		&Flag{Name: "zero", Short: 'z', Long: "zero", Help: "scan mode: connect and close immediately, no data"},
		&Flag{Name: "keep-open", Short: 'k', Long: "keep-open", Help: "listen for multiple connections (with -l)"},
		&Flag{Name: "ipv4", Short: '4', Long: "ipv4", Help: "use IPv4 only"},
		&Flag{Name: "ipv6", Short: '6', Long: "ipv6", Help: "use IPv6 only"},
		&Flag{Name: "interval", Short: 'i', Long: "interval", HasArg: true, ArgName: "SECS", Help: "delay SECS between lines sent to the peer"},
		&Flag{Name: "exec", Short: 'e', Long: "exec", HasArg: true, ArgName: "CMD", Help: "execute CMD after connecting (refused on Windows)"},
		&Flag{Name: "sh-exec", Short: 'c', Long: "sh-exec", HasArg: true, ArgName: "CMD", Help: "execute CMD via a shell (refused on Windows)"},
	)
	if !parseOrDie("nc", f, args) {
		return 2
	}
	if handleHelpVersion("nc", "HOST PORT  |  -l [HOST] PORT", f) {
		return 0
	}

	o := &ncOpts{
		listen:  f.Has("listen"),
		udp:     f.Has("udp"),
		verbose: f.Count("verbose"),
		numeric: f.Has("no-dns"),
		zero:    f.Has("zero"),
		keep:    f.Has("keep-open"),
		ipv4:    f.Has("ipv4"),
		ipv6:    f.Has("ipv6"),
		srcPort: -1,
		quit:    0,
	}
	if f.Has("source-port") {
		n, err := f.Int("source-port", 0)
		if err != nil || n < 0 || n > 65535 {
			warn("nc", "invalid source port")
			return 1
		}
		o.srcPort = n
	}
	o.srcAddr = f.Str("source")
	if f.Has("timeout") {
		n, err := f.Int("timeout", 0)
		if err != nil || n < 0 {
			warn("nc", "invalid timeout '%s'", f.Str("timeout"))
			return 1
		}
		o.timeout = n
	}
	if f.Has("quit") {
		n, err := f.Int("quit", 0)
		if err != nil || n < 0 {
			warn("nc", "invalid quit delay '%s'", f.Str("quit"))
			return 1
		}
		o.quit = n
	}
	if f.Has("interval") {
		n, err := f.Int("interval", 0)
		if err != nil || n < 0 {
			warn("nc", "invalid interval '%s'", f.Str("interval"))
			return 1
		}
		o.interval = n
	}

	// Refuse the dangerous/unsupportable options honestly instead of faking them.
	if f.Has("exec") || f.Has("sh-exec") {
		warn("nc", "-e/-c (execute a command) is not supported on Windows (security); pipe to 'winux sh -c' instead")
		return 1
	}
	if o.ipv4 && o.ipv6 {
		warn("nc", "cannot use -4 and -6 together")
		return 1
	}
	if o.zero && o.listen {
		warn("nc", "cannot use -z with -l")
		return 1
	}
	if o.keep && !o.listen {
		warn("nc", "-k only makes sense with -l")
		return 1
	}

	network := "tcp"
	if o.udp {
		network = "udp"
	}
	if o.ipv4 {
		network += "4"
	}
	if o.ipv6 {
		network += "6"
	}

	if err := ncResolveOperands(o, f.Positional, network); err != nil {
		warn("nc", "%s", err)
		return 1
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	if o.listen {
		if o.udp {
			return ncListenUDP(o, network, sig)
		}
		return ncListen(o, network, sig)
	}
	return ncConnect(o, network, sig)
}

// ncResolveOperands maps the positional HOST PORT (or [HOST] PORT with -l)
// onto the options, honouring -p as the listen port / source port and
// accepting service names such as `nc host http`.
func ncResolveOperands(o *ncOpts, pos []string, network string) error {
	if o.listen {
		switch len(pos) {
		case 0:
			if o.srcPort < 0 {
				return fmt.Errorf("missing port number")
			}
		case 1:
			if o.srcPort >= 0 {
				o.host = pos[0]
			} else {
				p, err := ncParsePort(pos[0], network)
				if err != nil {
					return err
				}
				o.port = p
			}
		case 2:
			if o.srcPort >= 0 {
				return fmt.Errorf("port already specified with -p")
			}
			o.host = pos[0]
			p, err := ncParsePort(pos[1], network)
			if err != nil {
				return err
			}
			o.port = p
		default:
			return fmt.Errorf("too many arguments")
		}
		if o.srcPort >= 0 {
			o.port = o.srcPort
		}
	} else {
		if len(pos) < 2 {
			return fmt.Errorf("missing port number")
		}
		if len(pos) > 2 {
			return fmt.Errorf("too many arguments")
		}
		o.host = pos[0]
		p, err := ncParsePort(pos[1], network)
		if err != nil {
			return err
		}
		o.port = p
	}

	if o.numeric && o.host != "" && net.ParseIP(o.host) == nil {
		return fmt.Errorf("-n requires a numeric host address, got '%s'", o.host)
	}
	return nil
}

func ncParsePort(s, network string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 || n > 65535 {
			return 0, fmt.Errorf("invalid port '%s'", s)
		}
		return n, nil
	}
	if p, err := net.LookupPort(network, s); err == nil {
		return p, nil
	}
	return 0, fmt.Errorf("invalid port '%s'", s)
}

func ncVerboseAddr(o *ncOpts, host string, port int) {
	if o.verbose == 0 {
		return
	}
	proto := "tcp"
	if o.udp {
		proto = "udp"
	}
	name := serviceName(port)
	if name == "" {
		name = strconv.Itoa(port)
	}
	fmt.Fprintf(os.Stderr, "Connection to %s %s port [%s/%s] succeeded!\n", host, name, proto, name)
}

// ncConnect dials the peer; -z turns this into a pure connectivity probe.
func ncConnect(o *ncOpts, network string, sig chan os.Signal) int {
	d := net.Dialer{Timeout: time.Duration(o.timeout) * time.Second}
	if o.srcAddr != "" || o.srcPort >= 0 {
		la := net.JoinHostPort(o.srcAddr, strconv.Itoa(o.srcPort))
		if o.udp {
			if a, err := net.ResolveUDPAddr(network, la); err == nil {
				d.LocalAddr = a
			} else if o.verbose > 0 {
				fmt.Fprintf(os.Stderr, "nc: %v\n", err)
			}
		} else {
			if a, err := net.ResolveTCPAddr(network, la); err == nil {
				d.LocalAddr = a
			} else if o.verbose > 0 {
				fmt.Fprintf(os.Stderr, "nc: %v\n", err)
			}
		}
	}
	conn, err := d.Dial(network, net.JoinHostPort(o.host, strconv.Itoa(o.port)))
	if err != nil {
		if o.verbose > 0 {
			fmt.Fprintf(os.Stderr, "nc: %v\n", err)
		}
		return 1
	}
	defer conn.Close()
	ncVerboseAddr(o, o.host, o.port)
	if o.zero {
		return 0
	}
	if o.udp {
		return ncRelayUDP(conn, o)
	}
	return ncRelay(conn, o, sig)
}

// ncRelay forwards stdin to the connection and the connection to stdout until
// the peer closes (GNU semantics: a closed connection ends nc even when stdin
// still has data). A Ctrl+C aborts with exit code 130.
func ncRelay(c net.Conn, o *ncOpts, sig chan os.Signal) int {
	recvDone := make(chan struct{})
	go func() {
		buf := make([]byte, 32*1024)
		for {
			if o.timeout > 0 {
				c.SetReadDeadline(time.Now().Add(time.Duration(o.timeout) * time.Second))
			}
			n, err := c.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n])
			}
			if err != nil {
				close(recvDone)
				return
			}
			// a successful read refreshes the idle timeout (deadline re-set above)
		}
	}()
	sendDone := make(chan struct{})
	go func() {
		br := bufio.NewReader(stripBOM(os.Stdin))
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				c.Write(line)
				if o.interval > 0 {
					time.Sleep(time.Duration(o.interval) * time.Second)
				}
			}
			if err != nil {
				break
			}
		}
		if o.quit > 0 {
			time.Sleep(time.Duration(o.quit) * time.Second)
		}
		if tc, ok := c.(*net.TCPConn); ok {
			tc.CloseWrite() // half-close: the peer sees EOF on our side
		}
		close(sendDone)
	}()
	select {
	case <-recvDone:
		return 0
	case <-sig:
		c.Close()
		<-sendDone
		return 130
	}
}

// ncRelayUDP forwards stdin to a UDP socket; there is no half-close, so nc
// exits once stdin is exhausted (OpenBSD nc behaviour).
func ncRelayUDP(c net.Conn, o *ncOpts) int {
	go func() {
		buf := make([]byte, 32*1024)
		for {
			if o.timeout > 0 {
				c.SetReadDeadline(time.Now().Add(time.Duration(o.timeout) * time.Second))
			}
			n, err := c.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	br := bufio.NewReader(stripBOM(os.Stdin))
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			c.Write(line)
			if o.interval > 0 {
				time.Sleep(time.Duration(o.interval) * time.Second)
			}
		}
		if err != nil {
			return 0
		}
	}
}

// ncListen accepts one connection (or many with -k) and relays it like a
// client connection would be.
func ncListen(o *ncOpts, network string, sig chan os.Signal) int {
	ln, err := net.Listen(network, net.JoinHostPort(o.host, strconv.Itoa(o.port)))
	if err != nil {
		warn("nc", "%v", err)
		return 1
	}
	defer ln.Close()
	if o.verbose > 0 {
		fmt.Fprintf(os.Stderr, "Listening on %s\n", ln.Addr())
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-sig:
				return 130
			default:
				warn("nc", "%v", err)
				return 1
			}
		}
		if o.verbose > 0 {
			fmt.Fprintf(os.Stderr, "Connection from %s\n", conn.RemoteAddr())
		}
		code := ncRelay(conn, o, sig)
		conn.Close()
		if code != 0 || !o.keep {
			return code
		}
	}
}

// ncListenUDP binds a UDP socket and relays datagrams to stdout while stdin
// lines go back to the most recent sender. It keeps receiving after stdin EOF
// (a bare UDP listener has no close handshake); exit via -w idle timeout or
// Ctrl+C.
func ncListenUDP(o *ncOpts, network string, sig chan os.Signal) int {
	pc, err := net.ListenPacket(network, net.JoinHostPort(o.host, strconv.Itoa(o.port)))
	if err != nil {
		warn("nc", "%v", err)
		return 1
	}
	defer pc.Close()
	if o.verbose > 0 {
		fmt.Fprintf(os.Stderr, "Listening on %s\n", pc.LocalAddr())
	}
	var (
		mu   sync.Mutex
		peer net.Addr
	)
	go func() {
		br := bufio.NewReader(stripBOM(os.Stdin))
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				mu.Lock()
				p := peer
				mu.Unlock()
				if p != nil {
					pc.WriteTo(line, p)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	buf := make([]byte, 32*1024)
	for {
		if o.timeout > 0 {
			pc.SetReadDeadline(time.Now().Add(time.Duration(o.timeout) * time.Second))
		}
		n, from, err := pc.ReadFrom(buf)
		if n > 0 {
			mu.Lock()
			peer = from
			mu.Unlock()
			os.Stdout.Write(buf[:n])
			continue
		}
		if err != nil {
			if o.timeout > 0 {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					return 0
				}
			}
			select {
			case <-sig:
				return 130
			default:
				warn("nc", "%v", err)
				return 1
			}
		}
	}
}
