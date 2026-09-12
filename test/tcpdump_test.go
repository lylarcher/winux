package core_test

// Synthesized pcap / pcapng files exercising the offline (-r) side of tcpdump:
// Ethernet/IPv4/TCP, IPv6/UDP, ICMP and ARP packets, plus the -v/-X/-c/-e
// output controls and the filter expression matcher.

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"winux/core"
)

func ethPkt(payload []byte, etherType int) []byte {
	b := make([]byte, 14+len(payload))
	// dst 00:11:22:33:44:55, src 66:77:88:99:aa:bb
	copy(b[0:6], []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55})
	copy(b[6:12], []byte{0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb})
	binary.BigEndian.PutUint16(b[12:14], uint16(etherType))
	copy(b[14:], payload)
	return b
}

func ipv4Pkt(src, dst string, proto byte, payload []byte) []byte {
	hdr := make([]byte, 20)
	hdr[0] = 0x45
	binary.BigEndian.PutUint16(hdr[2:4], uint16(20+len(payload)))
	binary.BigEndian.PutUint16(hdr[4:6], 0x1234)
	binary.BigEndian.PutUint16(hdr[6:8], 0x4000) // DF
	hdr[8] = 64
	hdr[9] = proto
	copy(hdr[12:16], net.ParseIP(src).To4())
	copy(hdr[16:20], net.ParseIP(dst).To4())
	return append(hdr, payload...)
}

func tcpPkt(srcIP, dstIP string, sport, dport int, flags uint16, payload []byte) []byte {
	hdr := make([]byte, 20)
	binary.BigEndian.PutUint16(hdr[0:2], uint16(sport))
	binary.BigEndian.PutUint16(hdr[2:4], uint16(dport))
	binary.BigEndian.PutUint32(hdr[4:8], 1000)
	binary.BigEndian.PutUint32(hdr[8:12], 2000)
	hdr[12] = 0x50
	binary.BigEndian.PutUint16(hdr[12:14], 0x5000|flags)
	binary.BigEndian.PutUint16(hdr[14:16], 65535)
	return append(hdr, payload...)
}

func udpPkt(srcIP, dstIP string, sport, dport int, payload []byte) []byte {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint16(hdr[0:2], uint16(sport))
	binary.BigEndian.PutUint16(hdr[2:4], uint16(dport))
	binary.BigEndian.PutUint16(hdr[4:6], uint16(8+len(payload)))
	return append(hdr, payload...)
}

func ipv6Pkt(src, dst string, proto byte, payload []byte) []byte {
	hdr := make([]byte, 40)
	hdr[0] = 0x60
	binary.BigEndian.PutUint16(hdr[4:6], uint16(len(payload)))
	hdr[6] = proto
	hdr[7] = 64
	copy(hdr[8:24], net.ParseIP(src).To16())
	copy(hdr[24:40], net.ParseIP(dst).To16())
	return append(hdr, payload...)
}

func icmpEchoPkt(srcIP, dstIP string, id, seq int) []byte {
	b := []byte{8, 0, 0, 0, byte(id >> 8), byte(id), byte(seq >> 8), byte(seq)}
	return append(b, []byte("hello world")...)
}

func arpPkt(op int, spa, tpa string) []byte {
	b := []byte{0, 1, 8, 0, 6, 4, byte(op >> 8), byte(op)}
	b = append(b, []byte{0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb}...) // sender MAC
	b = append(b, net.ParseIP(spa).To4()...)
	b = append(b, []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}...) // target MAC
	b = append(b, net.ParseIP(tpa).To4()...)
	return b
}

