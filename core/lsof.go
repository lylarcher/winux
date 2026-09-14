package core

// lsof lists open files. Windows has no /proc, so this port covers what the
// native APIs actually expose: the network endpoints a process owns (the same
// GetExtendedTcpTable/GetExtendedUdpTable tables behind netstat and ss) and
// the process images themselves. The selection grammar, the columns and the
// exit status follow the real lsof, so the usual one-liners keep working:
//
//	winux lsof -i :1022        who is listening on 1022
//	winux lsof -i -a -u lyl    lyl's network connections
//	winux lsof -t -i :1022     just the pids (kill $(winux lsof -t -i:1022))
//	winux lsof -p 4321         everything winux can see for one process
//	winux lsof -c winux        by command name
//
// Everything is read-only: no handle is opened for writing and no state is
// modified.

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() {
	addCmd("lsof", "list open files (network endpoints and process images)", cmdLsof)
}

// lsofNetSel is one -i selector: [46][tcp|udp][@host][:port[-port]].
type lsofNetSel struct {
	family4, family6 bool
	proto            string // tcp, udp or "" for both
	host             string // address or name, "" for any
	portLo, portHi   int    // 0 = any port
}

// lsofOpts is the parsed command line.
type lsofOpts struct {
	net      []lsofNetSel
	pids     map[int]bool
	notPIDs  map[int]bool
	cmds     []string
	notCmds  []string
	users    []string
	notUsers []string
	paths    []string
	states   map[int]bool
	notState map[int]bool
	and      bool
	noHost   bool
	noPort   bool
	terse    bool
	netGiven bool
	anySel   bool
}

// lsofEntry is one output row (one open file of one process).
type lsofEntry struct {
	command string
	pid     int
	user    string
	fd      string // txt, or "-" for sockets (Windows exposes no fd numbers)
	typ     string // REG, IPv4, IPv6
	device  string
	sizeOff string
	node    string
	name    string
	// selection keys
	proto        string // "", tcp, tcp6, udp, udp6
	state        int
	lhost, rhost string
	lport, rport int
	path         string // image path for txt entries
}

