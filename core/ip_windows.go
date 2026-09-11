//go:build windows

package core

// The ip family reads the adapter list through GetAdaptersAddresses and the
// neighbour (ARP) cache through GetIpNetTable — both native, read-only calls
// from iphlpapi. The layouts below are the documented 64-bit (LH) structures.

import (
	"fmt"
	"net"
	"strings"
	"syscall"
	"unsafe"
)

var (
	procGetAdaptersAddresses = iphlpapi.NewProc("GetAdaptersAddresses")
	procGetIpNetTable        = iphlpapi.NewProc("GetIpNetTable")
)

// The ifType/ifOperStatus constants and linkTypeName live in nettypes.go
// (shared with the untagged ip.go).

// ipAdapterAddresses mirrors IP_ADAPTER_ADDRESSES_LH for 64-bit Windows.
// Only the fields we read are laid out; the trailing fields (which we skip by
// walking Length) are not declared.
type ipAdapterAddresses struct {
	Length              uint32
	IfIndex             uint32
	Next                *ipAdapterAddresses
	AdapterName         *uint16
	FirstUnicastAddress *ipAdapterUnicastAddress
	FirstAnycastAddress uintptr
	FirstMulticastAddr  uintptr
	FirstDnsServerAddr  uintptr
	DnsSuffix           *uint16
	Description         *uint16
	FriendlyName        *uint16
	PhysicalAddress     [8]byte
	PhysicalAddressLen  uint32
	Flags               uint32
	Mtu                 uint32
	IfType              uint32
	OperStatus          uint32
	Ipv6IfIndex         uint32
	ZoneIndices         [16]uint32
	FirstPrefix         uintptr
	TransmitLinkSpeed   uint64
	ReceiveLinkSpeed    uint64
	FirstWinsServerAddr uintptr
	FirstGatewayAddress *ipAdapterGatewayAddress
	Ipv4Metric          uint32
	Ipv6Metric          uint32
	Luid                uint64
	Dhcpv4Server        uintptr
	CompartmentId       uint32
	NetworkGuid         [16]byte
	ConnectionType      uint32
	TunnelType          uint32
	Dhcpv6Server        uintptr
	Dhcpv6ClientDuid    [130]byte
	Dhcpv6ClientDuidLen uint32
	Dhcpv6Iaid          uint32
	// FirstDnsSuffix and beyond are unused; the struct length covers them.
}

// ipAdapterUnicastAddress mirrors IP_ADAPTER_UNICAST_ADDRESS_LH.
type ipAdapterUnicastAddress struct {
	Length             uint32
	Flags              uint32
	Next               *ipAdapterUnicastAddress
	Address            socketAddress
	PrefixOrigin       uint32
	SuffixOrigin       uint32
	DadState           uint32
	ValidLifetime      uint32
	PreferredLifetime  uint32
	LeaseLifetime      uint32
	OnLinkPrefixLength uint8
}

// ipAdapterGatewayAddress mirrors IP_ADAPTER_GATEWAY_ADDRESS_LH.
type ipAdapterGatewayAddress struct {
	Length  uint32
	Reserved uint32
	Next    *ipAdapterGatewayAddress
	Address socketAddress
}

// socketAddress mirrors SOCKET_ADDRESS.
type socketAddress struct {
	Sockaddr        *rawSockaddr
	SockaddrLength  int32
}

// rawSockaddr is the start of any sockaddr; the concrete layouts below follow.
type rawSockaddr struct {
	Family uint16
	Data   [14]byte
}

// sockaddrIn is SOCKADDR_IN (IPv4).
type sockaddrIn struct {
	Family uint16
	Port   uint16
	Addr   [4]byte
	Zero   [8]byte
}

// sockaddrIn6 is SOCKADDR_IN6 (IPv6).
type sockaddrIn6 struct {
	Family   uint16
	Port     uint16
	Flowinfo uint32
	Addr     [16]byte
	ScopeID  uint32
}

// adapterInfo, addrInfo and netNeighbor are defined in nettypes.go (shared,
// portable data used by the untagged ip.go too).

