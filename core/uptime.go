package core

// uptime prints how long the system has been running, GNU style. The boot
// clock and the logged-in session count come from the platform layer
// (uptime_windows.go / uptime_other.go). Windows has no 1/5/15-minute load
// average, so the classic "load average: a, b, c" column is reported
// honestly as N/A with a note instead of fabricating numbers.

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func init() {
	addCmd("uptime", "print how long the system has been running", cmdUptime)
}

func cmdUptime(args []string) int {
	f := NewFlags(
		&Flag{Name: "pretty", Short: 'p', Long: "pretty", Help: "show uptime in a friendly human-readable format"},
		&Flag{Name: "since", Short: 's', Long: "since", Help: "print the system boot time instead"},
	)
	if !parseOrDie("uptime", f, args) {
		return 2
	}
	if handleHelpVersion("uptime", "[-ps]", f) {
		return 0
	}
	if len(f.Positional) > 0 {
		warn("uptime", "unexpected argument '%s'", f.Positional[0])
		return 1
	}
	if f.Has("pretty") && f.Has("since") {
		warn("uptime", "cannot combine -p and -s")
		return 1
	}

	up := systemUptime()
	if up <= 0 {
		warn("uptime", "not supported on this platform")
		return 1
	}
	now := time.Now()
	switch {
	case f.Has("since"):
		fmt.Println(now.Add(-up).Format("2006-01-02 15:04:05"))
	case f.Has("pretty"):
		fmt.Println(formatPrettyUptime(up))
	default:
		users := activeSessionCount()
		fmt.Printf("%2d:%02d:%02d %s, %d user%s,  load average: N/A\n",
			now.Hour(), now.Minute(), now.Second(), formatUptime(up), users, plural(users))
		fmt.Fprintln(os.Stderr, "uptime: Windows has no 1/5/15-minute load average; N/A is shown instead")
	}
	return 0
}

// formatUptime renders the duration GNU-style: "up 3 days,  4:05" with at
// least one day, "up  4:05" below a day, "up 45 min" below an hour.
func formatUptime(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("up %d days, %2d:%02d", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("up %2d:%02d", hours, mins)
	default:
		return fmt.Sprintf("up %d min", mins)
	}
}

// formatPrettyUptime renders the duration for `uptime -p`.
func formatPrettyUptime(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", days, pluralUnit(days, "day")))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", hours, pluralUnit(hours, "hour")))
	}
	if mins > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d %s", mins, pluralUnit(mins, "minute")))
	}
	return "up " + strings.Join(parts, ", ")
}

func pluralUnit(n int, unit string) string {
	if n == 1 {
		return unit
	}
	return unit + "s"
}
