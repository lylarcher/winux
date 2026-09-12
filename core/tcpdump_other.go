//go:build !windows

package core

// tcpdump live capture depends on pktmon, which is Windows-only; the stubs
// keep the package cross-compiling. Offline -r parsing still works anywhere.

import (
	"fmt"
	"os"
)

func listPktmonInterfaces() ([]tcpdumpIfc, error) {
	return nil, &unsupportedError{"pktmon (Windows Packet Monitor)"}
}

func livePktmon(o *tcpdumpLiveOpts) int {
	fmt.Fprintln(os.Stderr, "tcpdump: live capture requires Windows Packet Monitor (pktmon); use -r to read a pcap/pcapng file")
	return 1
}