func cmdLsof(args []string) int {
	o := &lsofOpts{
		pids:     map[int]bool{},
		notPIDs:  map[int]bool{},
		states:   map[int]bool{},
		notState: map[int]bool{},
	}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help" || a == "-h" || a == "-?":
			lsofUsage()
			return 0
		case a == "--version" || a == "-v":
			printVersion("lsof")
			return 0
		case a == "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		case a == "-a":
			o.and = true
		case a == "-n":
			o.noHost = true
		case a == "-P":
			o.noPort = true
		case a == "-t":
			o.terse = true
		case a == "-i":
			sel := lsofNetSel{family4: true, family6: true}
			if i+1 < len(args) && lsofNetSelectorArg(args[i+1]) {
				i++
				s, err := lsofParseNetSel(args[i])
				if err != nil {
					return lsofFail("%v", err)
				}
				sel = s
			}
			o.net = append(o.net, sel)
			o.netGiven, o.anySel = true, true
		case strings.HasPrefix(a, "-i") && len(a) > 2:
			s, err := lsofParseNetSel(a[2:])
			if err != nil {
				return lsofFail("%v", err)
			}
			o.net = append(o.net, s)
			o.netGiven, o.anySel = true, true
		case a == "-p" || (strings.HasPrefix(a, "-p") && len(a) > 2):
			v, ok := lsofFlagValue(args, &i, a)
			if !ok {
				return lsofFail("option -p requires a process id")
			}
			o.anySel = true
			if code := o.parsePIDs(v); code != 0 {
				return code
			}
		case a == "-c" || (strings.HasPrefix(a, "-c") && len(a) > 2):
			v, ok := lsofFlagValue(args, &i, a)
			if !ok || v == "" {
				return lsofFail("option -c requires a command name")
			}
			o.anySel = true
			if strings.HasPrefix(v, "^") {
				o.notCmds = append(o.notCmds, strings.ToLower(v[1:]))
			} else {
				o.cmds = append(o.cmds, strings.ToLower(v))
			}
		case a == "-u" || (strings.HasPrefix(a, "-u") && len(a) > 2):
			v, ok := lsofFlagValue(args, &i, a)
			if !ok || v == "" {
				return lsofFail("option -u requires a user name")
			}
			o.anySel = true
			if strings.HasPrefix(v, "^") {
				o.notUsers = append(o.notUsers, strings.ToLower(v[1:]))
			} else {
				o.users = append(o.users, strings.ToLower(v))
			}
		case a == "-s" || (strings.HasPrefix(a, "-s") && len(a) > 2):
			v, ok := lsofFlagValue(args, &i, a)
			if !ok || v == "" {
				return lsofFail("option -s requires PROTO:STATE (e.g. TCP:LISTEN)")
			}
			if code := o.parseStates(v); code != 0 {
				return code
			}
		case a == "-4":
			o.net = append(o.net, lsofNetSel{family4: true})
			o.netGiven, o.anySel = true, true
		case a == "-6":
			o.net = append(o.net, lsofNetSel{family6: true})
			o.netGiven, o.anySel = true, true
		default:
			if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+") {
				return lsofFail("unsupported option '%s' (winux lsof implements the common subset; see lsof --help)", a)
			}
			positional = append(positional, a)
		}
	}
	for _, p := range positional {
		o.anySel = true
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			o.pids[n] = true
		} else {
			o.paths = append(o.paths, strings.ToLower(p))
		}
	}

	entries, err := lsofCollect(o)
	if err != nil {
		return lsofFail("%v", err)
	}
	sel := entries[:0:0]
	for i := range entries {
		if o.selected(&entries[i]) {
			sel = append(sel, entries[i])
		}
	}
	sort.SliceStable(sel, func(i, j int) bool {
		a, b := sel[i], sel[j]
		if a.pid != b.pid {
			return a.pid < b.pid
		}
		if (a.fd == "txt") != (b.fd == "txt") {
			return a.fd == "txt"
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.fd < b.fd
	})
	if o.terse {
		last := -1
		for _, e := range sel {
			if e.pid != last {
				fmt.Println(e.pid)
				last = e.pid
			}
		}
	} else {
		fmt.Printf("%-9s %5s %-8s %-4s %-5s %-8s %-8s %-4s %s\n",
			"COMMAND", "PID", "USER", "FD", "TYPE", "DEVICE", "SIZE/OFF", "NODE", "NAME")
		for _, e := range sel {
			fmt.Printf("%-9s %5d %-8s %-4s %-5s %-8s %-8s %-4s %s\n",
				e.command, e.pid, e.user, e.fd, e.typ,
				e.device, e.sizeOff, e.node, e.name)
		}
	}
	if len(sel) == 0 {
		return 1 // lsof's convention: 1 = nothing matched
	}
	return 0
}

// lsofFlagValue returns the value of a flag written as -xVALUE or, failing
// that, the next argument (advancing *i).
func lsofFlagValue(args []string, i *int, a string) (string, bool) {
	if len(a) > 2 {
		return a[2:], true
	}
	if *i+1 < len(args) {
		*i++
		return args[*i], true
	}
	return "", false
}

func (o *lsofOpts) parsePIDs(v string) int {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		neg := strings.HasPrefix(part, "^")
		part = strings.TrimPrefix(part, "^")
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			return lsofFail("invalid process id '%s'", part)
		}
		if neg {
			o.notPIDs[n] = true
		} else {
			o.pids[n] = true
		}
	}
	return 0
}

