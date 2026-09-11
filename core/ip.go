package core

// ipconfig wraps the real Windows ipconfig.exe; `ip`, `nmcli` and `nmtui`
// emulate the iproute2/NetworkManager command surfaces over the native adapter
// data. The query commands are strictly read-only: subcommands that would
// change the network state are refused with the native alternative. `nmtui`
// does not mutate anything either: it opens the Windows Network Connections
// panel (ncpa.cpl) where the user can operate on adapters by hand.

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

func init() {
	addCmd("ipconfig", "display IP configuration (wraps ipconfig.exe)", cmdIpconfig)
	addCmd("ip", "show/manipulate network interfaces and routing (read-only)", cmdIp)
	addCmd("nmcli", "query network devices and connections (NetworkManager style)", cmdNmcli)
	addCmd("nmtui", "open the Windows Network Connections panel (ncpa.cpl)", cmdNmtui)
}

// -------------------------------------------------------------- ipconfig -----

func cmdIpconfig(args []string) int {
	if len(args) == 0 {
		return runNative(systemExe("ipconfig.exe"))
	}
	// --help/--version are winux's own; everything else goes to ipconfig.exe
	for _, a := range args {
		if a == "--help" || a == "-h" || a == "/?" || a == "-?" {
			fmt.Println("Usage: ipconfig [OPTION]...")
			fmt.Println("Query or configure IP configuration (passes through to ipconfig.exe).")
			fmt.Println("")
			fmt.Println("  /all          display full configuration")
			fmt.Println("  /displaydns   display the DNS resolver cache")
			fmt.Println("  /showclassid  display the DHCP class id")
			fmt.Println("  /?            display this help")
			fmt.Println("")
			fmt.Println("Mutating options (/release, /renew, /flushdns, /registerdns) are refused:")
			fmt.Println("run ipconfig.exe directly when you intend to change the network state.")
			return 0
		}
		if a == "--version" || a == "-V" {
			fmt.Println("ipconfig " + Version)
			return 0
		}
	}
	for _, a := range args {
		la := strings.ToLower(a)
		if la == "/release" || la == "/renew" || la == "/flushdns" || la == "/registerdns" ||
			la == "/release6" || la == "/renew6" || la == "/setclassid" || la == "/setdns" {
			fmt.Fprintf(os.Stderr, "ipconfig: %s changes the network state and is refused; run ipconfig.exe directly if you need it\n", a)
			return 1
		}
	}
	return runNative(append([]string{systemExe("ipconfig.exe")}, args...)...)
}

// -------------------------------------------------------------------- ip -----

func cmdIp(args []string) int {
	// GNU ip treats "-br" as a single "brief" flag, not -b -r
	for i, a := range args {
		if a == "-br" {
			args[i] = "-b"
		}
	}
	f := NewFlags(
		&Flag{Name: "ipv4", Short: '4', Help: "IPv4 only"},
		&Flag{Name: "ipv6", Short: '6', Help: "IPv6 only"},
		&Flag{Name: "brief", Short: 'b', Long: "brief", Help: "brief output"},
		&Flag{Name: "oneline", Short: 'o', Long: "oneline", Help: "one line per record"},
		&Flag{Name: "stats", Short: 's', Long: "stats", Help: "show statistics (accepted)"},
		&Flag{Name: "json", Long: "json", Help: "accepted and ignored"},
	)
	if !parseOrDie("ip", f, args) {
		return 2
	}
	if handleHelpVersion("ip", "[ OPTIONS ] OBJECT { COMMAND | help }", f) {
		return 0
	}
	op := f.Positional
	if len(op) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ip [ OPTIONS ] OBJECT { COMMAND | help }")
		fmt.Fprintln(os.Stderr, "where  OBJECT := { addr | link | route | neigh }")
		fmt.Fprintln(os.Stderr, "winux keeps the network read-only: addr/link/route/neigh only support `show`.")
		return 1
	}
	obj := op[0]
	rest := op[1:]
	switch obj {
	case "addr", "address", "a":
		return ipAddr(rest, f)
	case "link", "l":
		return ipLink(rest, f)
	case "route", "r":
		return ipRoute(rest, f)
	case "neigh", "neighbour", "neighbor", "n":
		return ipNeigh(rest, f)
	default:
		fmt.Fprintf(os.Stderr, "Object \"%s\" is unknown, try \"ip help\".\n", obj)
		return 1
	}
}

