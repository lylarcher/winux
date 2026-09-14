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
	"strings"
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
)

// The MIB_TCP_STATE constants (tcpClosed .. tcpDeleteTcb) are in nettypes.go
// (shared with the untagged netstat.go); serviceName is there too.

// sockRow, routeRow and ifRow are defined in nettypes.go (shared, portable).

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
				proto:  "tcp",
				state:  int(*(*uint32)(unsafe.Pointer(&p[0]))),
				local:  fmt.Sprintf("%s:%d", ipv4(p[4:8]), swap16(*(*uint32)(unsafe.Pointer(&p[8])))),
				remote: fmt.Sprintf("%s:%d", ipv4(p[12:16]), swap16(*(*uint32)(unsafe.Pointer(&p[16])))),
				pid:    int(*(*uint32)(unsafe.Pointer(&p[20]))),
			})
		}
	} else {
		// MIB_TCP6ROW_OWNER_PID: ucLocalAddr[16], dwLocalScopeId,
		// dwLocalPort, ucRemoteAddr[16], dwRemoteScopeId, dwRemotePort,
		// dwState, dwOwningPid — the state and the ports sit *after* the
		// addresses, not before them.
		const rowSize = 56 // MIB_TCP6ROW_OWNER_PID
		for i := uint32(0); i < num; i++ {
			p := buf[4+int(i)*rowSize:]
			rows = append(rows, sockRow{
				proto:  "tcp6",
				state:  int(*(*uint32)(unsafe.Pointer(&p[48]))),
				local:  fmt.Sprintf("%s:%d", ipv6(p[0:16]), swap16(*(*uint32)(unsafe.Pointer(&p[20])))),
				remote: fmt.Sprintf("%s:%d", ipv6(p[24:40]), swap16(*(*uint32)(unsafe.Pointer(&p[44])))),
				pid:    int(*(*uint32)(unsafe.Pointer(&p[52]))),
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
				proto:  "udp",
				local:  fmt.Sprintf("%s:%d", ipv4(p[0:4]), swap16(*(*uint32)(unsafe.Pointer(&p[4])))),
				remote: "*:*",
				pid:    int(*(*uint32)(unsafe.Pointer(&p[8]))),
			})
		}
	} else {
		const rowSize = 28 // MIB_UDP6ROW_OWNER_PID
		for i := uint32(0); i < num; i++ {
			p := buf[4+int(i)*rowSize:]
			rows = append(rows, sockRow{
				proto:  "udp6",
				local:  fmt.Sprintf("%s:%d", ipv6(p[0:16]), swap16(*(*uint32)(unsafe.Pointer(&p[20])))),
				remote: "*:*",
				pid:    int(*(*uint32)(unsafe.Pointer(&p[24]))),
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

// mibIfRowSize is the size of a MIB_IFROW structure (used to walk GetIfTable's
// flat output buffer).
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
			index:       *(*uint32)(unsafe.Pointer(&p[512])),
			mtu:         *(*uint32)(unsafe.Pointer(&p[520])),
			speed:       *(*uint32)(unsafe.Pointer(&p[524])),
			adminUp:     *(*uint32)(unsafe.Pointer(&p[540])) == 1,
			operUp:      *(*uint32)(unsafe.Pointer(&p[544])) == 1,
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

// protoStats is defined in nettypes.go (shared with the net_other.go stub).

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

// serviceName lives in nettypes.go (shared; on non-Windows it simply skips the
// SystemRoot services file and uses the built-in fallback table).
