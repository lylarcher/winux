package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDfParseBlockSize(t *testing.T) {
	cases := []struct {
		in    string
		bytes int64
		label string
		err   bool
	}{
		{"512", 512, "512", false},
		{"1K", 1024, "1K", false},
		{"k", 1024, "1K", false},
		{"KB", 1000, "1KB", false},
		{"M", 1 << 20, "1M", false},
		{"2M", 2 << 20, "2M", false},
		{"1GB", 1000 * 1000 * 1000, "1GB", false},
		{"1G", 1 << 30, "1G", false},
		{"3T", 3 << 40, "3T", false},
		{"0", 0, "", true},
		{"x", 0, "", true},
		{"1Q", 0, "", true},
		{"-5", 0, "", true},
	}
	for _, c := range cases {
		got, label, err := parseBlockSize(c.in)
		if c.err {
			if err == nil {
				t.Errorf("parseBlockSize(%q) should fail, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseBlockSize(%q) failed: %v", c.in, err)
			continue
		}
		if got != c.bytes || label != c.label {
			t.Errorf("parseBlockSize(%q) = %d/%q, want %d/%q", c.in, got, label, c.bytes, c.label)
		}
	}
}

func TestDfPct(t *testing.T) {
	if got := dfPct(50, 100); got != "50%" {
		t.Errorf("dfPct(50,100) = %q", got)
	}
	if got := dfPct(1, 3); got != "33%" {
		t.Errorf("dfPct(1,3) = %q", got)
	}
	if got := dfPct(0, 0); got != "-" {
		t.Errorf("dfPct(0,0) = %q", got)
	}
}

func TestDfSiHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:       "0",
		999:     "999",
		1500:    "1.5K",
		123456:  "123K",
		1500000: "1.5M",
		10e9:    "10G",
	}
	for n, want := range cases {
		if got := siHumanSize(n); got != want {
			t.Errorf("siHumanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDfDriveRootOf(t *testing.T) {
	if got, err := driveRootOf("C:"); err != nil || got != `C:\` {
		t.Errorf("driveRootOf(C:) = %q, %v", got, err)
	}
	if got, err := driveRootOf("c:\\"); err != nil || got != `C:\` {
		t.Errorf("driveRootOf(c:\\) = %q, %v", got, err)
	}
	wd, _ := os.Getwd()
	vol := filepath.VolumeName(wd)
	if got, err := driveRootOf("."); err != nil || got != strings.ToUpper(vol[:2])+`\` {
		t.Errorf("driveRootOf(.) = %q, %v", got, err)
	}
	if _, err := driveRootOf(`\\server\share`); err == nil {
		t.Error("driveRootOf(UNC) should fail")
	}
	// a plain relative path maps to the current drive without needing to exist
	if _, err := driveRootOf("some-relative-path"); err != nil {
		t.Errorf("driveRootOf(relative) = %v", err)
	}
}

func TestDfIsBareDriveRef(t *testing.T) {
	if !isBareDriveRef("C:") || !isBareDriveRef(`C:\`) {
		t.Error("bare drive refs should be recognized")
	}
	if isBareDriveRef(`C:\x`) || isBareDriveRef(".") || isBareDriveRef("x") {
		t.Error("non-bare refs should not be recognized")
	}
}

func TestDfPrintTable(t *testing.T) {
	o := dfOpts{header: "1K-blocks", blockSize: 1024}
	rows := []dfRow{
		{fs: "C:", blocks: "1000", used: "250", avail: "750", usePct: "25%", mounted: `C:\`},
		{fs: "D:", blocks: "10", used: "8", avail: "2", usePct: "80%", mounted: `D:\`},
	}
	out := captureStdout(t, func() { printDfTable(o, rows) })
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got %d lines: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "Filesystem 1K-blocks") || !strings.Contains(lines[0], "Mounted on") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "C:") || !strings.HasSuffix(strings.TrimRight(lines[1], " "), `C:\`) {
		t.Errorf("row1 = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "D:") || !strings.HasSuffix(strings.TrimRight(lines[2], " "), `D:\`) {
		t.Errorf("row2 = %q", lines[2])
	}
}

func TestDfPrintTableWithType(t *testing.T) {
	o := dfOpts{header: "1K-blocks", blockSize: 1024, printType: true}
	rows := []dfRow{{fs: "C:", typ: "NTFS", blocks: "1", used: "0", avail: "1", usePct: "0%", mounted: `C:\`}}
	out := captureStdout(t, func() { printDfTable(o, rows) })
	if !strings.Contains(out, "Type") {
		t.Errorf("missing Type column: %q", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	b := make([]byte, 1<<20)
	n, _ := r.Read(b)
	r.Close()
	return string(b[:n])
}