// refuseMutation is the shared guard for network-changing subcommands.
func refuseMutation(cmd, native string) int {
	fmt.Fprintf(os.Stderr, "%s changes the network state and is refused (winux keeps networking read-only).\n", cmd)
	fmt.Fprintf(os.Stderr, "Use the native equivalent when you really need it: %s\n", native)
	return 1
}

func ipAddr(rest []string, f *Flags) int {
	if len(rest) > 0 {
		switch rest[0] {
		case "add", "del", "change", "replace", "flush", "save":
			return refuseMutation("ip addr "+rest[0], `netsh interface ipv4 add address "NAME" ADDR MASK`)
		case "help":
			fmt.Println("Usage: ip addr { show | flush } [dev NAME]")
			return 0
		}
	}
	dev, up := "", false
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "show", "list", "s":
		case "dev":
			if i+1 < len(rest) {
				dev = rest[i+1]
				i++
			}
		case "up":
			up = true
		default:
			dev = rest[i] // `ip addr show eth0` positional form
		}
	}
	ads, err := adapters()
	if err != nil {
		warn("ip", "%v", err)
		return 1
	}
	show := func(a adapterInfo, fam4, fam6 bool) {
		if dev != "" && !strings.EqualFold(a.name, dev) {
			return
		}
		if up && a.operStatus != ifOperStatusUp {
			return
		}
		if f.Has("brief") {
			addrs := make([]string, 0, 4)
			if fam4 {
				for _, ip := range a.ipv4 {
					addrs = append(addrs, fmt.Sprintf("%s/%d", ip.ip, ip.prefix))
				}
			}
			if fam6 {
				for _, ip := range a.ipv6 {
					addrs = append(addrs, fmt.Sprintf("%s/%d", ip.ip, ip.prefix))
				}
			}
			state := "DOWN"
			if a.operStatus == ifOperStatusUp {
				state = "UP"
			}
			if len(addrs) == 0 {
				addrs = append(addrs, "-")
			}
			fmt.Printf("%-16s %-9s %s\n", a.name, state, strings.Join(addrs, " "))
			return
		}
		fmt.Printf("%d: %s: <%s> mtu %d qdisc noqueue state %s\n", a.index, a.name, a.flags, a.mtu, operState(a.operStatus))
		typ := linkTypeName(a.ifType)
		fmt.Printf("    link/%s %s", typ, a.mac)
		if a.mac != "" {
			fmt.Print(" brd ff:ff:ff:ff:ff:ff")
		}
		fmt.Println()
		if fam4 {
			for _, ip := range a.ipv4 {
				fmt.Printf("    inet %s/%d scope %s %s\n", ip.ip, ip.prefix, ipScope(ip, a), a.name)
			}
		}
		if fam6 {
			for _, ip := range a.ipv6 {
				fmt.Printf("    inet6 %s/%d scope %s\n", ip.ip, ip.prefix, ip6Scope(ip))
			}
		}
	}
	fam4, fam6 := true, true
	if f.Has("ipv4") != f.Has("ipv6") {
		fam4, fam6 = f.Has("ipv4"), f.Has("ipv6")
	}
	for _, a := range ads {
		show(a, fam4, fam6)
	}
	return 0
}

func operState(s uint32) string {
	if s == ifOperStatusUp {
		return "UP"
	}
	return "DOWN"
}

func ipScope(ip addrInfo, a adapterInfo) string {
	if a.ifType == ifTypeLoopback {
		return "host"
	}
	if strings.HasPrefix(ip.ip, "127.") {
		return "host"
	}
	return "global"
}

func ip6Scope(ip addrInfo) string {
	if ip.ip == "::1" {
		return "host"
	}
	if strings.HasPrefix(ip.ip, "fe80:") || strings.HasPrefix(ip.ip, "fe8") {
		return "link"
	}
	return "global"
}

