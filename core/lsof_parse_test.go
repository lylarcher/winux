package core

// Unit tests for the lsof selection grammar. The parsing and the union/AND
// rules are pure logic, so they are tested here directly; the CLI-level
// behaviour (real sockets, exit status) lives in test/lsof_test.go.

import (
	"strconv"
	"strings"
	"testing"
)

func TestLsofParseNetSel(t *testing.T) {
	cases := []struct {
		in       string
		family4  bool
		family6  bool
		proto    string
		host     string
		lo, hi   int
		wantFail bool
	}{
		{in: "", family4: true, family6: true},
		{in: "4", family4: true},
		{in: "6", family6: true},
		{in: "tcp", family4: true, family6: true, proto: "tcp"},
		{in: "UDP", family4: true, family6: true, proto: "udp"},
		{in: "tcp6", family6: true, proto: "tcp"},
		{in: ":80", family4: true, family6: true, lo: 80, hi: 80},
		{in: ":80-90", family4: true, family6: true, lo: 80, hi: 90},
		{in: ":90-80", family4: true, family6: true, lo: 80, hi: 90},
		{in: "@127.0.0.1", family4: true, family6: true, host: "127.0.0.1"},
		{in: "@127.0.0.1:22", family4: true, family6: true, host: "127.0.0.1", lo: 22, hi: 22},
		{in: "@[::1]:80", family4: true, family6: true, host: "[::1]", lo: 80, hi: 80},
		{in: "tcp@10.0.0.1:22-23", family4: true, family6: true, proto: "tcp", host: "10.0.0.1", lo: 22, hi: 23},
		{in: "ssh", family4: true, family6: true, lo: 22, hi: 22}, // service name
		// a bare port number keeps every digit: the leading 4/6 is a family
		// prefix only when it is not part of the number
		{in: "63389", family4: true, family6: true, lo: 63389, hi: 63389},
		{in: "49152", family4: true, family6: true, lo: 49152, hi: 49152},
		{in: "4tcp", family4: true, proto: "tcp"},
		// IPv6 literals need the bracketed form to carry a port
		{in: "6tcp@[::1]:22", family6: true, proto: "tcp", host: "[::1]", lo: 22, hi: 22},
		{in: "@::1", family4: true, family6: true, host: "::1"},
		{in: "99999", wantFail: true},
		{in: "no-such-service", wantFail: true},
		{in: ":", wantFail: true},
		{in: "@host:notaport", wantFail: true},
	}
	for _, c := range cases {
		got, err := lsofParseNetSel(c.in)
		if c.wantFail {
			if err == nil {
				t.Errorf("lsofParseNetSel(%q) = %+v, want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("lsofParseNetSel(%q) failed: %v", c.in, err)
			continue
		}
		if got.family4 != c.family4 || got.family6 != c.family6 || got.proto != c.proto ||
			got.host != c.host || got.portLo != c.lo || got.portHi != c.hi {
			t.Errorf("lsofParseNetSel(%q) = %+v, want family4=%v family6=%v proto=%q host=%q port=%d-%d",
				c.in, got, c.family4, c.family6, c.proto, c.host, c.lo, c.hi)
		}
	}
}

func TestLsofStateNames(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"LISTEN", tcpListen}, {"listening", tcpListen},
		{"ESTABLISHED", tcpEstablished}, {"ESTAB", tcpEstablished},
		{"TIME_WAIT", tcpTimeWait}, {"time-wait", tcpTimeWait},
		{"SYN_SENT", tcpSynSent}, {"SYN_RECEIVED", tcpSynReceived},
		{"CLOSE_WAIT", tcpCloseWait}, {"FIN_WAIT2", tcpFinWait2},
	} {
		got, ok := lsofStateByName(c.in)
		if !ok || got != c.want {
			t.Errorf("lsofStateByName(%q) = %d,%v want %d", c.in, got, ok, c.want)
		}
	}
	if _, ok := lsofStateByName("BOGUS"); ok {
		t.Error("lsofStateByName(BOGUS) should fail")
	}
	// the NAME column uses lsof's own spellings
	if lsofStateName(tcpListen) != "LISTEN" || lsofStateName(tcpEstablished) != "ESTABLISHED" {
		t.Errorf("state spellings: %q/%q", lsofStateName(tcpListen), lsofStateName(tcpEstablished))
	}
}

func TestLsofAddrSplitting(t *testing.T) {
	for _, c := range []struct {
		addr, host string
		port       int
	}{
		{"127.0.0.1:1022", "127.0.0.1", 1022},
		{"0.0.0.0:0", "0.0.0.0", 0},
		{"::1:1022", "::1", 1022}, // IPv6 literals keep their colons
		{":::0", "::", 0},
	} {
		if got := lsofHostOf(c.addr); got != c.host {
			t.Errorf("lsofHostOf(%q) = %q want %q", c.addr, got, c.host)
		}
		if got := lsofPortOf(c.addr); got != c.port {
			t.Errorf("lsofPortOf(%q) = %d want %d", c.addr, got, c.port)
		}
	}
}

