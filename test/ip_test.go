package core_test

import (
	"os"
	"strings"
	"testing"
)

func TestIpAddrAndLink(t *testing.T) {
	netSkip(t)
	code, out, errOut := run(t, "", "ip", "addr")
	if code != 0 {
		t.Fatalf("ip addr failed: %s", errOut)
	}
	for _, want := range []string{"<", "> mtu ", "state ", "link/", "inet "} {
		if !strings.Contains(out, want) {
			t.Errorf("ip addr missing %q", want)
		}
	}
	if !strings.Contains(out, "127.0.0.1/8") {
		t.Errorf("ip addr should show the loopback address:\n%s", out)
	}
	// brief mode
	if code, out, _ := run(t, "", "ip", "-br", "a"); code != 0 || !strings.Contains(out, "UP") {
		t.Fatalf("ip -br a wrong:\n%s", out)
	}
	// dev filter is exact; an unknown device yields no output
	if code, out, _ := run(t, "", "ip", "a", "s", "dev", "no-such-device-xyz"); code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("ip a s dev no-such-device-xyz should print nothing:\n%q", out)
	}
	if code, out, _ := run(t, "", "ip", "-4", "a"); code != 0 || strings.Contains(out, "inet6") {
		t.Fatalf("ip -4 a should hide IPv6:\n%s", out)
	}
	// link
	if code, out, _ := run(t, "", "ip", "link"); code != 0 || !strings.Contains(out, "mtu") {
		t.Fatalf("ip link wrong:\n%s", out)
	}
}

func TestIpRoutesNeighboursAndRefusals(t *testing.T) {
	netSkip(t)
	if code, out, _ := run(t, "", "ip", "route"); code != 0 || (!strings.Contains(out, "default") && !strings.Contains(out, "dev")) {
		t.Fatalf("ip route wrong:\n%s", out)
	}
	if code, _, _ := run(t, "", "ip", "neigh"); code != 0 {
		t.Fatal("ip neigh failed")
	}
	// every mutating subcommand is refused, exit 1
	for _, c := range [][]string{
		{"ip", "addr", "add", "1.2.3.4/24", "dev", "WLAN"},
		{"ip", "addr", "del", "1.2.3.4/24", "dev", "WLAN"},
		{"ip", "link", "set", "WLAN", "up"},
		{"ip", "route", "add", "default", "via", "1.2.3.4"},
		{"ip", "neigh", "add", "1.2.3.4", "lladdr", "aa:bb:cc:dd:ee:ff", "dev", "WLAN"},
	} {
		code, _, errOut := run(t, "", c[0], c[1:]...)
		if code != 1 || !strings.Contains(errOut, "refused") {
			t.Errorf("%v: code=%d err=%q (want refusal)", c, code, errOut)
		}
	}
	// unknown objects fail cleanly
	if code, _, errOut := run(t, "", "ip", "bogus"); code != 1 || !strings.Contains(errOut, "unknown") {
		t.Fatalf("ip bogus: code=%d err=%q", code, errOut)
	}
}

func TestNmcliAndNmtui(t *testing.T) {
	netSkip(t)
	code, out, errOut := run(t, "", "nmcli")
	if code != 0 {
		t.Fatalf("nmcli failed: %s", errOut)
	}
	for _, want := range []string{"DEVICE", "TYPE", "STATE"} {
		if !strings.Contains(out, want) {
			t.Errorf("nmcli missing %q", want)
		}
	}
	if code, out, _ := run(t, "", "nmcli", "device", "show"); code != 0 || !strings.Contains(out, "GENERAL.DEVICE") {
		t.Fatalf("nmcli device show wrong:\n%s", out)
	}
	if code, out, _ := run(t, "", "nmcli", "connection", "show"); code != 0 || !strings.Contains(out, "NAME") {
		t.Fatalf("nmcli connection show wrong:\n%s", out)
	}
	// mutating nmcli subcommands are refused
	for _, c := range [][]string{
		{"nmcli", "device", "connect", "WLAN"},
		{"nmcli", "device", "disconnect", "WLAN"},
		{"nmcli", "connection", "up", "WLAN"},
		{"nmcli", "networking", "off"},
		{"nmcli", "radio", "wifi", "off"},
	} {
		code, _, errOut := run(t, "", c[0], c[1:]...)
		if code != 1 || !strings.Contains(errOut, "refused") {
			t.Errorf("%v: code=%d err=%q (want refusal)", c, code, errOut)
		}
	}
	// nmtui explains and fails
	code, _, errOut = run(t, "", "nmtui")
	if code != 1 || !strings.Contains(errOut, "NetworkManager") {
		t.Fatalf("nmtui: code=%d err=%q", code, errOut)
	}
}

func TestIpconfig(t *testing.T) {
	netSkip(t)
	code, out, errOut := run(t, "", "ipconfig")
	if code != 0 {
		t.Fatalf("ipconfig failed: %s", errOut)
	}
	if !strings.Contains(out, "IP") || len(out) < 50 {
		t.Fatalf("ipconfig should print real configuration:\n%s", out)
	}
	if code, out, _ := run(t, "", "ipconfig", "/all"); code != 0 || !strings.Contains(out, "IP") {
		t.Fatalf("ipconfig /all failed:\n%s", out)
	}
	// --help is winux's own
	if code, out, _ := run(t, "", "ipconfig", "--help"); code != 0 || !strings.Contains(out, "Usage: ipconfig") {
		t.Fatalf("ipconfig --help wrong:\n%s", out)
	}
	// mutating flags are refused
	for _, f := range []string{"/release", "/renew", "/flushdns", "/registerdns"} {
		if code, _, errOut := run(t, "", "ipconfig", f); code != 1 || !strings.Contains(errOut, "refused") {
			t.Errorf("ipconfig %s: code=%d err=%q (want refusal)", f, code, errOut)
		}
	}
}

// Guard against accidental network mutation even in the ungated test paths.
func TestNetworkRefusalGuard(t *testing.T) {
	if os.Getenv("WINUX_NET_TESTS") != "1" {
		t.Skip("network tests need WINUX_NET_TESTS=1 and explicit user consent")
	}
}