func writePcap(t *testing.T, path string, link int, packets [][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var hdr [24]byte
	binary.LittleEndian.PutUint32(hdr[0:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(hdr[4:6], 2)
	binary.LittleEndian.PutUint16(hdr[6:8], 4)
	binary.LittleEndian.PutUint32(hdr[16:20], 65535)
	binary.LittleEndian.PutUint32(hdr[20:24], uint32(link))
	f.Write(hdr[:])
	ts := time.Date(2024, 3, 5, 12, 34, 56, 123456000, time.UTC)
	for i, p := range packets {
		var ph [16]byte
		binary.LittleEndian.PutUint32(ph[0:4], uint32(ts.Unix()+int64(i)))
		binary.LittleEndian.PutUint32(ph[4:8], uint32(ts.Nanosecond()/1000))
		binary.LittleEndian.PutUint32(ph[8:12], uint32(len(p)))
		binary.LittleEndian.PutUint32(ph[12:16], uint32(len(p)))
		f.Write(ph[:])
		f.Write(p)
	}
}

func pcapngBlock(btype uint32, body []byte) []byte {
	blen := uint32(12 + len(body) + pad4(len(body)))
	b := make([]byte, blen)
	binary.LittleEndian.PutUint32(b[0:4], btype)
	binary.LittleEndian.PutUint32(b[4:8], blen)
	copy(b[8:8+len(body)], body)
	binary.LittleEndian.PutUint32(b[blen-4:], blen)
	return b
}

func pad4(n int) int { return (4 - n%4) % 4 }

func writePcapng(t *testing.T, path string, packets [][]byte) {
	t.Helper()
	var out []byte
	out = append(out, pcapngBlock(0x0a0d0d0a, nil)...)
	idb := make([]byte, 12)
	binary.LittleEndian.PutUint16(idb[0:2], 1) // Ethernet
	binary.LittleEndian.PutUint32(idb[4:8], 65535)
	out = append(out, pcapngBlock(0x00000001, idb)...)
	base := time.Date(2024, 3, 5, 12, 34, 56, 0, time.UTC).UnixNano()
	for i, p := range packets {
		var epb []byte
		ts := uint64(base + int64(i)*int64(time.Millisecond))
		epb = binary.LittleEndian.AppendUint32(epb, 0) // interface id
		epb = binary.LittleEndian.AppendUint32(epb, uint32(ts>>32))
		epb = binary.LittleEndian.AppendUint32(epb, uint32(ts))
		epb = binary.LittleEndian.AppendUint32(epb, uint32(len(p)))
		epb = binary.LittleEndian.AppendUint32(epb, uint32(len(p)))
		epb = append(epb, p...)
		out = append(out, pcapngBlock(0x00000006, epb)...)
	}
	if err := os.WriteFile(path, out, 0o666); err != nil {
		t.Fatal(err)
	}
}

func mkPcap(t *testing.T, dir string) string {
	t.Helper()
	tcp := ethPkt(ipv4Pkt("10.0.0.1", "10.0.0.2", 6, tcpPkt("10.0.0.1", "10.0.0.2", 12345, 80, 0x02, nil)), 0x0800)
	udp := ethPkt(ipv4Pkt("192.168.1.5", "8.8.8.8", 17, udpPkt("192.168.1.5", "8.8.8.8", 5353, 53, []byte("dns-data"))), 0x0800)
	icmp := ethPkt(ipv4Pkt("10.0.0.2", "10.0.0.1", 1, icmpEchoPkt("10.0.0.2", "10.0.0.1", 0xbeef, 7)), 0x0800)
	ip6 := ethPkt(ipv6Pkt("2001:db8::1", "2001:db8::2", 17, udpPkt("2001:db8::1", "2001:db8::2", 9999, 443, []byte("hi"))), 0x86dd)
	arp := ethPkt(arpPkt(1, "10.0.0.1", "10.0.0.9"), 0x0806)
	p := filepath.Join(dir, "sample.pcap")
	writePcap(t, p, 1, [][]byte{tcp, udp, icmp, ip6, arp})
	return p
}

func runTcpdump(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return core.Execute("tcpdump", args, strings.NewReader(""))
}

func TestTcpdumpReadPcap(t *testing.T) {
	p := mkPcap(t, t.TempDir())
	code, out, errOut := runTcpdump(t, "-r", p, "-nn")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{
		"IP 10.0.0.1.12345 > 10.0.0.2.80: Flags [S], seq 1000, win 65535, length 0",
		"IP 192.168.1.5.5353 > 8.8.8.8.53: UDP, length 8",
		"ICMP echo request, id 48879, seq 7, length 11",
		"IP6 2001:db8::1.9999 > 2001:db8::2.443: UDP, length 2",
		"ARP, Request who-has 10.0.0.9 tell 66:77:88:99:aa:bb, length 42",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestTcpdumpReadPcapng(t *testing.T) {
	dir := t.TempDir()
	tcp := ethPkt(ipv4Pkt("10.0.0.1", "10.0.0.2", 6, tcpPkt("10.0.0.1", "10.0.0.2", 1111, 22, 0x12, []byte("ssh!"))), 0x0800)
	udp := ethPkt(ipv4Pkt("10.0.0.3", "10.0.0.4", 17, udpPkt("10.0.0.3", "10.0.0.4", 68, 67, nil)), 0x0800)
	path := filepath.Join(dir, "sample.pcapng")
	writePcapng(t, path, [][]byte{tcp, udp})
	code, out, errOut := runTcpdump(t, "-r", path)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "Flags [S.], seq 1000, ack 2000, win 65535, length 4") {
		t.Errorf("tcp (with ACK) line missing:\n%s", out)
	}
	if !strings.Contains(out, "10.0.0.3.68 > 10.0.0.4.67: UDP, length 0") {
		t.Errorf("udp line missing:\n%s", out)
	}
}

func TestTcpdumpFilters(t *testing.T) {
	p := mkPcap(t, t.TempDir())
	code, out, errOut := runTcpdump(t, "-r", p, "port 80")
	if code != 0 || strings.Contains(out, "5353") || !strings.Contains(out, "10.0.0.1.12345 > 10.0.0.2.80") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	code, out, _ = runTcpdump(t, "-r", p, "host 8.8.8.8")
	if code != 0 || !strings.Contains(out, "8.8.8.8") || strings.Contains(out, "10.0.0.1.12345") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, out, _ = runTcpdump(t, "-r", p, "udp")
	if code != 0 || strings.Contains(out, "Flags [S]") || !strings.Contains(out, "UDP, length 2") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	// OR semantics
	code, out, _ = runTcpdump(t, "-r", p, "tcp or icmp")
	if code != 0 || !strings.Contains(out, "Flags [S]") || !strings.Contains(out, "ICMP echo request") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	// unsupported primitives are refused
	code, _, errOut = runTcpdump(t, "-r", p, "not port 80")
	if code != 2 || !strings.Contains(errOut, "not") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestTcpdumpCountVerboseHex(t *testing.T) {
	p := mkPcap(t, t.TempDir())
	code, out, errOut := runTcpdump(t, "-r", p, "-c", "2")
	if code != 0 || strings.Count(out, "\n") > 3 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	code, out, _ = runTcpdump(t, "-r", p, "-v", "-c", "1")
	if code != 0 || !strings.Contains(out, "tos 0x0, ttl 64, id 4660, offset 0, flags [DF], proto TCP (6), length 40") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, out, _ = runTcpdump(t, "-r", p, "-X", "-c", "3")
	if code != 0 || !strings.Contains(out, "0x0000:") || !strings.Contains(out, "hello world") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	code, out, _ = runTcpdump(t, "-r", p, "-A", "-c", "3")
	if code != 0 || !strings.Contains(out, "hello world") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestTcpdumpLinkHeader(t *testing.T) {
	p := mkPcap(t, t.TempDir())
	code, out, errOut := runTcpdump(t, "-r", p, "-e", "-c", "1")
	if code != 0 || !strings.Contains(out, "00:11:22:33:44:55 > 66:77:88:99:aa:bb") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestTcpdumpBadFileAndExpr(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pcap")
	os.WriteFile(bad, []byte("this is not a pcap"), 0o666)
	code, _, errOut := runTcpdump(t, "-r", bad)
	if code != 1 || errOut == "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	code, _, errOut = runTcpdump(t, "-r", mkPcap(t, dir), "portrange 1-100")
	if code != 2 || !strings.Contains(errOut, "portrange") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}
