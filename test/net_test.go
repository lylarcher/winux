package core_test

import (
	"os"
	"strings"
	"testing"
)

// Network commands touch the live network stack of the machine. Per the
// project requirement these tests only run after the user consents, which is
// signalled by WINUX_NET_TESTS=1. All commands under test are read-only
// (socket/route/interface queries); mutating network commands are refused by
// the commands themselves.
func netSkip(t *testing.T) {
	t.Helper()
	if os.Getenv("WINUX_NET_TESTS") != "1" {
		t.Skip("network tests need WINUX_NET_TESTS=1 and explicit user consent")
	}
}

func TestNetstatConnections(t *testing.T) {
	netSkip(t)
	code, out, errOut := run(t, "", "netstat", "-an")
	if code != 0 {
		t.Fatalf("netstat -an failed: %s", errOut)
	}
	for _, want := range []string{"Active Connections", "Proto", "Local Address", "Foreign Address", "State"} {
		if !strings.Contains(out, want) {
			t.Errorf("netstat -an missing %q", want)
		}
	}
	if !strings.Contains(out, "LISTENING") && !strings.Contains(out, "ESTABLISHED") {
		t.Fatalf("netstat -an should list TCP sockets with states:\n%s", out)
	}
	// -o adds an owning PID column
	code, out, _ = run(t, "", "netstat", "-ano")
	if code != 0 || !strings.Contains(out, "PID") {
		t.Fatalf("netstat -ano missing the PID column:\n%s", out)
	}
	// -p tcp selects TCP only
	code, out, _ = run(t, "", "netstat", "-p", "tcp", "-an")
	if code != 0 {
		t.Fatal("netstat -p tcp failed")
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "udp") {
			t.Fatalf("netstat -p tcp listed a UDP socket:\n%s", out)
		}
	}
	code, out, _ = run(t, "", "netstat", "-p", "udp", "-an")
	if code != 0 || !strings.Contains(out, "udp") {
		t.Fatalf("netstat -p udp should list UDP sockets:\n%s", out)
	}
	if code, _, errOut := run(t, "", "netstat", "-p", "bogus"); code != 1 || !strings.Contains(errOut, "unsupported protocol") {
		t.Fatalf("netstat -p bogus: code=%d err=%q", code, errOut)
	}
}

func TestNetstatRoutesEthernetInterfaces(t *testing.T) {
	netSkip(t)
	if code, out, _ := run(t, "", "netstat", "-r"); code != 0 || !strings.Contains(out, "Active Routes") || !strings.Contains(out, "0.0.0.0") {
		t.Fatalf("netstat -r should list the IPv4 route table:\n%s", out)
	}
	if code, out, _ := run(t, "", "netstat", "-e"); code != 0 || !strings.Contains(out, "Interface Statistics") || !strings.Contains(out, "Bytes") {
		t.Fatalf("netstat -e should show interface statistics:\n%s", out)
	}
	if code, out, _ := run(t, "", "netstat", "-i"); code != 0 || !strings.Contains(out, "Kernel Interface table") {
		t.Fatalf("netstat -i should list interfaces:\n%s", out)
	}
	if code, out, _ := run(t, "", "netstat", "-s"); code != 0 || !strings.Contains(out, "IPv4 Statistics") || !strings.Contains(out, "TCP Statistics") {
		t.Fatalf("netstat -s should show per-protocol statistics:\n%s", out)
	}
}

func TestSsSocketsAndSummary(t *testing.T) {
	netSkip(t)
	code, out, errOut := run(t, "", "ss", "-t", "-a", "-n")
	if code != 0 {
		t.Fatalf("ss -t -a -n failed: %s", errOut)
	}
	for _, want := range []string{"State", "Recv-Q", "Send-Q", "Local Address:Port", "Peer Address:Port"} {
		if !strings.Contains(out, want) {
			t.Errorf("ss header missing %q", want)
		}
	}
	if !strings.Contains(out, "LISTEN") && !strings.Contains(out, "ESTAB") {
		t.Fatalf("ss -t -a should list sockets with states:\n%s", out)
	}
	if code, out, _ := run(t, "", "ss", "-s"); code != 0 || !strings.Contains(out, "Total:") || !strings.Contains(out, "TCP:") {
		t.Fatalf("ss -s summary wrong:\n%s", out)
	}
	// -l shows only listeners
	if code, out, _ := run(t, "", "ss", "-l", "-t", "-n"); code != 0 || !strings.Contains(out, "LISTEN") {
		t.Fatalf("ss -l -t should list listeners:\n%s", out)
	}
	// -4 excludes tcp6 rows
	code, out, _ = run(t, "", "ss", "-4", "-t", "-a", "-n")
	if code != 0 {
		t.Fatal("ss -4 failed")
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "tcp6") {
			t.Fatalf("ss -4 listed an IPv6 socket:\n%s", out)
		}
	}
	if code, out, _ := run(t, "", "ss", "-r"); code != 0 || (!strings.Contains(out, "default") && !strings.Contains(out, "metric")) {
		t.Fatalf("ss -r should list routes:\n%s", out)
	}
}
