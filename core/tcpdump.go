package core

// tcpdump: capture and inspect network packets. Windows has no tcpdump, so the
// live-capture side maps to the built-in Packet Monitor (pktmon), which needs
// an elevated shell; the offline side (-r) reads classic pcap and pcapng files
// with a pure-Go parser and prints tcpdump-style summaries, so captured files
// from Wireshark / pktmon / Linux can be analysed without installing anything.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func init() {
	addCmd("tcpdump", "capture/inspect packets (maps to pktmon; -r reads pcap/pcapng)", cmdTcpdump)
}

// ------------------------------------------------------------- pcap files ----

type capturedPacket struct {
	ts   time.Time
	orig int // original wire length
	link int // link-layer type
	data []byte
}

func readPcapFile(path string) ([]capturedPacket, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var hdr [24]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, 0, fmt.Errorf("not a pcap file: %v", err)
	}
	magic := binary.LittleEndian.Uint32(hdr[0:4])
	swap := false
	switch magic {
	case 0xa1b2c3d4:
	case 0xd4c3b2a1:
		swap = true
	case 0xa1b23c4d: // nanosecond resolution
	case 0x4d3cb2a1:
		swap = true
	default:
		return nil, 0, fmt.Errorf("not a pcap file (bad magic 0x%08x)", magic)
	}
	u16 := func(b []byte) uint16 {
		if swap {
			return binary.BigEndian.Uint16(b)
		}
		return binary.LittleEndian.Uint16(b)
	}
	u32 := func(b []byte) uint32 {
		if swap {
			return binary.BigEndian.Uint32(b)
		}
		return binary.LittleEndian.Uint32(b)
	}
	link := int(u16(hdr[20:22]))
	nano := magic == 0xa1b23c4d || magic == 0x4d3cb2a1
	var pkts []capturedPacket
	for {
		var ph [16]byte
		if _, err := io.ReadFull(r, ph[:]); err != nil {
			if err == io.EOF {
				break
			}
			return nil, 0, err
		}
		sec := u32(ph[0:4])
		frac := u32(ph[4:8])
		incl := u32(ph[8:12])
		orig := u32(ph[12:16])
		if incl > 64<<20 {
			return nil, 0, fmt.Errorf("implausible pcap packet length %d", incl)
		}
		data := make([]byte, incl)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, 0, err
		}
		var ts time.Time
		if nano {
			ts = time.Unix(int64(sec), int64(frac))
		} else {
			ts = time.Unix(int64(sec), int64(frac)*1000)
		}
		pkts = append(pkts, capturedPacket{ts: ts, orig: int(orig), link: link, data: data})
	}
	return pkts, link, nil
}

