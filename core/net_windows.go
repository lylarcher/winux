//go:build windows

package core

// The socket/route/interface data behind netstat and ss comes from the native
// IP Helper API (iphlpapi.dll): GetExtendedTcpTable, GetExtendedUdpTable,
// GetIpForwardTable, GetIfTable and the Get*Statistics family. No external
// tools and no shelling out are involved, so these commands are fast and
// read-only — they never touch the network configuration.

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var iphlpapi = syscall.NewLazyDLL("iphlpapi.dll")

var (
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
	procGetIpForwardTable   = iphlpapi.NewProc("GetIpForwardTable")
	procGetIfTable          = iphlpapi.NewProc("GetIfTable")
	procGetTcpStatistics    = iphlpapi.NewProc("GetTcpStatistics")
	procGetUdpStatistics    = iphlpapi.NewProc("GetUdpStatistics")
	procGetIpStatistics     = iphlpapi.NewProc("GetIpStatistics")
	procGetIcmpStatistics   = iphlpapi.NewProc("GetIcmpStatistics")
)

const (
	afInet  = 2
	afInet6 = 23

	tcpTableOwnerPidAll = 5
	udpTableOwnerPid    = 1

	errorInsufficientBuffer = 0x7A

	// MIB_TCP_STATE values
	tcpClosed, tcpListen, tcpSynSent, tcpSynReceived, tcpEstablished = 1, 2, 3, 4, 5
	tcpFinWait1, tcpFinWait2, tcpCloseWait, tcpClosing, tcpLastAck   = 6, 7, 8, 9, 10
	tcpTimeWait, tcpDeleteTcb                                       = 11, 12
)

// sockRow is one entry of a TCP or UDP socket table.
type sockRow struct {
	proto             string // tcp, tcp6, udp, udp6
	state             int    // MIB TCP state; 0 for UDP
	local, remote     string // formatted address (host:port)
	lport, rport      int
	pid               int
	stateName         string // human state, precomputed
}

// tcpRows returns every TCP socket (v4 and v6) with its owner PID.
func tcpRows() ([]sockRow, error) {
	rows, err := tcpRowsFamily(afInet)
	if err != nil {
		return nil, err
	}
	v6, err := tcpRowsFamily(afInet6)
	if err != nil {
		return nil, err
	}
	return append(rows, v6...), nil
}

func tcpRowsFamily(family uint32) ([]sockRow, error) {
	var size uint32
	r, _, _ := procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPidAll, 0)
	if r != 0 && r != errorInsufficientBuffer {
		return nil, fmt.Errorf("GetExtendedTcpTable failed (error 0x%x)", r)
	}
	buf := make([]byte, size)
	r, _, _ = procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPidAll, 0)
	if r != 0 {
		return nil, fmt.Errorf("GetExtendedTcpTable failed (error 0x%x)", r)
	}
	num := *(*uint32)(unsafe.Pointer(&buf[0]))
	rows := make([]sockRow, 0, num)
	if family == afInet {
		const rowSize = 24 // MIB_TCPROW_OWNER_PID
		for i := uint32(0); i < num; i++ {
			p := buf[4+int(i)*rowSize:]
			rows = append(rows, sockRow{
				proto: "tcp",
				state: int(*(*uint32)(unsafe.Pointer(&p[0]))),
				local: fmt.Sprintf("%s:%d", ipv4(p[4:8]), swap16(*(*uint32)(unsafe.Pointer(&p[8])))),
				remote: fmt.Sprintf("%s:%d", ipv4(p[12:16]), swap16(*(*uint32)(unsafe.Pointer(&p[16])))),
				pid:   int(*(*uint32)(unsafe.Pointer(&p[20]))),
			})
		}
	} else {
		const rowSize = 56 // MIB_TCP6ROW_OWNER_PID
		for i := uint32(0); i < num; i++ {
			p := buf[4+int(i)*rowSize:]
			rows = append(rows, sockRow{
				proto: "tcp6",
				state: int(*(*uint32)(unsafe.Pointer(&p[0]))),
				local: fmt.Sprintf("%s:%d", ipv6(p[4:20]), swap16(*(*uint32)(unsafe.Pointer(&p[24])))),
				remote: fmt.Sprintf("%s:%d", ipv6(p[28:44]), swap16(*(*uint32)(unsafe.Pointer(&p[48])))),
				pid:   int(*(*uint32)(unsafe.Pointer(&p[52]))),
			})
		}
	}
	return rows, nil
}

// udpRows returns every UDP socket (v4 and v6) with its owner PID.
func udpRows() ([]sockRow, error) {
	rows, err := udpRowsFamily(afInet)
	if err != nil {
		return nil, err
	}
	v6, err := udpRowsFamily(afInet6)
	if err != nil {
		return nil, err
	}
	return append(rows, v6...), nil
}