// parseStates handles -s PROTO:STATE, e.g. TCP:LISTEN, TCP:^TIME_WAIT or the
// bare LISTEN form.
func (o *lsofOpts) parseStates(v string) int {
	neg := false
	spec := v
	if strings.HasPrefix(spec, "^") {
		neg, spec = true, spec[1:]
	}
	state := spec
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		proto := strings.ToLower(spec[:i])
		state = spec[i+1:]
		if proto != "tcp" {
			return lsofFail("only TCP states are available on Windows (got '%s')", spec[:i])
		}
	}
	if strings.HasPrefix(state, "^") {
		neg, state = true, state[1:]
	}
	n, ok := lsofStateByName(state)
	if !ok {
		return lsofFail("unknown TCP state '%s'", state)
	}
	if neg {
		o.notState[n] = true
	} else {
		o.states[n] = true
	}
	return 0
}

// lsofAllDigits reports whether s is a non-empty run of decimal digits.
func lsofAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// lsofNetSelectorArg reports whether the argument after -i is the selector
// rather than the next option or an unrelated operand.
func lsofNetSelectorArg(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	if s[0] == ':' || s[0] == '@' {
		return true
	}
	if _, err := strconv.Atoi(s); err == nil {
		return true // friendly: -i 1022 means -i:1022
	}
	lower := strings.ToLower(s)
	for _, p := range []string{"tcp", "udp", "4", "6"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

func lsofParseNetSel(s string) (lsofNetSel, error) {
	sel := lsofNetSel{family4: true, family6: true}
	rest := s
	// A leading 4 or 6 means the address family — except when the whole
	// selector is a bare port number, because `-i 63389` must keep meaning
	// port 63389 rather than "IPv6, port 3389".
	for len(rest) > 0 && (rest[0] == '4' || rest[0] == '6') {
		if len(rest) > 1 && lsofAllDigits(rest) {
			break
		}
		if rest[0] == '4' {
			sel.family4, sel.family6 = true, false
		} else {
			sel.family4, sel.family6 = false, true
		}
		rest = rest[1:]
	}
	lower := strings.ToLower(rest)
	for _, p := range []string{"tcp6", "udp6", "tcp4", "udp4", "tcp", "udp"} {
		if strings.HasPrefix(lower, p) {
			sel.proto = p[:3]
			switch {
			case strings.HasSuffix(p, "4"):
				sel.family4, sel.family6 = true, false
			case strings.HasSuffix(p, "6"):
				sel.family4, sel.family6 = false, true
			}
			rest = rest[len(p):]
			break
		}
	}
	if i := strings.IndexByte(rest, '@'); i >= 0 {
		host, portSpec := lsofSplitHostPort(rest[i+1:])
		sel.host = host
		if portSpec != "" {
			lo, hi, err := lsofParsePortSpec(portSpec)
			if err != nil {
				return sel, err
			}
			sel.portLo, sel.portHi = lo, hi
		}
		rest = ""
	}
	switch {
	case strings.HasPrefix(rest, ":"):
		lo, hi, err := lsofParsePortSpec(rest[1:])
		if err != nil {
			return sel, err
		}
		sel.portLo, sel.portHi = lo, hi
	case rest != "":
		lo, hi, err := lsofParsePortSpec(rest)
		if err != nil {
			return sel, err
		}
		sel.portLo, sel.portHi = lo, hi
	}
	return sel, nil
}

// lsofSplitHostPort splits "@host" from an optional trailing ":port",
// understanding the [::1]:80 form for IPv6 literals.
func lsofSplitHostPort(s string) (host, port string) {
	if strings.HasPrefix(s, "[") {
		if i := strings.IndexByte(s, ']'); i >= 0 {
			host = s[:i+1]
			if rest := s[i+1:]; strings.HasPrefix(rest, ":") {
				port = rest[1:]
			}
			return host, port
		}
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 && strings.Count(s, ":") == 1 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func lsofParsePortSpec(spec string) (int, int, error) {
	if spec == "" {
		return 0, 0, fmt.Errorf("empty port in internet address selector")
	}
	lo, hi := spec, ""
	if i := strings.IndexByte(spec, '-'); i >= 0 {
		lo, hi = spec[:i], spec[i+1:]
	}
	loN, err := lsofPortValue(lo)
	if err != nil {
		return 0, 0, err
	}
	hiN := loN
	if hi != "" {
		if hiN, err = lsofPortValue(hi); err != nil {
			return 0, 0, err
		}
	}
	if hiN < loN {
		loN, hiN = hiN, loN
	}
	return loN, hiN, nil
}

func lsofPortValue(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty port in internet address selector")
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 || n > 65535 {
			return 0, fmt.Errorf("port %d out of range", n)
		}
		return n, nil
	}
	if n, ok := servicePort(s); ok {
		return n, nil
	}
	return 0, fmt.Errorf("unknown port or service '%s'", s)
}

// ------------------------------------------------------------------ collect --

// lsofProc is the cached per-pid information.
type lsofProc struct{ command, path, user string }

type lsofTable struct {
	procs map[int]lsofProc
}

func (t *lsofTable) get(pid int) lsofProc {
	if p, ok := t.procs[pid]; ok {
		return p
	}
	name, path, user := lsofProcInfo(pid)
	if name == "" {
		name = "?"
	}
	if user == "" {
		user = "?"
	}
	p := lsofProc{command: name, path: path, user: user}
	t.procs[pid] = p
	return p
}

func lsofCollect(o *lsofOpts) ([]lsofEntry, error) {
	table := &lsofTable{procs: map[int]lsofProc{}}
	var entries []lsofEntry

	// txt entries: needed unless the only selection is -i (an internet
	// selector can never match a process image), which keeps the common
	// `lsof -i:PORT` case from walking every process on the machine.
	txtNeeded := !o.netGiven || len(o.pids) > 0 || len(o.notPIDs) > 0 ||
		len(o.cmds) > 0 || len(o.notCmds) > 0 || len(o.users) > 0 || len(o.notUsers) > 0 || len(o.paths) > 0
	if txtNeeded && len(o.states)+len(o.notState) == 0 {
		pids := make([]int, 0, 256)
		if len(o.pids) > 0 {
			for pid := range o.pids {
				pids = append(pids, pid)
			}
			sort.Ints(pids)
		} else {
			all, err := lsofPIDs()
			if err != nil {
				return nil, err
			}
			pids = all
		}
		for _, pid := range pids {
			p := table.get(pid)
			if p.path == "" {
				continue
			}
			entries = append(entries, lsofEntry{
				command: p.command,
				pid:     pid,
				user:    p.user,
				fd:      "txt",
				typ:     "REG",
				device:  "0x0",
				sizeOff: "0t0",
				node:    "-",
				name:    p.path,
				path:    p.path,
			})
		}
	}

	// sockets: always collected (plain lsof lists them too).
	rows, err := lsofSockets(o)
	if err != nil {
		return nil, err
	}
	lsofWarmHosts(rows, o.noHost)
	for _, r := range rows {
		p := table.get(r.pid)
		entries = append(entries, lsofEntry{
			command: p.command,
			pid:     r.pid,
			user:    p.user,
			fd:      "-",
			typ:     lsofType(r.proto),
			device:  "0x0",
			sizeOff: "0t0",
			node:    strings.ToUpper(r.proto[:3]),
			name:    lsofNetName(r, o.noHost, o.noPort),
			proto:   r.proto,
			state:   r.state,
			lhost:   lsofHostOf(r.local),
			rhost:   lsofHostOf(r.remote),
			lport:   lsofPortOf(r.local),
			rport:   lsofPortOf(r.remote),
		})
	}
	return entries, nil
}

// lsofSockets returns the TCP and UDP rows (IPv4 and IPv6) the -i selectors
// ask for; without -i every socket is a candidate.
func lsofSockets(o *lsofOpts) ([]sockRow, error) {
	fam4, fam6 := true, true
	if o.netGiven {
		fam4, fam6 = false, false
		for _, s := range o.net {
			fam4 = fam4 || s.family4
			fam6 = fam6 || s.family6
		}
	}
	proto := ""
	if o.netGiven {
		for _, s := range o.net {
			if s.proto == "" {
				proto = ""
				break
			}
			if proto == "" {
				proto = s.proto
			} else if proto != s.proto {
				proto = ""
				break
			}
		}
	}
	var rows []sockRow
	if proto != "udp" {
		r, err := tcpRows()
		if err != nil {
			return nil, err
		}
		rows = append(rows, r...)
	}
	if proto != "tcp" {
		r, err := udpRows()
		if err != nil {
			return nil, err
		}
		rows = append(rows, r...)
	}
	// Without -i a socket can only be selected through its owner, so when the
	// selection is a plain pid list there is no point resolving the account of
	// every other socket owner on the machine.
	pidFilter := len(o.pids) > 0 && !o.netGiven && len(o.cmds) == 0 && len(o.notCmds) == 0 &&
		len(o.users) == 0 && len(o.notUsers) == 0 && len(o.paths) == 0
	out := rows[:0:0]
	for _, r := range rows {
		// pid 0 owns the sockets the kernel keeps (TIME_WAIT, closed
		// connections); real lsof does not report those either — ss and
		// netstat still do.
		if r.pid == 0 {
			continue
		}
		if pidFilter && !o.pids[r.pid] {
			continue
		}
		isV6 := strings.HasSuffix(r.proto, "6")
		if (isV6 && !fam6) || (!isV6 && !fam4) {
			continue
		}
		if proto != "" && !strings.HasPrefix(r.proto, proto) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func lsofType(proto string) string {
	if strings.HasSuffix(proto, "6") {
		return "IPv6"
	}
	return "IPv4"
}

// ---------------------------------------------------------------- selection --

func (o *lsofOpts) selected(e *lsofEntry) bool {
	if len(o.states)+len(o.notState) > 0 {
		// -s only ever selects TCP states
		if e.proto != "tcp" && e.proto != "tcp6" {
			return false
		}
		if len(o.states) > 0 && !o.states[e.state] {
			return false
		}
		if o.notState[e.state] {
			return false
		}
	}
	if len(o.notPIDs) > 0 && o.notPIDs[e.pid] {
		return false
	}
	if len(o.notCmds) > 0 && lsofCmdMatch(e.command, o.notCmds) {
		return false
	}
	if len(o.notUsers) > 0 && lsofUserMatch(e.user, o.notUsers) {
		return false
	}
	if !o.anySel {
		return true
	}
	var checks []bool
	if o.netGiven {
		checks = append(checks, o.matchNet(e))
	}
	if len(o.pids) > 0 {
		checks = append(checks, o.pids[e.pid])
	}
	if len(o.cmds) > 0 {
		checks = append(checks, lsofCmdMatch(e.command, o.cmds))
	}
	if len(o.users) > 0 {
		checks = append(checks, lsofUserMatch(e.user, o.users))
	}
	if len(o.paths) > 0 {
		checks = append(checks, e.path != "" && lsofPathMatch(e.path, o.paths))
	}
	if len(checks) == 0 {
		return false // only negated selections were given
	}
	if o.and {
		for _, c := range checks {
			if !c {
				return false
			}
		}
		return true
	}
	for _, c := range checks {
		if c {
			return true
		}
	}
	return false
}

func (o *lsofOpts) matchNet(e *lsofEntry) bool {
	if e.proto == "" {
		return false
	}
	for _, s := range o.net {
		if s.matches(e) {
			return true
		}
	}
	return false
}

func (s lsofNetSel) matches(e *lsofEntry) bool {
	isV6 := strings.HasSuffix(e.proto, "6")
	if isV6 && !s.family6 {
		return false
	}
	if !isV6 && !s.family4 {
		return false
	}
	if s.proto != "" && !strings.HasPrefix(e.proto, s.proto) {
		return false
	}
	if s.host != "" && !lsofHostEq(s.host, e.lhost) && !lsofHostEq(s.host, e.rhost) {
		return false
	}
	if s.portLo > 0 {
		inLocal := e.lport >= s.portLo && e.lport <= s.portHi
		inRemote := e.rport >= s.portLo && e.rport <= s.portHi
		if !inLocal && !inRemote {
			return false
		}
	}
	return true
}

func lsofHostEq(want, have string) bool {
	want = strings.Trim(want, "[]")
	have = strings.Trim(have, "[]")
	return strings.EqualFold(want, have)
}

func lsofCmdMatch(command string, pats []string) bool {
	c := strings.ToLower(command)
	for _, p := range pats {
		if strings.HasPrefix(c, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func lsofUserMatch(user string, pats []string) bool {
	u := strings.ToLower(user)
	short := u
	if i := strings.LastIndexByte(u, '\\'); i >= 0 {
		short = u[i+1:]
	}
	for _, p := range pats {
		p = strings.ToLower(p)
		if u == p || short == p {
			return true
		}
	}
	return false
}

func lsofPathMatch(path string, pats []string) bool {
	p := strings.ToLower(path)
	for _, want := range pats {
		if strings.HasPrefix(p, want) {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ output --

// lsofNetName renders the NAME column the way lsof does: sockets show the
// local endpoint, an arrow to the peer when there is one, and the connection
// state in parentheses.
func lsofNetName(r sockRow, noHost, noPort bool) string {
	local := lsofAddr(lsofHostOf(r.local), lsofPortOf(r.local), noHost, noPort)
	if r.state == 0 { // UDP
		if lsofPortOf(r.remote) == 0 {
			return local
		}
		return local + "->" + lsofAddr(lsofHostOf(r.remote), lsofPortOf(r.remote), noHost, noPort)
	}
	if r.state == tcpListen {
		return local + " (LISTEN)"
	}
	peer := lsofAddr(lsofHostOf(r.remote), lsofPortOf(r.remote), noHost, noPort)
	return local + "->" + peer + " (" + lsofStateName(r.state) + ")"
}

func lsofAddr(host string, port int, noHost, noPort bool) string {
	h := host
	switch {
	case h == "" || h == "0.0.0.0" || h == "::":
		h = "*"
	case !noHost:
		h = lsofHostName(h)
	}
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	if port <= 0 {
		return h + ":*"
	}
	p := strconv.Itoa(port)
	if !noPort {
		if name := serviceName(port); name != "" {
			p = name
		}
	}
	return h + ":" + p
}

var lsofHostCache sync.Map

// lsofWarmHosts resolves every distinct peer address up front, in parallel:
// doing it lazily while printing would serialise the lookups and a machine
// whose DNS is slow or unreachable would stall lsof for seconds.
func lsofWarmHosts(rows []sockRow, noHost bool) {
	if noHost {
		return
	}
	seen := map[string]bool{}
	var hosts []string
	for _, r := range rows {
		for _, addr := range []string{r.local, r.remote} {
			h := lsofHostOf(addr)
			if h == "" || h == "0.0.0.0" || h == "::" || seen[h] {
				continue
			}
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		return
	}
	const workers = 16
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, h := range hosts {
		wg.Add(1)
		sem <- struct{}{}
		go func(h string) {
			defer wg.Done()
			defer func() { <-sem }()
			lsofHostName(h)
		}(h)
	}
	wg.Wait()
}

// lsofHostName reverse-resolves an address with a short deadline (a broken
// resolver must not stall lsof) and caches the answer; -n skips this entirely.
func lsofHostName(ip string) string {
	if v, ok := lsofHostCache.Load(ip); ok {
		return v.(string)
	}
	name := ip
	if net.ParseIP(ip) != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		if names, err := net.DefaultResolver.LookupAddr(ctx, ip); err == nil && len(names) > 0 {
			name = strings.TrimSuffix(names[0], ".")
		}
		cancel()
	}
	lsofHostCache.Store(ip, name)
	return name
}

// lsofHostOf / lsofPortOf split the "host:port" strings built by the socket
// table readers (IPv6 addresses already contain colons).
func lsofHostOf(addr string) string {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return addr
	}
	return addr[:i]
}

func lsofPortOf(addr string) int {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(addr[i+1:])
	return n
}

// lsofStateName uses lsof's spellings (LISTEN, not LISTENING).
func lsofStateName(state int) string {
	switch state {
	case tcpClosed:
		return "CLOSED"
	case tcpListen:
		return "LISTEN"
	case tcpSynSent:
		return "SYN_SENT"
	case tcpSynReceived:
		return "SYN_RCVD"
	case tcpEstablished:
		return "ESTABLISHED"
	case tcpFinWait1:
		return "FIN_WAIT1"
	case tcpFinWait2:
		return "FIN_WAIT2"
	case tcpCloseWait:
		return "CLOSE_WAIT"
	case tcpClosing:
		return "CLOSING"
	case tcpLastAck:
		return "LAST_ACK"
	case tcpTimeWait:
		return "TIME_WAIT"
	case tcpDeleteTcb:
		return "DELETE_TCB"
	}
	return "UNKNOWN"
}

// lsofStateByName accepts both the lsof spellings and the Windows ones.
func lsofStateByName(s string) (int, bool) {
	switch strings.ToUpper(strings.ReplaceAll(s, "-", "_")) {
	case "CLOSED":
		return tcpClosed, true
	case "LISTEN", "LISTENING":
		return tcpListen, true
	case "SYN_SENT":
		return tcpSynSent, true
	case "SYN_RCVD", "SYN_RECV", "SYN_RECEIVED":
		return tcpSynReceived, true
	case "ESTABLISHED", "ESTAB", "ESTABLISH":
		return tcpEstablished, true
	case "FIN_WAIT1", "FIN_WAIT_1":
		return tcpFinWait1, true
	case "FIN_WAIT2", "FIN_WAIT_2":
		return tcpFinWait2, true
	case "CLOSE_WAIT":
		return tcpCloseWait, true
	case "CLOSING":
		return tcpClosing, true
	case "LAST_ACK":
		return tcpLastAck, true
	case "TIME_WAIT":
		return tcpTimeWait, true
	case "DELETE_TCB":
		return tcpDeleteTcb, true
	}
	return 0, false
}

func lsofFail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "lsof: "+format+"\n", a...)
	return 1
}

func lsofUsage() {
	fmt.Print(`winux lsof — list open files (network endpoints and process images)

Usage: winux lsof [OPTION]... [PID]... [PATH]

selection:
  -i [SELECTOR]    internet files; SELECTOR is [46][tcp|udp][@host][:port[-port]]
                   e.g. -i:1022, -i :1022, -iTCP@10.0.0.1:22, -i:80-90, -i:ssh
                   (repeatable; without a selector every socket matches)
  -p PID[,PID]     select by process id; ^PID excludes
  -c NAME          select commands starting with NAME; ^NAME excludes
  -u USER          select by owner account (lyl or DOMAIN\lyl); ^USER excludes
  -s PROTO:STATE   select TCP states, e.g. -sTCP:LISTEN, -sTCP:^TIME_WAIT
  -a               AND the selections instead of OR-ing them (union)
  -4, -6           IPv4 / IPv6 sockets only
  PID, PATH        a bare pid selects that process; a path selects processes
                   whose image lives under it

output:
  -t               terse: print only pids, one per line
  -n               numeric hosts (skip reverse DNS)
  -P               numeric ports (skip service names)
  -h, --help       this help
  -v, --version    version information

columns: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
exit status: 0 when at least one file matched, 1 when nothing matched.

Windows keeps no per-process file handle table, so winux lsof reports the
process image (FD txt) and the sockets the process owns (TYPE IPv4/IPv6) —
memory maps, pipes and anonymous handles are not exposed by the native APIs.
Sockets owned by the kernel (pid 0: TIME_WAIT and other closed connections)
are left out, exactly as in lsof; ss and netstat still show them. The command
is read-only.

examples:
  winux lsof -i :1022            what is listening on 1022
  winux lsof -i -a -u lyl        lyl's network connections
  winux lsof -t -i :1022         just the pids (kill $(winux lsof -t -i:1022))
  winux lsof -c winux            every winux.exe endpoint
`)
}