func ipLink(rest []string, f *Flags) int {
	if len(rest) > 0 {
		switch rest[0] {
		case "set", "add", "del", "change":
			return refuseMutation("ip link "+rest[0], `netsh interface set interface "NAME" admin=enable|disable`)
		case "help":
			fmt.Println("Usage: ip link { show | set } [dev NAME]")
			return 0
		}
	}
	dev := ""
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "show", "list", "s":
		case "dev":
			if i+1 < len(rest) {
				dev = rest[i+1]
				i++
			}
		default:
			dev = rest[i]
		}
	}
	ads, err := adapters()
	if err != nil {
		warn("ip", "%v", err)
		return 1
	}
	for _, a := range ads {
		if dev != "" && !strings.EqualFold(a.name, dev) {
			continue
		}
		if f.Has("brief") {
			state := "DOWN"
			if a.operStatus == ifOperStatusUp {
				state = "UP"
			}
			typ := linkTypeName(a.ifType)
			fmt.Printf("%d: %s %s %s <BROADCAST,MULTICAST,%s>\n", a.index, a.name, typ, state, "UP,LOWER_UP")
			continue
		}
		fmt.Printf("%d: %s: <%s> mtu %d qdisc noqueue state %s mode DEFAULT group default\n", a.index, a.name, a.flags, a.mtu, operState(a.operStatus))
		typ := linkTypeName(a.ifType)
		fmt.Printf("    link/%s %s", typ, a.mac)
		if a.mac != "" {
			fmt.Print(" brd ff:ff:ff:ff:ff:ff")
		}
		fmt.Println()
	}
	return 0
}

func ipRoute(rest []string, f *Flags) int {
	if len(rest) > 0 {
		switch rest[0] {
		case "add", "del", "change", "replace", "flush":
			return refuseMutation("ip route "+rest[0], `netsh interface ipv4 add route DEST/MASK "IFACE" GATEWAY`)
		case "help":
			fmt.Println("Usage: ip route show")
			return 0
		}
	}
	rows, err := routeRows()
	if err != nil {
		warn("ip", "%v", err)
		return 1
	}
	ifaces := ifNameByIndex()
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].dest < rows[j].dest })
	for _, r := range rows {
		iface := ifaces[r.ifIndex]
		if iface == "" {
			iface = strconv.Itoa(r.ifIndex)
		}
		if r.dest == "0.0.0.0" && r.mask == "0.0.0.0" {
			fmt.Printf("default via %s dev %s metric %d\n", r.gateway, iface, r.metric)
			continue
		}
		line := fmt.Sprintf("%s/%d dev %s proto %s scope link metric %d", r.dest, maskPrefix(r.mask), iface, routeProto(r.proto), r.metric)
		if r.gateway != "0.0.0.0" {
			line += " via " + r.gateway
		}
		fmt.Println(line)
	}
	return 0
}

func routeProto(p int) string {
	switch p {
	case 2:
		return "kernel"
	case 3:
		return "netmgmt"
	case 13:
		return "ospf"
	case 14:
		return "bgp"
	case 8:
		return "rip"
	}
	return "static"
}

func ipNeigh(rest []string, f *Flags) int {
	if len(rest) > 0 {
		switch rest[0] {
		case "add", "del", "change", "replace", "flush":
			return refuseMutation("ip neigh "+rest[0], `netsh interface ipv4 set neighbors "IFACE" ADDR MAC`)
		case "help":
			fmt.Println("Usage: ip neigh show")
			return 0
		}
	}
	rows, err := netNeighbors()
	if err != nil {
		warn("ip", "%v", err)
		return 1
	}
	ifaces := ifNameByIndex()
	for _, r := range rows {
		state := "REACHABLE"
		if r.perm {
			state = "PERMANENT"
		}
		dev := ifaces[r.devIdx]
		if dev == "" {
			dev = strconv.Itoa(r.devIdx)
		}
		fmt.Printf("%s dev %s lladdr %s %s\n", r.addr, dev, r.lladdr, state)
	}
	return 0
}

// ------------------------------------------------------------------ nmcli ----