// readPcapngFile handles the block types pktmon and Wireshark emit: SHB, IDB,
// EPB, SPB and legacy PB.
func readPcapngFile(path string) ([]capturedPacket, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var pkts []capturedPacket
	link := -1
	tsResol := int64(1000000) // default 10^-6 s
	for {
		var bh [8]byte
		if _, err := io.ReadFull(r, bh[:]); err != nil {
			if err == io.EOF {
				break
			}
			return nil, 0, err
		}
		btype := binary.LittleEndian.Uint32(bh[0:4])
		blen := binary.LittleEndian.Uint32(bh[4:8])
		if blen < 12 || blen > 64<<20 {
			return nil, 0, fmt.Errorf("bad pcapng block length %d", blen)
		}
		body := make([]byte, blen-12)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, 0, err
		}
		var tr [4]byte
		if _, err := io.ReadFull(r, tr[:]); err != nil {
			return nil, 0, err
		}
		_ = tr
		switch btype {
		case 0x0a0d0d0a: // SHB
		case 0x00000001: // IDB
			if len(body) >= 8 {
				link = int(binary.LittleEndian.Uint16(body[0:2]))
			}
			// options: if_tsresol (type 9), 1 byte: 10^-n seconds or 2^-n
			for off := 8; off+4 <= len(body); {
				ot := binary.LittleEndian.Uint16(body[off:])
				ol := binary.LittleEndian.Uint16(body[off+2:])
				off += 4
				if ot == 0 && ol == 0 {
					break
				}
				if ot == 9 && ol >= 1 && off+1 <= len(body) {
					v := body[off]
					if v&0x80 == 0 {
						tsResol = pow10(int64(v & 0x7f))
					} else {
						tsResol = 1 << (v & 0x7f)
					}
				}
				off += int(ol) + (int(ol) & 1)
			}
		case 0x00000006: // EPB
			if len(body) < 20 {
				continue
			}
			tsHigh := binary.LittleEndian.Uint32(body[4:8])
			tsLow := binary.LittleEndian.Uint32(body[8:12])
			caplen := binary.LittleEndian.Uint32(body[12:16])
			orig := binary.LittleEndian.Uint32(body[16:20])
			if caplen > uint32(len(body)-20) {
				continue
			}
			if link < 0 {
				link = 1 // assume Ethernet when no IDB was seen
			}
			ts := time.Unix(0, int64(tsHigh)<<32|int64(tsLow)*tsResol)
			pkts = append(pkts, capturedPacket{ts: ts, orig: int(orig), link: link, data: append([]byte(nil), body[20:20+caplen]...)})
		case 0x00000003, 0x0000000b: // SPB / legacy PB
			if len(body) < 8 {
				continue
			}
			orig := binary.LittleEndian.Uint32(body[0:4])
			if link < 0 {
				link = 1
			}
			pkts = append(pkts, capturedPacket{ts: time.Now(), orig: int(orig), link: link, data: append([]byte(nil), body[4:]...)})
		}
	}
	return pkts, link, nil
}

