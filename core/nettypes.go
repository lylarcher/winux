package core

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Shared portable data types used by netstat/ss/ip/nmcli. They live in an
// untagged file (instead of inside net_windows.go / ip_windows.go) so that both
// the Windows implementations and the non-Windows stubs (net_other.go,
// ip_other.go) can reference them — this is what keeps the package
// cross-compilable for the Linux CI build.

// sockRow is one entry of a TCP or UDP socket table.
type sockRow struct {
	proto         string // tcp, tcp6, udp, udp6
	state         int    // MIB TCP state; 0 for UDP
	local, remote string // formatted address (host:port)
	lport, rport  int
	pid           int
	stateName     string // human state, precomputed
}

// routeRow is one IPv4 route.
type routeRow struct {
	dest, mask, gateway string
	ifIndex, metric     int
	typ, proto          int
}

// ifRow is one interface's statistics from MIB_IFROW.
type ifRow struct {
	index, mtu, speed       uint32
	name                    string // friendly description
	addr                    string // MAC, hex
	adminUp, operUp         bool
	inOctets, outOctets     uint32
	inUcast, inNUcast       uint32
	inDiscards, inErrors    uint32
	inUnknown               uint32
	outUcast, outNUcast     uint32
	outDiscards, outErrors  uint32
}

// adapterInfo is the cooked, portable form of one adapter.
type adapterInfo struct {
	index      int
	name       string // friendly name, e.g. "Ethernet"
	desc       string // driver description
	ifType     uint32
	operStatus uint32
	mtu        uint32
	mac        string
	flags      string
	ipv4       []addrInfo
	ipv6       []addrInfo
	gateways   []string
}

// addrInfo is one IP address with its prefix and scope.
type addrInfo struct {
	ip     string // "127.0.0.1" or "::1"
	prefix int
	scope  string
}

// netNeighbor is one IPv4 neighbour (ARP) cache entry.
type netNeighbor struct {
	devIdx       int
	lladdr, addr string
	perm         bool
}

// Interface / link constants shared with the untagged ip.go renderer.
const (
	ifOperStatusUp       = 1
	ifTypeLoopback       = 24
	ifTypeEthernetCSMACD = 6
	ifTypeIeee80211      = 71
	ifTypeTunnel         = 131
)

// linkTypeName maps an ifType to the iproute2 link type name.
func linkTypeName(ifType uint32) string {
	switch ifType {
	case ifTypeLoopback:
		return "loopback"
	case ifTypeIeee80211:
		return "wifi"
	case ifTypeTunnel:
		return "tunnel"
	default:
		return "ether"
	}
}

// protoStats holds the aggregate counters used by `netstat -s`.
type protoStats struct {
	ip   [29]uint32 // MIB_IPSTATS
	tcp  [15]uint32 // MIB_TCPSTATS
	udp  [5]uint32  // MIB_UDPSTATS
	icmp [26]uint32 // MIB_ICMPSTATS (in + out)
}

// MIB_TCP_STATE values (shared with the untagged netstat.go state rendering).
const (
	tcpClosed, tcpListen, tcpSynSent, tcpSynReceived, tcpEstablished = 1, 2, 3, 4, 5
	tcpFinWait1, tcpFinWait2, tcpCloseWait, tcpClosing, tcpLastAck   = 6, 7, 8, 9, 10
	tcpTimeWait, tcpDeleteTcb                                       = 11, 12
)

var (
	servicesOnce sync.Once
	services     map[int]string // port -> service name (TCP)
)

// knownPorts is the built-in fallback table used when the system services file
// has no entry; serviceName and servicePort share it.
var knownPorts = map[int]string{
	22:   "ssh",
	53:   "domain",
	80:   "http",
	443:  "https",
	445:  "microsoft-ds",
	3389: "ms-wbt-server",
}

// loadServices reads %SystemRoot%\System32\drivers\etc\services once. On
// non-Windows the lookup simply fails and only knownPorts is available.
func loadServices() {
	servicesOnce.Do(func() {
		services = map[int]string{}
		data, err := os.ReadFile(os.Getenv("SystemRoot") + `\System32\drivers\etc\services`)
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			sp := strings.SplitN(fields[1], "/", 2)
			if len(sp) != 2 || !strings.EqualFold(sp[1], "tcp") {
				continue
			}
			var n int
			if _, err := fmt.Sscanf(sp[0], "%d", &n); err == nil {
				if _, ok := services[n]; !ok {
					services[n] = fields[0]
				}
			}
		}
	})
}

// serviceName resolves a well-known port via the services file, falling back
// to the built-in table.
func serviceName(port int) string {
	loadServices()
	if name, ok := services[port]; ok {
		return name
	}
	return knownPorts[port]
}

// servicePort names the inverse of serviceName: it turns a service name (or a
// numeric string) into a TCP port, which is what `lsof -i:ssh` needs.
func servicePort(name string) (int, bool) {
	if n, err := strconv.Atoi(name); err == nil {
		if n > 0 && n < 65536 {
			return n, true
		}
		return 0, false
	}
	loadServices()
	for port, svc := range services {
		if strings.EqualFold(svc, name) {
			return port, true
		}
	}
	for port, svc := range knownPorts {
		if strings.EqualFold(svc, name) {
			return port, true
		}
	}
	return 0, false
}
