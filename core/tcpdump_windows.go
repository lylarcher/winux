//go:build windows

package core

// The live-capture side of tcpdump maps to the built-in Packet Monitor. pktmon
// needs an elevated process, so cmdTcpdump re-runs itself through UAC first;
// inside the elevated instance we drive pktmon, then convert the ETL log to
// pcapng (etl2pcap) and reuse the pure-Go reader/printer.

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func init() {
	tcpdumpListInterfaces = listPktmonInterfaces
	tcpdumpLive = livePktmon
}

var pktmonIdRe = regexp.MustCompile(`(?i)id[:：]?\s*(\d+)`)

func runPktmon(args ...string) (string, error) {
	cmd := exec.Command("pktmon.exe", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// listPktmonInterfaces parses `pktmon list`: lines carry a name and an "Id:".
// The exact wording is localized, so we look for the id marker and take the
// text before it as the interface name.
func listPktmonInterfaces() ([]tcpdumpIfc, error) {
	out, err := runPktmon("list")
	if err != nil {
		return nil, err
	}
	var ifaces []tcpdumpIfc
	for _, line := range strings.Split(out, "\n") {
		m := pktmonIdRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, _ := strconv.Atoi(m[1])
		name := strings.TrimSpace(pktmonIdRe.ReplaceAllString(line, ""))
		name = strings.TrimSpace(strings.TrimPrefix(name, ":"))
		name = strings.Trim(name, "\t -")
		if name == "" {
			name = fmt.Sprintf("component %d", id)
		}
		ifaces = append(ifaces, tcpdumpIfc{id: id, name: name})
	}
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("no interfaces found (pktmon list produced no parseable entries)")
	}
	return ifaces, nil
}

func resolveInterface(spec string) ([]string, error) {
	if spec == "" || spec == "any" {
		return nil, nil // all components
	}
	if id, err := strconv.Atoi(spec); err == nil {
		return []string{"--comp", strconv.Itoa(id)}, nil
	}
	ifaces, err := listPktmonInterfaces()
	if err != nil {
		return nil, err
	}
	for _, ifc := range ifaces {
		if strings.EqualFold(ifc.name, spec) || strings.Contains(strings.ToLower(ifc.name), strings.ToLower(spec)) {
			return []string{"--comp", strconv.Itoa(ifc.id)}, nil
		}
	}
	return nil, fmt.Errorf("no such interface '%s' (run 'winux tcpdump -D' to list them)", spec)
}

// pktmonFilterArgs translates the parsed filter expression into one pktmon
// filter per OR group (pktmon reports a packet when it matches all conditions
// of at least one filter, which is exactly OR-of-AND semantics).
func pktmonFilterArgs(e *pktFilterExpr) [][]string {
	if e == nil {
		return nil
	}
	var out [][]string
	for i, g := range e.groups {
		args := []string{"filter", "add", fmt.Sprintf("winuxf%d", i)}
		for _, t := range g.terms {
			switch t.kind {
			case "proto":
				switch t.val {
				case "tcp":
					args = append(args, "-t", "TCP")
				case "udp":
					args = append(args, "-t", "UDP")
				case "icmp":
					args = append(args, "-t", "ICMP")
				case "icmp6":
					args = append(args, "-t", "ICMPv6")
				case "ip":
					args = append(args, "-d", "IPv4")
				case "ip6":
					args = append(args, "-d", "IPv6")
				case "arp":
					args = append(args, "-d", "ARP")
				}
			case "port":
				args = append(args, "-p", t.val)
			case "host":
				args = append(args, "-i", t.val)
			}
		}
		out = append(out, args)
	}
	return out
}

// parseCounters sums the packet counters from `pktmon counters`. Rows end with
// two numbers (packets, bytes); the header and separator lines do not.
func parseCounters(out string) int64 {
	total := int64(0)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pkts, err1 := strconv.ParseInt(fields[len(fields)-2], 10, 64)
		_, err2 := strconv.ParseInt(fields[len(fields)-1], 10, 64)
		if err1 == nil && err2 == nil {
			total += pkts
		}
	}
	return total
}

