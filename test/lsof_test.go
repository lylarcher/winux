package core_test

// lsof is exercised end to end: the test opens a real listening socket (and a
// UDP socket) and then asks winux lsof about it, which is exactly how the
// command is used in practice. The selection grammar itself is unit tested in
// core/lsof_parse_test.go.

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLsofHelpAndVersion(t *testing.T) {
	code, out, errOut := run(t, "", "lsof", "--help")
	if code != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	if !strings.Contains(out, "-i") || !strings.Contains(out, "exit status") {
		t.Fatalf("help is missing the selection options: %q", out)
	}
	code, out, errOut = run(t, "", "lsof", "--version")
	if code != 0 || !strings.Contains(out, "lsof") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

// listenTCP opens an ephemeral listener and returns its port.
func listenTCP(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot open a listener: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestLsofFindsListener(t *testing.T) {
	port := listenTCP(t)
	code, out, errOut := run(t, "", "lsof", "-i:"+strconv.Itoa(port), "-P", "-n")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "COMMAND") || !strings.Contains(out, "NAME") {
		t.Fatalf("missing header: %q", out)
	}
	if !strings.Contains(out, ":"+strconv.Itoa(port)) || !strings.Contains(out, "(LISTEN)") {
		t.Fatalf("listener not reported: %q", out)
	}
	// the socket belongs to this test process
	if !strings.Contains(out, strconv.Itoa(os.Getpid())) {
		t.Fatalf("own pid missing from %q", out)
	}
	if !strings.Contains(out, "TCP") {
		t.Fatalf("NODE column should say TCP: %q", out)
	}
	// -i selects internet files only
	if strings.Contains(out, " txt ") {
		t.Fatalf("-i must not list process images: %q", out)
	}
}

func TestLsofBareSelectorForms(t *testing.T) {
	port := listenTCP(t)
	// `-i PORT` (space separated) and `-i:PORT` must agree
	code, out, errOut := run(t, "", "lsof", "-i", strconv.Itoa(port), "-P", "-n")
	if code != 0 {
		t.Fatalf("space form: code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, ":"+strconv.Itoa(port)) {
		t.Fatalf("space form did not find the listener: %q", out)
	}
	// every data row must be about this very port: a mis-parsed selector — the
	// leading digit of a port such as 63389 eaten as an address-family prefix —
	// would silently report a different port instead
	for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		if !strings.Contains(line, ":"+strconv.Itoa(port)) {
			t.Fatalf("row is not about port %d: %q", port, line)
		}
	}
	// the port range form covers it too
	lo := port - 1
	hi := port + 1
	code, out, errOut = run(t, "", "lsof", "-i:"+strconv.Itoa(lo)+"-"+strconv.Itoa(hi), "-P", "-n")
	if code != 0 || !strings.Contains(out, ":"+strconv.Itoa(port)) {
		t.Fatalf("range form: code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestLsofTersePrintsOnlyPIDs(t *testing.T) {
	port := listenTCP(t)
	code, out, errOut := run(t, "", "lsof", "-t", "-i:"+strconv.Itoa(port), "-P", "-n")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	got := strings.TrimSpace(out)
	if got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("-t output = %q, want just %d", got, os.Getpid())
	}
	// a terse run is meant to feed `kill`, so there must be no header
	if strings.Contains(out, "COMMAND") || strings.Contains(out, "LISTEN") {
		t.Fatalf("-t printed more than pids: %q", out)
	}
}

func TestLsofUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot open a UDP socket: %v", err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	code, out, errOut := run(t, "", "lsof", "-i", "udp", "-P", "-n")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "UDP") || !strings.Contains(out, ":"+strconv.Itoa(port)) {
		t.Fatalf("UDP socket not reported: %q", out)
	}
	if !strings.Contains(out, strconv.Itoa(os.Getpid())) {
		t.Fatalf("owner pid missing: %q", out)
	}
}