func TestLsofNetName(t *testing.T) {
	listen := sockRow{proto: "tcp", state: tcpListen, local: "0.0.0.0:1022", remote: "0.0.0.0:0"}
	if got := lsofNetName(listen, true, true); got != "*:1022 (LISTEN)" {
		t.Errorf("listener NAME = %q", got)
	}
	estab := sockRow{proto: "tcp", state: tcpEstablished, local: "10.0.0.5:1022", remote: "10.0.0.9:51515"}
	if got := lsofNetName(estab, true, true); got != "10.0.0.5:1022->10.0.0.9:51515 (ESTABLISHED)" {
		t.Errorf("established NAME = %q", got)
	}
	v6 := sockRow{proto: "tcp6", state: tcpListen, local: ":::1022", remote: ":::0"}
	if got := lsofNetName(v6, true, true); got != "*:1022 (LISTEN)" {
		t.Errorf("IPv6 listener NAME = %q", got)
	}
	udp := sockRow{proto: "udp", local: "0.0.0.0:5353", remote: "*:*"}
	if got := lsofNetName(udp, true, true); got != "*:5353" {
		t.Errorf("UDP NAME = %q", got)
	}
	// without -P the port becomes the service name
	web := sockRow{proto: "tcp", state: tcpListen, local: "0.0.0.0:80", remote: "0.0.0.0:0"}
	if got := lsofNetName(web, true, false); got != "*:http (LISTEN)" {
		t.Errorf("service-name NAME = %q", got)
	}
}

// newLsofOpts mirrors what cmdLsof builds, without touching the OS.
func newLsofOpts(t *testing.T, args ...string) *lsofOpts {
	t.Helper()
	o := &lsofOpts{
		pids:     map[int]bool{},
		notPIDs:  map[int]bool{},
		states:   map[int]bool{},
		notState: map[int]bool{},
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-a":
			o.and = true
		case a == "-i":
			sel := lsofNetSel{family4: true, family6: true}
			if i+1 < len(args) && lsofNetSelectorArg(args[i+1]) {
				i++
				s, err := lsofParseNetSel(args[i])
				if err != nil {
					t.Fatalf("%v", err)
				}
				sel = s
			}
			o.net = append(o.net, sel)
			o.netGiven, o.anySel = true, true
		case a == "-p":
			i++
			if code := o.parsePIDs(args[i]); code != 0 {
				t.Fatalf("parsePIDs(%q) failed", args[i])
			}
			o.anySel = true
		case a == "-c":
			i++
			o.cmds = append(o.cmds, args[i])
			o.anySel = true
		case a == "-u":
			i++
			o.users = append(o.users, args[i])
			o.anySel = true
		case a == "-s":
			i++
			if code := o.parseStates(args[i]); code != 0 {
				t.Fatalf("parseStates(%q) failed", args[i])
			}
		case a == "-6":
			o.net = append(o.net, lsofNetSel{family6: true})
			o.netGiven, o.anySel = true, true
		default:
			// positional: a bare pid, or a path prefix (as cmdLsof does)
			o.anySel = true
			if n, err := strconv.Atoi(a); err == nil && n > 0 {
				o.pids[n] = true
			} else {
				o.paths = append(o.paths, strings.ToLower(a))
			}
		}
	}
	return o
}

func txtEntry(pid int, command, user, path string) lsofEntry {
	return lsofEntry{command: command, pid: pid, user: user, fd: "txt", typ: "REG", path: path, name: path}
}

func sockEntry(pid int, command, user, proto string, state int, lhost string, lport int, rhost string, rport int) lsofEntry {
	return lsofEntry{
		command: command, pid: pid, user: user, fd: "-", typ: lsofType(proto),
		proto: proto, state: state, lhost: lhost, rport: rport, rhost: rhost, lport: lport,
	}
}