func udpRowsFamily(family uint32) ([]sockRow, error) {
	var size uint32
	r, _, _ := procGetExtendedUdpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), udpTableOwnerPid, 0)
	if r != 0 && r != errorInsufficientBuffer {
		return nil, fmt.Errorf("GetExtendedUdpTable failed (error 0x%x)", r)
	}
	buf := make([]byte, size)
	r, _, _ = procGetExtendedUdpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), udpTableOwnerPid, 0)
	if r != 0 {
		return nil, fmt.Errorf("GetExtendedUdpTable failed (error 0x%x)", r)
	}
	num := *(*uint32)(unsafe.Pointer(&buf[0]))
	rows := make([]sockRow, 0, num)
	if family == afInet {
		const rowSize = 12 // MIB_UDPROW_OWNER_PID
		for i := uint32(0); i < num; i++ {
			p := buf[4+int(i)*rowSize:]
			rows = append(rows, sockRow{
				proto: "udp",
				local: fmt.Sprintf("%s:%d", ipv4(p[0:4]), swap16(*(*uint32)(unsafe.Pointer(&p[4])))),
				remote: "*:*",
				pid:   int(*(*uint32)(unsafe.Pointer(&p[8]))),
			})
		}
	} else {
		const rowSize = 28 // MIB_UDP6ROW_OWNER_PID
		for i := uint32(0); i < num; i++ {
			p := buf[4+int(i)*rowSize:]
			rows = append(rows, sockRow{
				proto: "udp6",
				local: fmt.Sprintf("%s:%d", ipv6(p[0:16]), swap16(*(*uint32)(unsafe.Pointer(&p[20])))),
				remote: "*:*",
				pid:   int(*(*uint32)(unsafe.Pointer(&p[24]))),
			})
		}
	}
	return rows, nil
}

func ipv4(b []byte) string {
	return fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3])
}

func ipv6(b []byte) string {
	return net.IP(b).String()
}

// swap16 converts a port stored in network byte order to host order.
func swap16(v uint32) int {
	return int((v&0xFF)<<8 | (v>>8)&0xFF)
}

// routeRow is one IPv4 route.
type routeRow struct {
	dest, mask, gateway string
	ifIndex, metric     int
	typ, proto          int
}

// routeRows returns the IPv4 forwarding table.
func routeRows() ([]routeRow, error) {
	var size uint32
	r, _, _ := procGetIpForwardTable.Call(0, uintptr(unsafe.Pointer(&size)), 0)
	if r != 0 && r != errorInsufficientBuffer {
		return nil, fmt.Errorf("GetIpForwardTable failed (error 0x%x)", r)
	}
	buf := make([]byte, size)
	r, _, _ = procGetIpForwardTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	if r != 0 {
		return nil, fmt.Errorf("GetIpForwardTable failed (error 0x%x)", r)
	}
	num := *(*uint32)(unsafe.Pointer(&buf[0]))
	const rowSize = 56 // MIB_IPFORWARDROW
	rows := make([]routeRow, 0, num)
	for i := uint32(0); i < num; i++ {
		p := buf[4+int(i)*rowSize:]
		rows = append(rows, routeRow{
			dest:    ipv4(p[0:4]),
			mask:    ipv4(p[4:8]),
			gateway: ipv4(p[12:16]),
			ifIndex: int(*(*uint32)(unsafe.Pointer(&p[16]))),
			typ:     int(*(*uint32)(unsafe.Pointer(&p[20]))),
			proto:   int(*(*uint32)(unsafe.Pointer(&p[24]))),
			metric:  int(*(*uint32)(unsafe.Pointer(&p[36]))),
		})
	}
	return rows, nil
}

// ifRow is one interface's statistics from MIB_IFROW.
type ifRow struct {
	index, mtu, speed  uint32
	name               string // friendly description
	addr               string // MAC, hex
	adminUp, operUp    bool
	inOctets, outOctets        uint32
	inUcast, inNUcast          uint32
	inDiscards, inErrors       uint32
	inUnknown                  uint32
	outUcast, outNUcast        uint32
	outDiscards, outErrors     uint32
}

const mibIfRowSize = 860