func TestLsofProcessImage(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	code, out, errOut := run(t, "", "lsof", "-p", strconv.Itoa(os.Getpid()))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "txt") {
		t.Fatalf("no txt entry: %q", out)
	}
	// the image path of this very process must be reported (case-insensitively,
	// because the API and os.Executable may disagree on the drive-letter case)
	if !strings.Contains(strings.ToLower(out), strings.ToLower(exe)) {
		t.Fatalf("process image %q missing from %q", exe, out)
	}
	if !strings.Contains(out, filepath.Base(exe)) {
		t.Fatalf("COMMAND column should be the image name: %q", out)
	}
}

// The -a flag turns the default union into an intersection, and negations
// always exclude.
func TestLsofAndSelectionAndNegation(t *testing.T) {
	port := listenTCP(t)
	sel := "-i:" + strconv.Itoa(port)

	// union (default): our listener matches -i even though the pid does not
	code, out, _ := run(t, "", "lsof", sel, "-p", "4", "-P", "-n")
	if code != 0 || !strings.Contains(out, ":"+strconv.Itoa(port)) {
		t.Fatalf("union: code=%d out=%q", code, out)
	}
	// intersection: -a -p 4 cannot match our listener
	code, out, _ = run(t, "", "lsof", "-a", sel, "-p", "4", "-P", "-n")
	if code != 1 {
		t.Fatalf("intersection: code=%d out=%q (want 1, nothing matched)", code, out)
	}
	if strings.Contains(out, ":"+strconv.Itoa(port)) {
		t.Fatalf("intersection leaked a match: %q", out)
	}
	// negation: exclude our own pid
	code, out, _ = run(t, "", "lsof", sel, "-p", "^"+strconv.Itoa(os.Getpid()), "-P", "-n")
	if code != 1 || strings.Contains(out, ":"+strconv.Itoa(port)) {
		t.Fatalf("negation: code=%d out=%q", code, out)
	}
}

func TestLsofStateSelection(t *testing.T) {
	port := listenTCP(t)
	code, out, errOut := run(t, "", "lsof", "-sTCP:LISTEN", "-i:"+strconv.Itoa(port), "-P", "-n")
	if code != 0 || !strings.Contains(out, "(LISTEN)") {
		t.Fatalf("listen filter: code=%d out=%q stderr=%q", code, out, errOut)
	}
	// the same socket is not ESTABLISHED, so this must find nothing
	code, out, _ = run(t, "", "lsof", "-sTCP:ESTABLISHED", "-i:"+strconv.Itoa(port), "-P", "-n")
	if code != 1 || strings.Contains(out, "(ESTABLISHED)") {
		t.Fatalf("established filter: code=%d out=%q", code, out)
	}
}

func TestLsofNoMatchExitStatus(t *testing.T) {
	code, out, errOut := run(t, "", "lsof", "-c", "zzz-no-such-command-zzz")
	if code != 1 {
		t.Fatalf("code=%d want 1 (nothing matched) stderr=%q out=%q", code, errOut, out)
	}
	// the header is still printed, like lsof does
	if !strings.Contains(out, "COMMAND") {
		t.Fatalf("missing header: %q", out)
	}
}

func TestLsofRejectsBadOptions(t *testing.T) {
	for _, args := range [][]string{
		{"-Z"},
		{"+d", "C:\\"},
		{"-i:99999"},
		{"-sTCP:BOGUS"},
		{"-p", "notanumber"},
		{"-i", "@host:notaport"},
	} {
		code, _, errOut := run(t, "", "lsof", args...)
		if code != 1 {
			t.Errorf("lsof %v: code=%d want 1", args, code)
		}
		if !strings.Contains(errOut, "lsof:") {
			t.Errorf("lsof %v: stderr=%q", args, errOut)
		}
	}
}

func TestLsofCommandFilter(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	name := strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe))
	code, out, errOut := run(t, "", "lsof", "-c", name, "-p", strconv.Itoa(os.Getpid()))
	if code != 0 {
		t.Fatalf("-c %s: code=%d stderr=%q", name, code, errOut)
	}
	if !strings.Contains(out, "txt") {
		t.Fatalf("-c %s did not list the process: %q", name, out)
	}
}