func cmdNmcli(args []string) int {
	f := NewFlags(
		&Flag{Name: "terse", Short: 't', Long: "terse", Help: "terse output"},
		&Flag{Name: "fields", Short: 'f', Long: "fields", HasArg: true, ArgName: "FIELDS", Help: "accepted and ignored"},
	)
	if !parseOrDie("nmcli", f, args) {
		return 2
	}
	if handleHelpVersion("nmcli", "[ OPTIONS ] OBJECT { COMMAND | help }", f) {
		return 0
	}
	op := f.Positional
	if len(op) == 0 {
		return nmcliDeviceStatus(nil)
	}
	switch op[0] {
	case "device", "dev", "d":
		return nmcliDevice(op[1:], f)
	case "connection", "con", "c":
		return nmcliConnection(op[1:])
	case "general", "g":
		return nmcliGeneral(op[1:])
	case "networking", "radio":
		return refuseMutation("nmcli "+op[0], "netsh interface set interface / Windows Settings")
	case "help":
		fmt.Println("Usage: nmcli { device | connection | general }")
		return 0
	default:
		fmt.Fprintf(os.Stderr, "Unknown object \"%s\". Use `nmcli help`.\n", op[0])
		return 1
	}
}

func nmcliDevice(rest []string, f *Flags) int {
	if len(rest) == 0 || rest[0] == "status" {
		return nmcliDeviceStatus(nil)
	}
	if rest[0] == "show" {
		dev := ""
		if len(rest) > 1 && rest[1] == "dev" {
			dev = rest[2]
		} else if len(rest) > 1 {
			dev = rest[1]
		}
		return nmcliDeviceShow(dev)
	}
	switch rest[0] {
	case "connect", "disconnect", "reapply", "set", "wifi", "modify":
		return refuseMutation("nmcli device "+rest[0], "netsh interface set interface / Windows Settings")
	case "help":
		fmt.Println("Usage: nmcli device { status | show [DEVICE] }")
		return 0
	default:
		fmt.Fprintf(os.Stderr, "Unknown command \"%s\". Use `nmcli device help`.\n", rest[0])
		return 1
	}
}

func nmcliDeviceStatus(_ []string) int {
	ads, err := adapters()
	if err != nil {
		warn("nmcli", "%v", err)
		return 1
	}
	fmt.Printf("%-20s %-8s %-16s %s\n", "DEVICE", "TYPE", "STATE", "CONNECTION")
	for _, a := range ads {
		state := "disconnected"
		if a.operStatus == ifOperStatusUp {
			state = "connected"
		}
		fmt.Printf("%-20s %-8s %-16s %s\n", a.name, linkTypeName(a.ifType), state, a.name)
	}
	return 0
}

func nmcliDeviceShow(dev string) int {
	ads, err := adapters()
	if err != nil {
		warn("nmcli", "%v", err)
		return 1
	}
	for _, a := range ads {
		if dev != "" && !strings.EqualFold(a.name, dev) {
			continue
		}
		fmt.Printf("GENERAL.DEVICE:                         %s\n", a.name)
		fmt.Printf("GENERAL.TYPE:                           %s\n", linkTypeName(a.ifType))
		state := "disconnected"
		if a.operStatus == ifOperStatusUp {
			state = "connected"
		}
		fmt.Printf("GENERAL.STATE:                          %s\n", state)
		fmt.Printf("GENERAL.CONNECTION:                     %s\n", a.name)
		fmt.Printf("GENERAL.HWADDR:                         %s\n", a.mac)
		fmt.Printf("GENERAL.MTU:                            %d\n", a.mtu)
		for i, ip := range a.ipv4 {
			fmt.Printf("IP4.ADDRESS[%d]:                         %s/%d\n", i+1, ip.ip, ip.prefix)
		}
		for i, g := range a.gateways {
			fmt.Printf("IP4.GATEWAY[%d]:                         %s\n", i+1, g)
		}
		for i, ip := range a.ipv6 {
			fmt.Printf("IP6.ADDRESS[%d]:                         %s/%d\n", i+1, ip.ip, ip.prefix)
		}
	}
	return 0
}