// ifRows returns one row per interface, indexed by ifIndex.
func ifRows() ([]ifRow, error) {
	var size uint32
	r, _, _ := procGetIfTable.Call(0, uintptr(unsafe.Pointer(&size)), 0)
	if r != 0 && r != errorInsufficientBuffer {
		return nil, fmt.Errorf("GetIfTable failed (error 0x%x)", r)
	}
	buf := make([]byte, size)
	r, _, _ = procGetIfTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	if r != 0 {
		return nil, fmt.Errorf("GetIfTable failed (error 0x%x)", r)
	}
	num := *(*uint32)(unsafe.Pointer(&buf[0]))
	rows := make([]ifRow, 0, num)
	for i := uint32(0); i < num; i++ {
		p := buf[4+int(i)*mibIfRowSize:]
		row := ifRow{
			index:   *(*uint32)(unsafe.Pointer(&p[512])),
			mtu:     *(*uint32)(unsafe.Pointer(&p[520])),
			speed:   *(*uint32)(unsafe.Pointer(&p[524])),
			adminUp: *(*uint32)(unsafe.Pointer(&p[540])) == 1,
			operUp:  *(*uint32)(unsafe.Pointer(&p[544])) == 1,
			inOctets:    *(*uint32)(unsafe.Pointer(&p[552])), // dwLastChange at 548
			inUcast:     *(*uint32)(unsafe.Pointer(&p[556])),
			inNUcast:    *(*uint32)(unsafe.Pointer(&p[560])),
			inDiscards:  *(*uint32)(unsafe.Pointer(&p[564])),
			inErrors:    *(*uint32)(unsafe.Pointer(&p[568])),
			inUnknown:   *(*uint32)(unsafe.Pointer(&p[572])),
			outOctets:   *(*uint32)(unsafe.Pointer(&p[576])),
			outUcast:    *(*uint32)(unsafe.Pointer(&p[580])),
			outNUcast:   *(*uint32)(unsafe.Pointer(&p[584])),
			outDiscards: *(*uint32)(unsafe.Pointer(&p[588])),
			outErrors:   *(*uint32)(unsafe.Pointer(&p[592])),
		}
		// bDescr at offset 604, len 256
		desc := p[604:860]
		if n := bytes.IndexByte(desc, 0); n >= 0 {
			desc = desc[:n]
		}
		row.name = string(desc)
		if row.name == "" {
			row.name = syscall.UTF16ToString((*[256]uint16)(unsafe.Pointer(&p[0]))[:])
		}
		// bPhysAddr at 532, len 8, count at 528
		alen := int(*(*uint32)(unsafe.Pointer(&p[528])))
		if alen > 8 {
			alen = 8
		}
		mac := make([]string, 0, alen)
		for j := 0; j < alen; j++ {
			mac = append(mac, fmt.Sprintf("%02x", p[532+j]))
		}
		row.addr = strings.Join(mac, ":")
		rows = append(rows, row)
	}
	return rows, nil
}

// ifNameByIndex builds an ifIndex -> description map.
func ifNameByIndex() map[int]string {
	m := map[int]string{}
	if rows, err := ifRows(); err == nil {
		for _, r := range rows {
			m[int(r.index)] = r.name
		}
	}
	return m
}

// protoStats holds the aggregate counters used by `netstat -s`.
type protoStats struct {
	ip   [29]uint32 // MIB_IPSTATS
	tcp  [15]uint32 // MIB_TCPSTATS
	udp  [5]uint32  // MIB_UDPSTATS
	icmp [26]uint32 // MIB_ICMPSTATS (in + out)
}

func gatherProtoStats() protoStats {
	var s protoStats
	if r, _, _ := procGetIpStatistics.Call(uintptr(unsafe.Pointer(&s.ip[0]))); r != 0 {
		s.ip[0] = 0xFFFFFFFF
	}
	if r, _, _ := procGetTcpStatistics.Call(uintptr(unsafe.Pointer(&s.tcp[0]))); r != 0 {
		s.tcp[0] = 0xFFFFFFFF
	}
	if r, _, _ := procGetUdpStatistics.Call(uintptr(unsafe.Pointer(&s.udp[0]))); r != 0 {
		s.udp[0] = 0xFFFFFFFF
	}
	if r, _, _ := procGetIcmpStatistics.Call(uintptr(unsafe.Pointer(&s.icmp[0]))); r != 0 {
		s.icmp[0] = 0xFFFFFFFF
	}
	return s
}

var (
	servicesOnce sync.Once
	services     map[int]string // port -> service name (TCP)
)

// serviceName resolves a well-known port via %SystemRoot%\System32\drivers\
// etc\services, falling back to a small built-in table.
func serviceName(port int) string {
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
	if name, ok := services[port]; ok {
		return name
	}
	switch port {
	case 22:
		return "ssh"
	case 53:
		return "domain"
	case 80:
		return "http"
	case 443:
		return "https"
	case 445:
		return "microsoft-ds"
	case 3389:
		return "ms-wbt-server"
	}
	return ""
}