// adapters returns the full adapter list, read-only.
func adapters() ([]adapterInfo, error) {
	var size uint32
	procGetAdaptersAddresses.Call(0 /* AF_UNSPEC */, 0, 0, 0, uintptr(unsafe.Pointer(&size)))
	buf := make([]byte, size)
	r, _, _ := procGetAdaptersAddresses.Call(
		0, 0, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return nil, fmt.Errorf("GetAdaptersAddresses failed (error 0x%x)", r)
	}
	var out []adapterInfo
	for p := (*ipAdapterAddresses)(unsafe.Pointer(&buf[0])); p != nil; p = p.Next {
		a := adapterInfo{
			index:      int(p.IfIndex),
			name:       wstr(p.FriendlyName),
			desc:       wstr(p.Description),
			ifType:     p.IfType,
			operStatus: p.OperStatus,
			mtu:        p.Mtu,
			flags:      adapterFlags(p),
		}
		if p.PhysicalAddressLen > 0 && p.PhysicalAddressLen <= 8 {
			parts := make([]string, 0, p.PhysicalAddressLen)
			for i := uint32(0); i < p.PhysicalAddressLen; i++ {
				parts = append(parts, fmt.Sprintf("%02x", p.PhysicalAddress[i]))
			}
			a.mac = strings.Join(parts, ":")
		}
		for u := p.FirstUnicastAddress; u != nil; u = u.Next {
			info, ok := sockaddrInfo(u.Address)
			if !ok {
				continue
			}
			if info.prefix == 0 {
				info.prefix = int(u.OnLinkPrefixLength)
			}
			if strings.Contains(info.ip, ":") {
				a.ipv6 = append(a.ipv6, info)
			} else {
				a.ipv4 = append(a.ipv4, info)
			}
		}
		for g := p.FirstGatewayAddress; g != nil; g = g.Next {
			if info, ok := sockaddrInfo(g.Address); ok && info.ip != "0.0.0.0" && info.ip != "::" {
				a.gateways = append(a.gateways, info.ip)
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// sockaddrInfo extracts the address and prefix scope from a SOCKET_ADDRESS.
func sockaddrInfo(s socketAddress) (addrInfo, bool) {
	if s.Sockaddr == nil {
		return addrInfo{}, false
	}
	fam := *(*uint16)(unsafe.Pointer(s.Sockaddr))
	var info addrInfo
	switch fam {
	case 2: // AF_INET
		sa := (*sockaddrIn)(unsafe.Pointer(s.Sockaddr))
		info.ip = fmt.Sprintf("%d.%d.%d.%d", sa.Addr[0], sa.Addr[1], sa.Addr[2], sa.Addr[3])
	case 23: // AF_INET6
		sa := (*sockaddrIn6)(unsafe.Pointer(s.Sockaddr))
		info.ip = net.IP(sa.Addr[:]).String()
	default:
		return addrInfo{}, false
	}
	return info, true
}

func wstr(p *uint16) string {
	if p == nil {
		return ""
	}
	return syscall.UTF16ToString((*[1 << 20]uint16)(unsafe.Pointer(p))[:])
}

// adapterFlags builds the <> flag list shown by `ip addr`.
func adapterFlags(a *ipAdapterAddresses) string {
	var f []string
	if a.IfType == ifTypeLoopback {
		f = append(f, "LOOPBACK")
	}
	if a.PhysicalAddressLen > 0 {
		f = append(f, "BROADCAST")
	}
	f = append(f, "MULTICAST")
	if a.OperStatus == ifOperStatusUp {
		f = append(f, "UP", "LOWER_UP")
	}
	if len(f) == 0 {
		f = append(f, "NO-CARRIER")
	}
	return strings.Join(f, ",")
}

// netNeighbors returns the IPv4 neighbour (ARP) cache via GetIpNetTable.

func netNeighbors() ([]netNeighbor, error) {
	var size uint32
	procGetIpNetTable.Call(0, uintptr(unsafe.Pointer(&size)), 0)
	buf := make([]byte, size)
	r, _, _ := procGetIpNetTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	if r != 0 {
		return nil, fmt.Errorf("GetIpNetTable failed (error 0x%x)", r)
	}
	num := *(*uint32)(unsafe.Pointer(&buf[0]))
	const rowSize = 24 // MIB_IPNETROW
	var out []netNeighbor
	for i := uint32(0); i < num; i++ {
		p := buf[4+int(i)*rowSize:]
		row := netNeighbor{
			devIdx: int(*(*uint32)(unsafe.Pointer(&p[0]))),
			addr:   fmt.Sprintf("%d.%d.%d.%d", p[16], p[17], p[18], p[19]),
			perm:   *(*uint32)(unsafe.Pointer(&p[20])) == 4, // MIB_IPNET_TYPE_STATIC
		}
		alen := int(*(*uint32)(unsafe.Pointer(&p[4])))
		if alen > 8 {
			alen = 8
		}
		if alen > 0 {
			parts := make([]string, 0, alen)
			for j := 0; j < alen; j++ {
				parts = append(parts, fmt.Sprintf("%02x", p[8+j]))
			}
			row.lladdr = strings.Join(parts, ":")
		}
		out = append(out, row)
	}
	return out, nil
}
