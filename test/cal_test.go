package core_test

import (
	"strings"
	"testing"
	"time"
)

func TestCalMonth(t *testing.T) {
	code, out, errOut := run(t, "", "cal", "3", "2025")
	if code != 0 {
		t.Fatalf("cal 3 2025 failed: %s", errOut)
	}
	for _, want := range []string{"March 2025", "Su Mo Tu We Th Fr Sa", " 1", "30 31"} {
		if !strings.Contains(out, want) {
			t.Errorf("cal 3 2025 missing %q:\n%s", want, out)
		}
	}
	// March 1 2025 is a Saturday: the leading cell is empty and "1" sits
	// at the very end of the first week row
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("too few lines: %q", out)
	}
	if !strings.HasSuffix(lines[2], "1") {
		t.Fatalf("March 1 2025 should end the first week row: %q", lines[2])
	}
	// a bare `cal` prints the current month
	if _, out, _ := run(t, "", "cal"); !strings.Contains(out, time.Now().Format("January 2006")) {
		t.Fatalf("bare cal = %q", out)
	}
}

func TestCalLeapFebruary(t *testing.T) {
	code, out, _ := run(t, "", "cal", "2", "2024")
	if code != 0 {
		t.Fatal("cal 2 2024 failed")
	}
	if !strings.Contains(out, "29") {
		t.Fatalf("2024 is a leap year, February must contain 29:\n%s", out)
	}
	if _, out, _ := run(t, "", "cal", "2", "2025"); strings.Contains(out, "29") {
		t.Fatalf("2025 is not a leap year, February must not contain 29:\n%s", out)
	}
}

func TestCalMondayAndJulian(t *testing.T) {
	code, out, _ := run(t, "", "cal", "-m", "3", "2025")
	if code != 0 {
		t.Fatal("cal -m failed")
	}
	if !strings.Contains(out, "Mo Tu We Th Fr Sa Su") {
		t.Fatalf("cal -m header wrong:\n%s", out)
	}
	// Julian day numbers: March 1 2025 is day 60 (31 + 28 + 1)
	code, out, _ = run(t, "", "cal", "-j", "3", "2025")
	if code != 0 {
		t.Fatal("cal -j failed")
	}
	if !strings.Contains(out, "60") || !strings.Contains(out, "90") {
		t.Fatalf("cal -j should show day-of-year numbers (60..90 for March 2025):\n%s", out)
	}
}

func TestCalYear(t *testing.T) {
	code, out, errOut := run(t, "", "cal", "2025")
	if code != 0 {
		t.Fatalf("cal 2025 failed: %s", errOut)
	}
	for _, want := range []string{"2025", "January", "February", "December", "Su Mo Tu We Th Fr Sa"} {
		if !strings.Contains(out, want) {
			t.Errorf("cal 2025 missing %q", want)
		}
	}
	// -y shows the current year
	now := time.Now()
	if _, out, _ := run(t, "", "cal", "-y"); !strings.Contains(out, now.Format("2006")) {
		t.Fatalf("cal -y should show %d:\n%s", now.Year(), out)
	}
}

func TestCalThreeAndRelative(t *testing.T) {
	now := time.Now()
	prev := now.AddDate(0, -1, 0).Format("January 2006")
	cur := now.Format("January 2006")
	next := now.AddDate(0, 1, 0).Format("January 2006")
	code, out, errOut := run(t, "", "cal", "-3")
	if code != 0 {
		t.Fatalf("cal -3 failed: %s", errOut)
	}
	for _, want := range []string{prev, cur, next} {
		if !strings.Contains(out, want) {
			t.Errorf("cal -3 missing %q:\n%s", want, out)
		}
	}
	// -B 1 -A 1 spans the same three months around today
	code, out, _ = run(t, "", "cal", "-B", "1", "-A", "1")
	if code != 0 || !strings.Contains(out, prev) || !strings.Contains(out, next) {
		t.Fatalf("cal -B 1 -A 1:\n%s", out)
	}
}

func TestCalInvalidArguments(t *testing.T) {
	if code, _, _ := run(t, "", "cal", "0", "2025"); code != 1 {
		t.Fatal("cal 0 2025 should fail")
	}
	if code, _, _ := run(t, "", "cal", "13", "2025"); code != 1 {
		t.Fatal("cal 13 2025 should fail")
	}
	if code, _, _ := run(t, "", "cal", "abc"); code != 1 {
		t.Fatal("cal abc should fail")
	}
	if code, _, _ := run(t, "", "cal", "1", "2", "3"); code != 1 {
		t.Fatal("cal with three arguments should fail")
	}
	if code, _, _ := run(t, "", "cal", "-y", "-3"); code != 1 {
		t.Fatal("cal -y -3 should be refused")
	}
	if code, _, _ := run(t, "", "cal", "-A", "x"); code != 1 {
		t.Fatal("cal -A x should fail")
	}
}