func nmcliConnection(rest []string) int {
	if len(rest) == 0 || rest[0] == "show" {
		ads, err := adapters()
		if err != nil {
			warn("nmcli", "%v", err)
			return 1
		}
		fmt.Printf("%-20s %-8s %-8s %s\n", "NAME", "UUID", "TYPE", "DEVICE")
		for i, a := range ads {
			fmt.Printf("%-20s %-36s %-8s %s\n", a.name, fmt.Sprintf("%08d-0000-0000-0000-000000000000", i+1), linkTypeName(a.ifType), a.name)
		}
		fmt.Fprintln(os.Stderr, "nmcli: Windows has no NetworkManager profiles; the listing above is derived from the adapters.")
		return 0
	}
	switch rest[0] {
	case "up", "down", "add", "modify", "delete", "reload":
		return refuseMutation("nmcli connection "+rest[0], "netsh interface set interface / Windows Settings")
	case "help":
		fmt.Println("Usage: nmcli connection show")
		return 0
	default:
		fmt.Fprintf(os.Stderr, "Unknown command \"%s\". Use `nmcli connection help`.\n", rest[0])
		return 1
	}
}

func nmcliGeneral(rest []string) int {
	if len(rest) == 0 || rest[0] == "status" {
		fmt.Println("STATE      CONNECTIVITY  WIFI-HW  WIFI     WWAN-HW  WWAN")
		fmt.Println("connected  full          enabled  enabled  enabled  enabled")
		return 0
	}
	if rest[0] == "permissions" {
		fmt.Println("PERMISSION  VALUE")
		fmt.Println("org.freedesktop.NetworkManager.enable-disable-network  yes")
		fmt.Println("org.freedesktop.NetworkManager.enable-disable-wifi     yes")
		fmt.Println("org.freedesktop.NetworkManager.enable-disable-wwan     yes")
		fmt.Println("org.freedesktop.NetworkManager.enable-disable-statistics yes")
		return 0
	}
	if rest[0] == "hostname" {
		if h, err := os.Hostname(); err == nil {
			fmt.Println(h)
			return 0
		}
		return 1
	}
	return refuseMutation("nmcli general "+rest[0], "Windows Settings / netsh")
}

// ------------------------------------------------------------------ nmtui ----

// cmdNmtui maps the NetworkManager text UI to the real Windows surfaces, so
// `nmtui` in a shell behaves like the tool the user expects:
//
//	nmtui            → 控制面板\网络和 Internet\网络连接  (ncpa.cpl)
//	nmtui edit NAME  → same panel (connection management lives there)
//	nmtui connect    → same panel
//	nmtui hostname   → “重命名这台电脑” settings page (ms-settings:system-about)
//
// The GUI helper is started detached and nmtui returns immediately.
func cmdNmtui(args []string) int {
	f := NewFlags()
	if !parseOrDie("nmtui", f, args) {
		return 2
	}
	if handleHelpVersion("nmtui", "[edit|connect|hostname]", f) {
		return 0
	}
	sub := ""
	if len(f.Positional) > 0 {
		sub = f.Positional[0]
	}
	switch sub {
	case "", "edit", "connect":
		fmt.Println("nmtui: opening Network Connections (控制面板\\网络和 Internet\\网络连接, ncpa.cpl)")
		if err := launchDetached("control.exe", "ncpa.cpl"); err != nil {
			warn("nmtui", "cannot open the Network Connections panel: %v", err)
			fmt.Fprintln(os.Stderr, "nmtui: use `ipconfig`, `ip addr` or `nmcli device status` in a terminal instead")
			return 1
		}
		return 0
	case "hostname":
		fmt.Println("nmtui: opening the 'Rename this PC' settings page (ms-settings:system-about)")
		if err := launchDetached("explorer.exe", "ms-settings:system-about"); err != nil {
			warn("nmtui", "cannot open the rename page: %v", err)
			return 1
		}
		return 0
	default:
		warn("nmtui", "unknown command '%s'; supported subcommands: edit, connect, hostname", sub)
		return 1
	}
}

// launchDetached starts a GUI helper process without waiting for it, so the
// caller returns while the panel keeps running.
func launchDetached(name, arg string) error {
	cmd := exec.Command(name, arg)
	if err := cmd.Start(); err != nil {
		return err
	}
	cmd.Process.Release()
	return nil
}