func livePktmon(o *tcpdumpLiveOpts) int {
	if !isElevated() {
		// re-run this same command in an elevated process (UAC)
		return accountElevate("tcpdump", os.Args[2:])
	}
	comp, err := resolveInterface(o.iface)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tcpdump: %v\n", err)
		return 1
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("winux-pktmon-%d", os.Getpid()))
	etl := tmp + ".etl"
	pcapng := tmp + ".pcapng"
	defer os.Remove(etl)
	defer os.Remove(pcapng)

	ifaceName := o.iface
	if ifaceName == "" {
		ifaceName = "all"
	}
	fmt.Fprintf(os.Stderr, "winux tcpdump: listening on %s, link-type EN10MB (Ethernet), snapshot length %d bytes\n", ifaceName, o.snaplen)
	fmt.Fprintln(os.Stderr, "winux tcpdump: press Ctrl+C to stop capture")

	// filters first (before start)
	filters := pktmonFilterArgs(o.filter)
	if _, err := runPktmon("filter", "remove"); err != nil {
		fmt.Fprintf(os.Stderr, "tcpdump: pktmon filter remove: %v\n", err)
	}
	for _, fa := range filters {
		if _, err := runPktmon(fa...); err != nil {
			fmt.Fprintf(os.Stderr, "tcpdump: pktmon filter add: %v\n", err)
		}
	}
	defer runPktmon("filter", "remove")

	startArgs := []string{"start", "--capture"}
	if o.snaplen > 0 {
		startArgs = append(startArgs, "--pkt-size", strconv.Itoa(o.snaplen))
	}
	startArgs = append(startArgs, comp...)
	startArgs = append(startArgs, "-f", etl)
	if out, err := runPktmon(startArgs...); err != nil {
		fmt.Fprintf(os.Stderr, "tcpdump: pktmon start: %v\n%s\n", err, out)
		return 1
	}

	// wait for Ctrl+C, or poll counters until -c N packets are seen
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	gotCount := false
	if o.count > 0 {
		gotCount = true
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		unparsed := 0
	loop:
		for {
			select {
			case <-done:
				break loop
			case <-ticker.C:
				out, err := runPktmon("counters")
				if err != nil {
					unparsed++
					if unparsed > 4 {
						fmt.Fprintln(os.Stderr, "tcpdump: cannot read pktmon counters; capture until Ctrl+C")
						gotCount = false
						break loop
					}
					continue
				}
				if parseCounters(out) >= int64(o.count) {
					break loop
				}
			}
		}
	} else {
		<-done
	}

	stopOut, stopErr := runPktmon("stop")
	if stopErr != nil {
		fmt.Fprintf(os.Stderr, "tcpdump: pktmon stop: %v\n%s\n", stopErr, stopOut)
	}

	// convert and print / save
	if _, err := runPktmon("etl2pcap", etl, "-o", pcapng); err != nil {
		fmt.Fprintf(os.Stderr, "tcpdump: pktmon etl2pcap: %v\n", err)
		return 1
	}
	pkts, link, err := readPcapngFile(pcapng)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tcpdump: %v\n", err)
		return 1
	}
	if o.write != "" {
		if strings.EqualFold(filepath.Ext(o.write), ".pcap") {
			// convert pcapng -> classic pcap so the extension is honoured
			if err := writePcapFile(o.write, link, pkts); err != nil {
				fmt.Fprintf(os.Stderr, "tcpdump: %v\n", err)
				return 1
			}
		} else {
			data, err := os.ReadFile(pcapng)
			if err != nil {
				fmt.Fprintf(os.Stderr, "tcpdump: %v\n", err)
				return 1
			}
			if err := os.WriteFile(o.write, data, 0o666); err != nil {
				fmt.Fprintf(os.Stderr, "tcpdump: %v\n", err)
				return 1
			}
		}
		fmt.Fprintf(os.Stderr, "tcpdump: %d packets written to %s (pcapng)\n", len(pkts), o.write)
		if strings.EqualFold(filepath.Ext(o.write), ".pcap") {
			fmt.Fprintln(os.Stderr, "tcpdump: note: converted to classic pcap; timestamps kept, link type preserved")
		}
	} else {
		for _, pk := range pkts {
			p := decodePacket(pk)
			if !o.filter.matches(p) {
				continue
			}
			printPacket(p, o.printOpt)
		}
		fmt.Fprintf(os.Stderr, "tcpdump: %d packets captured\n", len(pkts))
	}
	if gotCount && o.count > 0 && int64(len(pkts)) < int64(o.count) {
		// counters were probably double-counted across stack components;
		// this is informational only
	}
	return 0
}