func writePcapFile(path string, link int, pkts []capturedPacket) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var hdr [24]byte
	binary.LittleEndian.PutUint32(hdr[0:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(hdr[4:6], 2)
	binary.LittleEndian.PutUint16(hdr[6:8], 4)
	binary.LittleEndian.PutUint32(hdr[16:20], 65535)
	binary.LittleEndian.PutUint32(hdr[20:24], uint32(link))
	if _, err := f.Write(hdr[:]); err != nil {
		return err
	}
	for _, p := range pkts {
		var ph [16]byte
		sec := p.ts.Unix()
		nsec := p.ts.Nanosecond()
		binary.LittleEndian.PutUint32(ph[0:4], uint32(sec))
		binary.LittleEndian.PutUint32(ph[4:8], uint32(nsec/1000))
		binary.LittleEndian.PutUint32(ph[8:12], uint32(len(p.data)))
		binary.LittleEndian.PutUint32(ph[12:16], uint32(p.orig))
		if _, err := f.Write(ph[:]); err != nil {
			return err
		}
		if _, err := f.Write(p.data); err != nil {
			return err
		}
	}
	return nil
}

func pow10(n int64) int64 {
	r := int64(1)
	for i := int64(0); i < n; i++ {
		r *= 10
	}
	return r
}

// ------------------------------------------------------------- decoding -----

type packetInfo struct {
	ts       time.Time
	wireLen  int
	link     int
	ethDst   string
	ethSrc   string
	ethType  int
	isARP    bool
	arpOp    string
	ipVer    int
	srcIP    string
	dstIP    string
	proto    string
	protoNum int
	srcPort  int
	dstPort  int
	ttl      int
	tos      int
	id       int
	ipLen    int
	fragOff  int
	fragDF   bool
	fragMF   bool
	flags    int // TCP flags
	seq      int
	ack      int
	win      int
	icmpType int
	icmpCode int
	payload  []byte
}

func u16be(b []byte, off int) int {
	if off+2 > len(b) {
		return -1
	}
	return int(b[off])<<8 | int(b[off+1])
}

func u32be(b []byte, off int) int64 {
	if off+4 > len(b) {
		return -1
	}
	return int64(b[off])<<24 | int64(b[off+1])<<16 | int64(b[off+2])<<8 | int64(b[off+3])
}

func macString(b []byte, off int) string {
	if off+6 > len(b) {
		return "?"
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[off], b[off+1], b[off+2], b[off+3], b[off+4], b[off+5])
}

func ipv4String(b []byte, off int) string {
	if off+4 > len(b) {
		return "?"
	}
	return net.IPv4(b[off], b[off+1], b[off+2], b[off+3]).String()
}

func ipv6String(b []byte, off int) string {
	if off+16 > len(b) {
		return "?"
	}
	return net.IP(append([]byte(nil), b[off:off+16]...)).String()
}

func decodePacket(pk capturedPacket) *packetInfo {
	p := &packetInfo{ts: pk.ts, wireLen: pk.orig, link: pk.link}
	b := pk.data
	off := 0
	// link layer
	switch pk.link {
	case 1: // Ethernet
		if len(b) < 14 {
			return p
		}
		p.ethDst = macString(b, 0)
		p.ethSrc = macString(b, 6)
		et := u16be(b, 12)
		off = 14
		for et == 0x8100 && off+4 <= len(b) { // 802.1Q VLAN
			et = u16be(b, off+2)
			off += 4
		}
		p.ethType = et
	case 0: // BSD loopback: 4-byte address family
		if len(b) < 4 {
			return p
		}
		fam := int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24
		off = 4
		switch fam {
		case 2:
			p.ethType = 0x0800
		case 10, 24, 28, 30:
			p.ethType = 0x86dd
		default:
			p.ethType = 0x0800
		}
	case 101: // RAW IP
		p.ethType = -1
	default:
		p.payload = b
		return p
	}
	if off+1 > len(b) {
		return p
	}
	// network layer
	ver := b[off] >> 4
	switch {
	case ver == 4 && (p.ethType == 0x0800 || p.ethType == -1 || p.link == 0):
		hdrLen := int(b[off]&0x0f) * 4
		if hdrLen < 20 || off+hdrLen > len(b) {
			return p
		}
		p.ipVer = 4
		p.tos = int(b[off+1])
		p.ipLen = u16be(b, off+2)
		p.id = u16be(b, off+4)
		frag := u16be(b, off+6)
		p.fragOff = frag & 0x1fff
		p.fragDF = frag&0x4000 != 0
		p.fragMF = frag&0x2000 != 0
		p.ttl = int(b[off+8])
		p.protoNum = int(b[off+9])
		p.srcIP = ipv4String(b, off+12)
		p.dstIP = ipv4String(b, off+16)
		off += hdrLen
	case ver == 6 && (p.ethType == 0x86dd || p.ethType == -1):
		if off+40 > len(b) {
			return p
		}
		p.ipVer = 6
		p.ipLen = u16be(b, off+4)
		p.ttl = int(b[off+7])
		p.protoNum = int(b[off+6])
		p.srcIP = ipv6String(b, off+8)
		p.dstIP = ipv6String(b, off+24)
		off += 40
		// extension headers
		for p.protoNum == 0 || p.protoNum == 43 || p.protoNum == 44 || p.protoNum == 60 || p.protoNum == 51 {
			if p.protoNum == 44 && off+8 <= len(b) { // fragment
				off += 8
				p.protoNum = int(b[off-1])
				continue
			}
			if off+2 > len(b) {
				return p
			}
			next := int(b[off])
			hlen := (int(b[off+1]) + 1) * 8
			off += hlen
			if off > len(b) {
				return p
			}
			p.protoNum = next
		}
	case p.ethType == 0x0806:
		p.isARP = true
		if len(b) < off+8 {
			return p
		}
		if u16be(b, off+6) == 1 {
			p.arpOp = "Request"
		} else if u16be(b, off+6) == 2 {
			p.arpOp = "Reply"
		} else {
			p.arpOp = "op " + strconv.Itoa(u16be(b, off+6))
		}
		if len(b) >= off+20 {
			p.srcIP = ipv4String(b, off+14)
			p.dstIP = ipv4String(b, off+24)
		}
		p.payload = b[off:]
		return p
	default:
		p.payload = b[off:]
		return p
	}
	// transport
	switch p.protoNum {
	case 6: // TCP
		if off+20 > len(b) {
			return p
		}
		p.proto = "tcp"
		p.srcPort = u16be(b, off)
		p.dstPort = u16be(b, off+2)
		p.seq = int(u32be(b, off+4))
		p.ack = int(u32be(b, off+8))
		dataOff := int(b[off+12]>>4) * 4
		p.flags = u16be(b, off+12) & 0x01ff
		p.win = u16be(b, off+14)
		if dataOff < 20 {
			dataOff = 20
		}
		if off+dataOff <= len(b) {
			p.payload = b[off+dataOff:]
		}
	case 17: // UDP
		if off+8 > len(b) {
			return p
		}
		p.proto = "udp"
		p.srcPort = u16be(b, off)
		p.dstPort = u16be(b, off+2)
		if off+8 <= len(b) {
			p.payload = b[off+8:]
		}
	case 1: // ICMP
		if off+4 > len(b) {
			return p
		}
		p.proto = "icmp"
		p.icmpType = int(b[off])
		p.icmpCode = int(b[off+1])
		if off+8 <= len(b) {
			p.id = u16be(b, off+4)
			p.seq = u16be(b, off+6)
			p.payload = b[off+8:]
		} else {
			p.payload = b[off+4:]
		}
	case 58: // ICMPv6
		if off+4 > len(b) {
			return p
		}
		p.proto = "icmp6"
		p.icmpType = int(b[off])
		p.icmpCode = int(b[off+1])
		if off+8 <= len(b) {
			p.id = u16be(b, off+4)
			p.seq = u16be(b, off+6)
			p.payload = b[off+8:]
		} else {
			p.payload = b[off+4:]
		}
	default:
		p.proto = "proto-" + strconv.Itoa(p.protoNum)
		p.payload = b[off:]
	}
	return p
}

// ------------------------------------------------------------ printing -----

type tcpdumpOpts struct {
	noNames   bool // -n / -nn: never resolve (always true; we do not resolve)
	verbose   int  // -v / -vv
	showLink  bool // -e
	hexAscii  bool // -X
	asciiOnly bool // -A
	snaplen   int  // -s
	filter    *pktFilterExpr
	out       io.Writer
}

func tcpFlags(f int) string {
	var b strings.Builder
	for _, c := range []struct {
		bit  int
		char byte
	}{{0x20, 'U'}, {0x08, 'P'}, {0x04, 'R'}, {0x02, 'S'}, {0x01, 'F'}} {
		if f&c.bit != 0 {
			b.WriteByte(c.char)
		}
	}
	if f&0x10 != 0 { // ACK
		b.WriteByte('.')
	}
	if b.Len() == 0 {
		return "none"
	}
	return b.String()
}

func icmpName(t, code int) string {
	if t == 3 {
		names := map[int]string{0: "net unreachable", 1: "host unreachable", 2: "protocol unreachable", 3: "port unreachable", 4: "fragmentation needed"}
		if s, ok := names[code]; ok {
			return "destination unreachable (" + s + ")"
		}
		return fmt.Sprintf("destination unreachable (code %d)", code)
	}
	names := map[int]string{
		0: "echo reply", 4: "source quench", 5: "redirect", 8: "echo request",
		11: "time exceeded", 12: "parameter problem", 13: "timestamp", 14: "timestamp reply",
		17: "address mask request", 18: "address mask reply",
	}
	if s, ok := names[t]; ok {
		return s
	}
	return fmt.Sprintf("icmp type %d", t)
}

func icmp6Name(t int) string {
	names := map[int]string{
		1: "destination unreachable", 2: "packet too big", 3: "time exceeded",
		4: "parameter problem", 128: "echo request", 129: "echo reply",
		133: "router solicitation", 134: "router advertisement",
		135: "neighbor solicitation", 136: "neighbor advertisement",
	}
	if s, ok := names[t]; ok {
		return s
	}
	return fmt.Sprintf("icmp6 type %d", t)
}

func portSuffix(p *packetInfo) string {
	if p.srcPort > 0 {
		return fmt.Sprintf(".%d", p.srcPort)
	}
	return ""
}

func printPacket(p *packetInfo, o tcpdumpOpts) {
	w := o.out
	ts := p.ts.Format("15:04:05.000000")
	body := ""
	switch {
	case p.isARP:
		if p.arpOp == "Request" {
			body = fmt.Sprintf("ARP, Request who-has %s tell %s, length %d", p.dstIP, p.ethSrc, p.wireLen)
		} else if p.arpOp == "Reply" {
			body = fmt.Sprintf("ARP, Reply %s is-at %s, length %d", p.srcIP, p.ethSrc, p.wireLen)
		} else {
			body = fmt.Sprintf("ARP, %s, length %d", p.arpOp, p.wireLen)
		}
	case p.proto == "tcp":
		l := fmt.Sprintf("Flags [%s], seq %d", tcpFlags(p.flags), p.seq)
		if p.flags&0x10 != 0 {
			l += fmt.Sprintf(", ack %d", p.ack)
		}
		l += fmt.Sprintf(", win %d, length %d", p.win, len(p.payload))
		body = fmt.Sprintf("%s.%d > %s.%d: %s", p.srcIP, p.srcPort, p.dstIP, p.dstPort, l)
	case p.proto == "udp":
		body = fmt.Sprintf("%s.%d > %s.%d: UDP, length %d", p.srcIP, p.srcPort, p.dstIP, p.dstPort, len(p.payload))
	case p.proto == "icmp":
		body = fmt.Sprintf("%s > %s: ICMP %s", p.srcIP, p.dstIP, icmpName(p.icmpType, p.icmpCode))
		if p.icmpType == 0 || p.icmpType == 8 || p.icmpType == 13 || p.icmpType == 14 {
			body += fmt.Sprintf(", id %d, seq %d", p.id, p.seq)
		}
		body += fmt.Sprintf(", length %d", len(p.payload))
	case p.proto == "icmp6":
		body = fmt.Sprintf("%s > %s: ICMP6, %s", p.srcIP, p.dstIP, icmp6Name(p.icmpType))
		if p.icmpType == 128 || p.icmpType == 129 {
			body += fmt.Sprintf(", id %d, seq %d", p.id, p.seq)
		}
		body += fmt.Sprintf(", length %d", len(p.payload))
	case p.ipVer == 4 || p.ipVer == 6:
		if p.protoNum == 6 || p.protoNum == 17 || p.protoNum == 1 || p.protoNum == 58 {
			body = fmt.Sprintf("%s%s > %s: %s, length %d", p.srcIP, portSuffix(p), p.dstIP, p.proto, len(p.payload))
		} else {
			body = fmt.Sprintf("%s > %s: %s, length %d", p.srcIP, p.dstIP, p.proto, len(p.payload))
		}
	case p.ethType == 0x0800 || p.ethType == 0x86dd:
		body = fmt.Sprintf("%s > %s: truncated, length %d", p.srcIP, p.dstIP, p.wireLen)
	default:
		body = fmt.Sprintf("unknown link type %d, %d bytes", p.link, len(p.payload))
	}
	rest := body
	if o.verbose > 0 && (p.ipVer == 4 || p.ipVer == 6) {
		protoName := map[int]string{6: "TCP", 17: "UDP", 1: "ICMP", 58: "ICMPv6"}[p.protoNum]
		if protoName == "" {
			protoName = strconv.Itoa(p.protoNum)
		}
		fl := "none"
		if p.fragDF {
			fl = "DF"
		}
		if p.fragMF {
			if fl != "none" {
				fl += "+"
			}
			fl += "MF"
		}
		rest = fmt.Sprintf("%s (tos 0x%x, ttl %d, id %d, offset %d, flags [%s], proto %s (%d), length %d) %s",
			pktIPWord(p), p.tos, p.ttl, p.id, p.fragOff, fl, protoName, p.protoNum, p.ipLen, body)
	} else if p.ipVer == 4 || p.ipVer == 6 {
		rest = pktIPWord(p) + " " + body
	}
	line := ts + " " + rest
	if o.showLink && p.ethSrc != "" {
		line = ts + " " + p.ethDst + " > " + p.ethSrc + ", " + rest
	}
	fmt.Fprintln(w, line)
	if o.asciiOnly || o.hexAscii {
		dumpPayload(p, o)
	}
}

func pktIPWord(p *packetInfo) string {
	if p.ipVer == 6 {
		return "IP6"
	}
	return "IP"
}

func dumpPayload(p *packetInfo, o tcpdumpOpts) {
	pl := p.payload
	if o.snaplen > 0 && len(pl) > o.snaplen {
		pl = pl[:o.snaplen]
	}
	if len(pl) == 0 {
		return
	}
	if o.asciiOnly && !o.hexAscii {
		var b strings.Builder
		for _, c := range pl {
			if c >= 0x20 && c < 0x7f {
				b.WriteByte(c)
			} else {
				b.WriteByte('.')
			}
		}
		fmt.Fprintf(o.out, "\t%s\n", b.String())
		return
	}
	for i := 0; i < len(pl); i += 16 {
		end := i + 16
		if end > len(pl) {
			end = len(pl)
		}
		hex := ""
		asc := ""
		for j := i; j < end; j++ {
			hex += fmt.Sprintf("%02x ", pl[j])
			if pl[j] >= 0x20 && pl[j] < 0x7f {
				asc += string(pl[j])
			} else {
				asc += "."
			}
		}
		fmt.Fprintf(o.out, "\t0x%04x:  %-48s %s\n", i, hex, asc)
	}
}

// ------------------------------------------------------------- filters ------

type pktTerm struct {
	kind string // proto | port | host
	val  string
}

type pktGroup struct{ terms []pktTerm }

type pktFilterExpr struct{ groups []pktGroup }

func parsePktFilter(s string) (*pktFilterExpr, error) {
	e := &pktFilterExpr{}
	toks := strings.Fields(strings.ToLower(s))
	if len(toks) == 0 {
		return e, nil
	}
	for _, t := range toks {
		if t == "not" {
			return nil, fmt.Errorf("'not' is not supported by the pktmon-backed tcpdump (no exclusion filters)")
		}
		if t == "portrange" || t == "greater" || t == "less" {
			return nil, fmt.Errorf("filter '%s' is not supported by the pktmon-backed tcpdump (try 'port'/'host'/'tcp'/'udp'/'icmp')", t)
		}
	}
	var cur []pktTerm
	i := 0
	for i < len(toks) {
		t := toks[i]
		if t == "and" {
			i++
			continue
		}
		if t == "or" {
			if len(cur) > 0 {
				e.groups = append(e.groups, pktGroup{terms: cur})
				cur = nil
			}
			i++
			continue
		}
		dir := ""
		if t == "src" || t == "dst" {
			dir = t
			i++
			if i >= len(toks) {
				return nil, fmt.Errorf("filter '%s' needs an operand", t)
			}
			t = toks[i]
		}
		term, err := parsePktTerm(t, dir, toks, &i)
		if err != nil {
			return nil, err
		}
		cur = append(cur, term)
		i++
	}
	if len(cur) > 0 {
		e.groups = append(e.groups, pktGroup{terms: cur})
	}
	return e, nil
}

func parsePktTerm(t, dir string, toks []string, i *int) (pktTerm, error) {
	_ = dir
	next := func() (string, error) {
		*i++
		if *i >= len(toks) {
			return "", fmt.Errorf("filter '%s' needs a value", t)
		}
		return toks[*i], nil
	}
	switch t {
	case "tcp", "udp", "icmp", "icmp6", "ip", "ip6", "arp":
		return pktTerm{kind: "proto", val: t}, nil
	case "port":
		v, err := next()
		if err != nil {
			return pktTerm{}, err
		}
		if _, err := strconv.Atoi(v); err != nil {
			return pktTerm{}, fmt.Errorf("invalid port '%s'", v)
		}
		return pktTerm{kind: "port", val: v}, nil
	case "host":
		v, err := next()
		if err != nil {
			return pktTerm{}, err
		}
		if net.ParseIP(v) == nil {
			return pktTerm{}, fmt.Errorf("invalid host '%s' (IP addresses only)", v)
		}
		return pktTerm{kind: "host", val: v}, nil
	default:
		return pktTerm{}, fmt.Errorf("unsupported filter primitive '%s'", t)
	}
}

func (e *pktFilterExpr) matches(p *packetInfo) bool {
	if e == nil || len(e.groups) == 0 {
		return true
	}
	for _, g := range e.groups {
		ok := true
		for _, t := range g.terms {
			if !termMatches(t, p) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func termMatches(t pktTerm, p *packetInfo) bool {
	switch t.kind {
	case "proto":
		switch t.val {
		case "tcp":
			return p.proto == "tcp"
		case "udp":
			return p.proto == "udp"
		case "icmp":
			return p.proto == "icmp"
		case "icmp6":
			return p.proto == "icmp6"
		case "ip":
			return p.ipVer == 4 && p.proto != ""
		case "ip6":
			return p.ipVer == 6 && p.proto != ""
		case "arp":
			return p.isARP
		}
	case "port":
		n, _ := strconv.Atoi(t.val)
		return p.srcPort == n || p.dstPort == n
	case "host":
		return p.srcIP == t.val || p.dstIP == t.val
	}
	return false
}

// ------------------------------------------------------------- command ------

type tcpdumpIfc struct {
	id   int
	name string
}

var tcpdumpListInterfaces = func() ([]tcpdumpIfc, error) { return nil, &unsupportedError{"pktmon"} }
var tcpdumpLive = func(o *tcpdumpLiveOpts) int { return 1 }

type tcpdumpLiveOpts struct {
	iface    string
	count    int
	write    string
	snaplen  int
	filter   *pktFilterExpr
	printOpt tcpdumpOpts
}

func cmdTcpdump(args []string) int {
	f := NewFlags(
		&Flag{Name: "interface", Short: 'i', Long: "interface", HasArg: true, ArgName: "IFACE", Help: "interface to capture on (name or pktmon component id; any = all)"},
		&Flag{Name: "count", Short: 'c', Long: "count", HasArg: true, ArgName: "N", Help: "exit after N packets (polled from pktmon counters)"},
		&Flag{Name: "write", Short: 'w', Long: "write", HasArg: true, ArgName: "FILE", Help: "write packets to FILE (pcapng via pktmon; .pcap is converted)"},
		&Flag{Name: "read", Short: 'r', Long: "read", HasArg: true, ArgName: "FILE", Help: "read packets from FILE (pcap/pcapng)"},
		&Flag{Name: "no-names", Short: 'n', Long: "no-names", Help: "do not resolve names (always the case)"},
		&Flag{Name: "no-port-names", Short: 'N', Long: "no-port-names", Help: "do not resolve port names (always the case)"},
		&Flag{Name: "snaplen", Short: 's', Long: "snaplen", HasArg: true, ArgName: "BYTES", Help: "capture/print at most BYTES of each packet (0 = full)"},
		&Flag{Name: "link", Short: 'e', Long: "link", Help: "print the link-layer header"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "print extra header detail (-vv accepted)"},
		&Flag{Name: "ascii", Short: 'A', Long: "ascii", Help: "print payload as ASCII"},
		&Flag{Name: "hex-ascii", Short: 'X', Long: "hex-ascii", Help: "print payload as hex + ASCII"},
		&Flag{Name: "list", Short: 'D', Long: "list-interfaces", Help: "list capture interfaces and exit"},
		&Flag{Name: "promiscuous", Short: 'p', Long: "no-promiscuous-mode", Help: "accepted for GNU compatibility (no effect on Windows)"},
		&Flag{Name: "quiet", Short: 'q', Long: "quiet", Help: "accepted for GNU compatibility (no effect)"},
	)
	if !parseOrDie("tcpdump", f, args) {
		return 2
	}
	if handleHelpVersion("tcpdump", "[OPTION]... [EXPRESSION]", f) {
		return 0
	}
	opts := tcpdumpOpts{
		noNames:   true,
		verbose:   f.Count("verbose"),
		showLink:  f.Has("link"),
		hexAscii:  f.Has("hex-ascii"),
		asciiOnly: f.Has("ascii"),
		out:       os.Stdout,
	}
	if s, e := f.Int("snaplen", 0); e == nil && s >= 0 {
		opts.snaplen = s
	}
	count, _ := f.Int("count", 0)
	if count < 0 {
		count = 0
	}
	expr := strings.Join(f.Positional, " ")
	var filter *pktFilterExpr
	if expr != "" {
		var err error
		filter, err = parsePktFilter(expr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tcpdump: %v\n", err)
			return 2
		}
		opts.filter = filter
	}

	// -D: list interfaces
	if f.Has("list") {
		ifaces, err := tcpdumpListInterfaces()
		if err != nil {
			fmt.Fprintf(os.Stderr, "tcpdump: %v (pktmon needs an elevated shell)\n", err)
			return 1
		}
		for i, ifc := range ifaces {
			fmt.Printf("%d. %s (id %d)\n", i+1, ifc.name, ifc.id)
		}
		return 0
	}

	// -r: offline analysis, no elevation needed
	if rfile := f.Str("read"); rfile != "" {
		pkts, link, err := readPacketFile(rfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tcpdump: %v\n", err)
			return 1
		}
		seen := 0
		for _, pk := range pkts {
			p := decodePacket(pk)
			if !filter.matches(p) {
				continue
			}
			printPacket(p, opts)
			seen++
			if count > 0 && seen >= count {
				break
			}
		}
		_ = link
		return 0
	}

	// live capture via pktmon (elevated)
	snaplen := opts.snaplen
	if snaplen == 0 {
		snaplen = 65535
	}
	lo := &tcpdumpLiveOpts{
		iface:    f.Str("interface"),
		count:    count,
		write:    f.Str("write"),
		snaplen:  snaplen,
		filter:   filter,
		printOpt: opts,
	}
	return tcpdumpLive(lo)
}

func readPacketFile(path string) ([]capturedPacket, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	var magic [4]byte
	n, _ := f.Read(magic[:])
	f.Close()
	if n == 4 {
		m := binary.LittleEndian.Uint32(magic[:])
		if m == 0xa1b2c3d4 || m == 0xd4c3b2a1 || m == 0xa1b23c4d || m == 0x4d3cb2a1 {
			return readPcapFile(path)
		}
		if m == 0x0a0d0d0a {
			return readPcapngFile(path)
		}
	}
	// fall back: try both
	pkts, link, err := readPcapFile(path)
	if err == nil {
		return pkts, link, nil
	}
	return readPcapngFile(path)
}