func TestLsofSelectionRules(t *testing.T) {
	winuxTxt := txtEntry(5, "winux.exe", `DOM\lyl`, `D:\tools\winux.exe`)
	otherTxt := txtEntry(6, "svchost.exe", `DOM\lyl`, `C:\Windows\System32\svchost.exe`)
	winux80 := sockEntry(5, "winux.exe", `DOM\lyl`, "tcp", tcpListen, "0.0.0.0", 80, "0.0.0.0", 0)
	other80 := sockEntry(6, "svchost.exe", `DOM\lyl`, "tcp", tcpListen, "0.0.0.0", 80, "0.0.0.0", 0)
	otherEstab := sockEntry(6, "svchost.exe", `DOM\lyl`, "tcp", tcpEstablished, "10.0.0.1", 5000, "10.0.0.2", 443)
	otherUDP := sockEntry(6, "svchost.exe", `DOM\lyl`, "udp", 0, "0.0.0.0", 5353, "*", 0)
	v6 := sockEntry(7, "svchost.exe", `DOM\lyl`, "tcp6", tcpListen, "::", 80, "::", 0)
	adminTxt := txtEntry(8, "lsass.exe", "SYSTEM", `C:\Windows\System32\lsass.exe`)

	cases := []struct {
		name  string
		args  []string
		entry lsofEntry
		want  bool
	}{
		// plain lsof lists everything winux can see
		{"no selection: txt", nil, winuxTxt, true},
		{"no selection: socket", nil, otherUDP, true},
		// -i selects sockets only
		{"-i:80 local port", []string{"-i", ":80"}, winux80, true},
		{"-i:80 peer port", []string{"-i", ":80"}, otherEstab, false},
		{"-i:443 peer port", []string{"-i", ":443"}, otherEstab, true},
		{"-i excludes txt", []string{"-i", ":80"}, winuxTxt, false},
		{"-i:80 excludes udp 5353", []string{"-i", ":80"}, otherUDP, false},
		{"-i proto tcp excludes udp", []string{"-i", "tcp"}, otherUDP, false},
		{"-i proto udp", []string{"-i", "udp"}, otherUDP, true},
		{"-i host", []string{"-i", "@10.0.0.2"}, otherEstab, true},
		{"-i host mismatch", []string{"-i", "@10.0.0.3"}, otherEstab, false},
		{"-i6 excludes ipv4", []string{"-i", "6"}, winux80, false},
		{"-i6 matches ipv6", []string{"-i", "6"}, v6, true},
		// -p selects the whole process
		{"-p 5 txt", []string{"-p", "5"}, winuxTxt, true},
		{"-p 5 socket", []string{"-p", "5"}, winux80, true},
		{"-p 5 excludes other pid", []string{"-p", "5"}, other80, false},
		{"-p ^5 excludes", []string{"-p", "^5"}, winux80, false},
		// union by default, AND with -a
		{"union -i -p 6", []string{"-i", ":80", "-p", "6"}, winux80, true},
		{"union -i -p 5 txt", []string{"-i", ":80", "-p", "5"}, winuxTxt, true},
		{"union -i -p 5 other txt", []string{"-i", ":80", "-p", "5"}, otherTxt, false},
		{"and -i -p 5", []string{"-a", "-i", ":80", "-p", "5"}, winux80, true},
		{"and -i -p 5 other", []string{"-a", "-i", ":80", "-p", "5"}, other80, false},
		{"and drops txt", []string{"-a", "-i", ":80", "-p", "5"}, winuxTxt, false},
		// -c / -u
		{"-c prefix", []string{"-c", "winux"}, winux80, true},
		{"-c prefix miss", []string{"-c", "winux"}, other80, false},
		{"-u short name", []string{"-u", "lyl"}, winux80, true},
		{"-u qualified name", []string{"-u", `DOM\lyl`}, winux80, true},
		{"-u miss", []string{"-u", "bob"}, winux80, false},
		{"-u system", []string{"-u", "system"}, adminTxt, true},
		// -s selects TCP states (and drops everything that is not TCP)
		{"-sTCP:LISTEN", []string{"-s", "TCP:LISTEN"}, winux80, true},
		{"-sTCP:LISTEN drops estab", []string{"-s", "TCP:LISTEN"}, otherEstab, false},
		{"-sTCP:LISTEN drops udp", []string{"-s", "TCP:LISTEN"}, otherUDP, false},
		{"-sTCP:LISTEN drops txt", []string{"-s", "TCP:LISTEN"}, winuxTxt, false},
		{"-sTCP:^LISTEN keeps estab", []string{"-s", "TCP:^LISTEN"}, otherEstab, true},
		{"-sTCP:^LISTEN drops listen", []string{"-s", "TCP:^LISTEN"}, winux80, false},
		// path selection
		{"path prefix", []string{"D:\\tools"}, winuxTxt, true},
		{"path prefix miss", []string{"D:\\tools"}, otherTxt, false},
	}
	for _, c := range cases {
		o := newLsofOpts(t, c.args...)
		if got := o.selected(&c.entry); got != c.want {
			t.Errorf("%s: selected = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLsofServicePort(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{{"ssh", 22}, {"http", 80}, {"https", 443}, {"domain", 53}, {"8080", 8080}} {
		got, ok := servicePort(c.in)
		if !ok || got != c.want {
			t.Errorf("servicePort(%q) = %d,%v want %d", c.in, got, ok, c.want)
		}
	}
	if _, ok := servicePort("no-such-service"); ok {
		t.Error("servicePort(no-such-service) should fail")
	}
	if _, ok := servicePort("99999"); ok {
		t.Error("servicePort(99999) should fail (out of range)")
	}
	if serviceName(22) != "ssh" || serviceName(443) != "https" {
		t.Errorf("serviceName fallback broken: %q %q", serviceName(22), serviceName(443))
	}
}
