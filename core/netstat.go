package core

// netstat (Windows-style) and ss (iproute2-style) both read the same native
// socket tables. netstat follows the Windows conventions (Proto/Local/
// Foreign/State columns, LISTENING/ESTABLISHED spellings, -p as a protocol
// filter and -o for the owning PID), while ss follows the Linux conventions
// (LISTEN/ESTAB spellings, -p showing the owning process, -s for a summary).
// Everything is read-only: these commands never alter the network state.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func init() {
	addCmd("netstat", "print network connections, routing tables and interface statistics", cmdNetstat)
	addCmd("ss", "investigate sockets (iproute2 style)", cmdSs)
}

// formatAddr renders host:port for netstat/ss output, resolving the port to a
// service name unless numeric is set.
func formatAddr(addr string, numeric bool) string {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return addr
	}
	host, portStr := addr[:i], addr[i+1:]
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	if numeric {
		return addr
	}
	if name := serviceName(port); name != "" {
		return host + ":" + name
	}
	return addr
}

// tcpStateWindows is the Windows netstat spelling of a MIB TCP state.
func tcpStateWindows(state int) string {
	switch state {
	case tcpClosed:
		return "CLOSED"
	case tcpListen:
		return "LISTENING"
	case tcpSynSent:
		return "SYN_SENT"
	case tcpSynReceived:
		return "SYN_RECEIVED"
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

// tcpStateSS is the Linux ss spelling of a MIB TCP state.
func tcpStateSS(state int) string {
	switch state {
	case tcpClosed:
		return "CLOSED"
	case tcpListen:
		return "LISTEN"
	case tcpSynSent:
		return "SYN-SENT"
	case tcpSynReceived:
		return "SYN-RECV"
	case tcpEstablished:
		return "ESTAB"
	case tcpFinWait1:
		return "FIN-WAIT-1"
	case tcpFinWait2:
		return "FIN-WAIT-2"
	case tcpCloseWait:
		return "CLOSE-WAIT"
	case tcpClosing:
		return "CLOSING"
	case tcpLastAck:
		return "LAST-ACK"
	case tcpTimeWait:
		return "TIME-WAIT"
	case tcpDeleteTcb:
		return "DELET-TCB"
	}
	return "UNKNOWN"
}

// socketSelection turns the user flags into a (tcp, udp, listeningOnly) choice.
func socketSelection(hasTCP, hasUDP, hasAll, hasListen bool) (tcp, udp, listenOnly bool) {
	tcp = hasTCP || !hasUDP // TCP is the default
	udp = hasUDP
	if hasAll {
		tcp, udp = true, true
	}
	return tcp, udp, hasListen
}

// filteredRows applies the selection and ordering shared by netstat and ss.
// udpAlways shows UDP rows even without -a/-l (ss's default mode and
// netstat -u both do this).
func filteredRows(tcp, udp, listenOnly, withAll, udpAlways bool, family4, family6 bool) ([]sockRow, error) {
	var rows []sockRow
	if tcp {
		r, err := tcpRows()
		if err != nil {
			return nil, err
		}
		rows = append(rows, r...)
	}
	if udp {
		r, err := udpRows()
		if err != nil {
			return nil, err
		}
		rows = append(rows, r...)
	}
	out := rows[:0:0]
	for _, r := range rows {
		if !family4 && !strings.HasSuffix(r.proto, "6") {
			continue
		}
		if !family6 && strings.HasSuffix(r.proto, "6") {
			continue
		}
		switch {
		case r.state == 0: // UDP
			if !withAll && !listenOnly && !udpAlways {
				continue
			}
		case listenOnly:
			if r.state != tcpListen {
				continue
			}
		case !withAll:
			if r.state == tcpListen {
				continue
			}
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.proto != b.proto {
			return a.proto < b.proto
		}
		if a.local != b.local {
			return a.local < b.local
		}
		return a.state < b.state
	})
	return out, nil
}

// ------------------------------------------------------------------ netstat --

func cmdNetstat(args []string) int {
	f := NewFlags(
		&Flag{Name: "all", Short: 'a', Long: "all", Help: "display all sockets including listeners and UDP"},
		&Flag{Name: "tcp", Short: 't', Long: "tcp", Help: "display TCP sockets"},
		&Flag{Name: "udp", Short: 'u', Long: "udp", Help: "display UDP sockets"},
		&Flag{Name: "listening", Short: 'l', Long: "listening", Help: "display only listening sockets"},
		&Flag{Name: "numeric", Short: 'n', Long: "numeric", Help: "do not resolve port names"},
		&Flag{Name: "process", Short: 'o', Long: "process", Help: "display the owning process ID"},
		&Flag{Name: "proto", Short: 'p', Long: "proto", HasArg: true, ArgName: "PROTO", Help: "filter by protocol: tcp or udp"},
		&Flag{Name: "ethernet", Short: 'e', Long: "ethernet", Help: "display interface statistics"},
		&Flag{Name: "interfaces", Short: 'i', Long: "interfaces", Help: "display per-interface statistics"},
		&Flag{Name: "route", Short: 'r', Long: "route", Help: "display the routing table"},
		&Flag{Name: "statistics", Short: 's', Long: "statistics", Help: "display per-protocol statistics"},
	)
	if !parseOrDie("netstat", f, args) {
		return 2
	}
	if handleHelpVersion("netstat", "[OPTION]...", f) {
		return 0
	}
	tcpReq, udpReq := f.Has("tcp"), f.Has("udp")
	protoGiven := false
	if p := f.Str("proto"); p != "" {
		protoGiven = true
		switch strings.ToLower(p) {
		case "tcp":
			tcpReq, udpReq = true, false
		case "udp":
			tcpReq, udpReq = false, true
		default:
			warn("netstat", "unsupported protocol '%s' (use tcp or udp)", p)
			return 1
		}
	}
	switch {
	case f.Has("ethernet"):
		return netstatEthernet()
	case f.Has("interfaces"):
		return printInterfaces()
	case f.Has("route"):
		return printRoutesNetstat()
	case f.Has("statistics"):
		return printProtoStats()
	}
	var tcp, udp, listenOnly bool
	if protoGiven {
		// -p selects the protocol exclusively; -a does not widen it back
		tcp, udp, listenOnly = tcpReq, udpReq, f.Has("listening")
	} else {
		tcp, udp, listenOnly = socketSelection(tcpReq, udpReq, f.Has("all"), f.Has("listening"))
	}
	rows, err := filteredRows(tcp, udp, listenOnly, f.Has("all"), udpReq, true, true)
	if err != nil {
		warn("netstat", "%v", err)
		return 1
	}
	showPID := f.Has("process")
	fmt.Println("Active Connections")
	fmt.Println("")
	fmt.Printf("%-5s %-22s %-22s %-14s", "Proto", "Local Address", "Foreign Address", "State")
	if showPID {
		fmt.Print(" " + "PID")
	}
	fmt.Println()
	for _, r := range rows {
		local := formatAddr(r.local, f.Has("numeric"))
		foreign := r.remote
		if r.state == 0 {
			foreign = "*:*"
		}
		foreign = formatAddr(foreign, f.Has("numeric"))
		state := ""
		if r.state != 0 {
			state = tcpStateWindows(r.state)
		}
		fmt.Printf("%-5s %-22s %-22s %-14s", r.proto, local, foreign, state)
		if showPID {
			fmt.Printf(" %6d", r.pid)
		}
		fmt.Println()
	}
	return 0
}

func netstatEthernet() int {
	rows, err := ifRows()
	if err != nil {
		warn("netstat", "%v", err)
		return 1
	}
	var inOct, outOct, inU, inNU, inD, inE, outU, outNU, outD, outE uint64
	for _, r := range rows {
		inOct += uint64(r.inOctets)
		outOct += uint64(r.outOctets)
		inU += uint64(r.inUcast)
		inNU += uint64(r.inNUcast)
		inD += uint64(r.inDiscards)
		inE += uint64(r.inErrors)
		outU += uint64(r.outUcast)
		outNU += uint64(r.outNUcast)
		outD += uint64(r.outDiscards)
		outE += uint64(r.outErrors)
	}
	fmt.Println("Interface Statistics")
	fmt.Println("")
	fmt.Printf("%28s %12s %12s\n", "", "Received", "Sent")
	fmt.Printf("%28s %12d %12d\n", "Bytes", inOct, outOct)
	fmt.Printf("%28s %12d %12d\n", "Unicast packets", inU, outU)
	fmt.Printf("%28s %12d %12d\n", "Non-unicast packets", inNU, outNU)
	fmt.Printf("%28s %12d %12d\n", "Discards", inD, outD)
	fmt.Printf("%28s %12d %12d\n", "Errors", inE, outE)
	fmt.Printf("%28s %12d\n", "Unknown protocols", inU)
	return 0
}

func printInterfaces() int {
	rows, err := ifRows()
	if err != nil {
		warn("netstat", "%v", err)
		return 1
	}
	fmt.Println("Kernel Interface table")
	fmt.Printf("%-22s %7s %8s %7s %8s %7s\n", "Iface", "MTU", "RX-OK", "RX-ERR", "TX-OK", "TX-ERR")
	for _, r := range rows {
		fmt.Printf("%-22s %7d %8d %7d %8d %7d\n",
			shortIface(r.name), r.mtu,
			r.inUcast+r.inNUcast, r.inErrors,
			r.outUcast+r.outNUcast, r.outErrors)
	}
	return 0
}

// shortIface trims the long interface description to something readable.
func shortIface(name string) string {
	name = strings.TrimSpace(name)
	if len(name) > 21 {
		return name[:21]
	}
	return name
}

func printRoutesNetstat() int {
	rows, err := routeRows()
	if err != nil {
		warn("netstat", "%v", err)
		return 1
	}
	ifaces := ifNameByIndex()
	fmt.Println("IPv4 Route Table")
	fmt.Println("=======================================================================")
	fmt.Println("")
	fmt.Println("Active Routes:")
	fmt.Printf("%-24s %-15s %-15s %-15s %s\n", "Network Destination", "Netmask", "Gateway", "Interface", "Metric")
	for _, r := range rows {
		iface := ifaces[r.ifIndex]
		if iface == "" {
			iface = strconv.Itoa(r.ifIndex)
		}
		fmt.Printf("%-24s %-15s %-15s %-15s %d\n", r.dest, r.mask, r.gateway, iface, r.metric)
	}
	return 0
}

func printProtoStats() int {
	s := gatherProtoStats()
	if s.ip[0] == 0xFFFFFFFF {
		fmt.Println("netstat: IPv4 statistics are unavailable (elevation may be required)")
		return 1
	}
	fmt.Println("IPv4 Statistics")
	fmt.Printf("    Packets Received                  = %d\n", s.ip[2])
	fmt.Printf("    Received Header Errors            = %d\n", s.ip[3])
	fmt.Printf("    Received Address Errors           = %d\n", s.ip[4])
	fmt.Printf("    Datagrams Forwarded               = %d\n", s.ip[5])
	fmt.Printf("    Received Packets Discarded        = %d\n", s.ip[7])
	fmt.Printf("    Received Deliveries               = %d\n", s.ip[8])
	fmt.Printf("    Output Requests                   = %d\n", s.ip[9])
	fmt.Printf("    Outbound Discards                 = %d\n", s.ip[11])
	fmt.Printf("    Outbound No Routes                = %d\n", s.ip[12])
	fmt.Println("")
	fmt.Println("TCP Statistics")
	fmt.Printf("    Active Opens                       = %d\n", s.tcp[4])
	fmt.Printf("    Passive Opens                      = %d\n", s.tcp[5])
	fmt.Printf("    Failed Connection Attempts         = %d\n", s.tcp[6])
	fmt.Printf("    Reset Connections                  = %d\n", s.tcp[7])
	fmt.Printf("    Current Connections                = %d\n", s.tcp[8])
	fmt.Printf("    Segments Received                  = %d\n", s.tcp[9])
	fmt.Printf("    Segments Sent                      = %d\n", s.tcp[10])
	fmt.Printf("    Segments Retransmitted             = %d\n", s.tcp[11])
	fmt.Println("")
	fmt.Println("UDP Statistics")
	fmt.Printf("    Datagrams Received                 = %d\n", s.udp[0])
	fmt.Printf("    No Ports                           = %d\n", s.udp[1])
	fmt.Printf("    Receive Errors                     = %d\n", s.udp[2])
	fmt.Printf("    Datagrams Sent                     = %d\n", s.udp[3])
	fmt.Println("")
	fmt.Println("ICMP Statistics")
	fmt.Printf("    Messages Received                  = %d\n", s.icmp[0])
	fmt.Printf("    Errors Received                    = %d\n", s.icmp[1])
	fmt.Printf("    Messages Sent                      = %d\n", s.icmp[13])
	fmt.Printf("    Errors Sent                        = %d\n", s.icmp[14])
	return 0
}

// ------------------------------------------------------------------- ss ------

func cmdSs(args []string) int {
	f := NewFlags(
		&Flag{Name: "all", Short: 'a', Long: "all", Help: "display all sockets including listening"},
		&Flag{Name: "tcp", Short: 't', Long: "tcp", Help: "display TCP sockets"},
		&Flag{Name: "udp", Short: 'u', Long: "udp", Help: "display UDP sockets"},
		&Flag{Name: "listening", Short: 'l', Long: "listening", Help: "display only listening sockets"},
		&Flag{Name: "numeric", Short: 'n', Long: "numeric", Help: "do not resolve service names"},
		&Flag{Name: "process", Short: 'p', Long: "process", Help: "show the process using the socket"},
		&Flag{Name: "extended", Short: 'e', Long: "extended", Help: "extended info (shows the process)"},
		&Flag{Name: "options", Short: 'o', Long: "options", Help: "show timer info (no timer data on Windows)"},
		&Flag{Name: "memory", Short: 'm', Long: "memory", Help: "show socket memory (no data on Windows)"},
		&Flag{Name: "summary", Short: 's', Long: "summary", Help: "print a protocol summary"},
		&Flag{Name: "route", Short: 'r', Long: "route", Help: "display the routing table"},
		&Flag{Name: "interfaces", Short: 'i', Long: "interfaces", Help: "display per-interface statistics"},
		&Flag{Name: "ipv4", Short: '4', Long: "ipv4", Help: "display only IPv4 sockets"},
		&Flag{Name: "ipv6", Short: '6', Long: "ipv6", Help: "display only IPv6 sockets"},
	)
	if !parseOrDie("ss", f, args) {
		return 2
	}
	if handleHelpVersion("ss", "[OPTION]...", f) {
		return 0
	}
	switch {
	case f.Has("summary"):
		return ssSummary()
	case f.Has("route"):
		return ssRoutes()
	case f.Has("interfaces"):
		return printInterfaces()
	}
	tcp, udp, listenOnly := socketSelection(f.Has("tcp"), f.Has("udp"), f.Has("all"), f.Has("listening"))
	// ss's default shows UDP too
	udpAlways := !f.Has("tcp") && !f.Has("udp")
	if udpAlways {
		udp = true
	}
	fam4, fam6 := true, true
	if f.Has("ipv4") != f.Has("ipv6") {
		fam4, fam6 = f.Has("ipv4"), f.Has("ipv6")
	}
	rows, err := filteredRows(tcp, udp, listenOnly, f.Has("all"), udpAlways, fam4, fam6)
	if err != nil {
		warn("ss", "%v", err)
		return 1
	}
	showProc := f.Has("process") || f.Has("extended")
	names := map[int]string{}
	if showProc {
		if procs, err := snapshotProcesses(); err == nil {
			for _, p := range procs {
				if _, ok := names[p.pid]; !ok {
					names[p.pid] = p.name
				}
			}
		}
	}
	fmt.Printf("%-7s %-7s %-7s %-21s %-21s", "State", "Recv-Q", "Send-Q", "Local Address:Port", "Peer Address:Port")
	if showProc {
		fmt.Print("  Process")
	}
	fmt.Println()
	for _, r := range rows {
		state := "UNCONN"
		if r.state != 0 {
			state = tcpStateSS(r.state)
		}
		local := formatAddr(r.local, f.Has("numeric"))
		peer := r.remote
		if r.state == 0 {
			peer = "*:*"
		}
		peer = formatAddr(peer, f.Has("numeric"))
		fmt.Printf("%-7s %-7s %-7s %-21s %-21s", state, "0", "0", local, peer)
		if showProc {
			if name := names[r.pid]; name != "" {
				fmt.Printf("  users:((%q,pid=%d))", name, r.pid)
			} else {
				fmt.Printf("  pid=%d", r.pid)
			}
		}
		fmt.Println()
	}
	return 0
}

func ssSummary() int {
	tcp, err := tcpRows()
	if err != nil {
		warn("ss", "%v", err)
		return 1
	}
	udp, err := udpRows()
	if err != nil {
		warn("ss", "%v", err)
		return 1
	}
	estab, timewait, closed := 0, 0, 0
	for _, r := range tcp {
		switch r.state {
		case tcpEstablished:
			estab++
		case tcpTimeWait:
			timewait++
		case tcpClosed, tcpDeleteTcb:
			closed++
		}
	}
	total := len(tcp) + len(udp)
	fmt.Printf("Total: %d (kernel 0)\n", total)
	fmt.Printf("TCP:   %d (estab %d, closed %d, orphaned 0, timewait %d/0)\n", len(tcp), estab, closed, timewait)
	fmt.Printf("UDP:   %d (%d)\n", len(udp), len(udp))
	return 0
}

func ssRoutes() int {
	rows, err := routeRows()
	if err != nil {
		warn("ss", "%v", err)
		return 1
	}
	ifaces := ifNameByIndex()
	for _, r := range rows {
		dev := ifaces[r.ifIndex]
		if dev == "" {
			dev = strconv.Itoa(r.ifIndex)
		}
		if r.dest == "0.0.0.0" && r.mask == "0.0.0.0" {
			fmt.Printf("default via %s dev %s metric %d\n", r.gateway, dev, r.metric)
			continue
		}
		prefix := maskPrefix(r.mask)
		line := fmt.Sprintf("%s/%d dev %s", r.dest, prefix, dev)
		if r.gateway != "0.0.0.0" {
			line += " via " + r.gateway
		}
		line += " metric " + strconv.Itoa(r.metric)
		fmt.Println(line)
	}
	return 0
}

func maskPrefix(mask string) int {
	parts := strings.Split(mask, ".")
	if len(parts) != 4 {
		return 0
	}
	bits := 0
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		for n > 0 {
			bits += n & 1
			n >>= 1
		}
	}
	return bits
}
