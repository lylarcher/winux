package core

// cal renders GNU-style calendars. The layout follows the coreutils cal
// conventions: 20-character month grids, Sunday or Monday first, an optional
// Julian day-of-year column, and three-column year or month sequences.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func init() {
	addCmd("cal", "display a calendar", cmdCal)
}

func cmdCal(args []string) int {
	f := NewFlags(
		&Flag{Name: "monday", Short: 'm', Long: "monday", Help: "display Monday as the first day of the week"},
		&Flag{Name: "year", Short: 'y', Long: "year", Help: "display a calendar for the current year"},
		&Flag{Name: "three", Short: '3', Long: "three", Help: "display the previous, current and next month"},
		&Flag{Name: "julian", Short: 'j', Long: "julian", Help: "display Julian day numbers (day of year)"},
		&Flag{Name: "after", Short: 'A', Long: "after", HasArg: true, ArgName: "NUM", Help: "display NUM months after the current month"},
		&Flag{Name: "before", Short: 'B', Long: "before", HasArg: true, ArgName: "NUM", Help: "display NUM months before the current month"},
	)
	if !parseOrDie("cal", f, args) {
		return 2
	}
	if handleHelpVersion("cal", "[OPTION]... [[[DAY] MONTH] YEAR]", f) {
		return 0
	}
	op := f.Positional
	now := time.Now()
	month, year := int(now.Month()), now.Year()
	yearOnly := false
	for _, name := range []string{"after", "before"} {
		if f.Has(name) {
			if _, err := f.Int(name, 0); err != nil {
				warn("cal", "invalid number of months '%s'", f.Str(name))
				return 1
			}
		}
	}
	switch len(op) {
	case 0:
	case 1:
		n, err := strconv.Atoi(op[0])
		if err != nil || n < 1 || n > 9999 {
			warn("cal", "invalid date '%s'", op[0])
			return 1
		}
		if n <= 12 {
			month = n
		} else {
			year, yearOnly = n, true
		}
	case 2:
		m, err1 := strconv.Atoi(op[0])
		y, err2 := strconv.Atoi(op[1])
		if err1 != nil || err2 != nil || m < 1 || m > 12 || y < 1 || y > 9999 {
			warn("cal", "invalid date '%s %s'", op[0], op[1])
			return 1
		}
		month, year = m, y
	default:
		warn("cal", "too many arguments")
		return 1
	}
	if f.Has("year") {
		year, yearOnly = now.Year(), true
	}
	if yearOnly && (f.Has("three") || intFlag(f, "after") > 0 || intFlag(f, "before") > 0) {
		warn("cal", "the year view cannot be combined with -3/-A/-B")
		return 1
	}

	monday := f.Has("monday")
	julian := f.Has("julian")

	switch {
	case yearOnly:
		for _, line := range calYear(year, monday, julian) {
			fmt.Println(line)
		}
	case f.Has("three"):
		py, pm := prevMonth(year, month)
		ny, nm := nextMonth(year, month)
		months := []struct{ y, m int }{{py, pm}, {year, month}, {ny, nm}}
		for _, line := range calColumns(months, monday, julian, true) {
			fmt.Println(line)
		}
	case intFlag(f, "before") > 0 || intFlag(f, "after") > 0:
		before, after := intFlag(f, "before"), intFlag(f, "after")
		var months []struct{ y, m int }
		cy, cm := year, month
		for i := 0; i < before; i++ {
			cy, cm = prevMonth(cy, cm)
			months = append([]struct{ y, m int }{{cy, cm}}, months...)
		}
		months = append(months, struct{ y, m int }{year, month})
		cy, cm = year, month
		for i := 0; i < after; i++ {
			cy, cm = nextMonth(cy, cm)
			months = append(months, struct{ y, m int }{cy, cm})
		}
		for _, line := range calColumns(months, monday, julian, true) {
			fmt.Println(line)
		}
	default:
		for _, line := range monthGrid(year, month, monday, julian, true) {
			fmt.Println(line)
		}
	}
	return 0
}

// intFlag reads a numeric flag, falling back to 0 on a missing or bad value.
func intFlag(f *Flags, name string) int {
	n, _ := f.Int(name, 0)
	return n
}

func prevMonth(y, m int) (int, int) {
	if m == 1 {
		return y - 1, 12
	}
	return y, m - 1
}

func nextMonth(y, m int) (int, int) {
	if m == 12 {
		return y + 1, 1
	}
	return y, m + 1
}

// monthGrid renders one month as lines: centered title, weekday header, and
// up to six week rows. withYear includes the year in the title (the year view
// shows only the month name, with the year printed once on top).
func monthGrid(year, month int, monday, julian, withYear bool) []string {
	first := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	days := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.Local).Day()
	dow := int(first.Weekday()) // 0 = Sunday
	if monday {
		dow = (dow + 6) % 7
	}
	title := time.Month(month).String()
	if withYear {
		title = fmt.Sprintf("%s %d", title, year)
	}
	pad := (20 - len(title)) / 2
	if pad < 0 {
		pad = 0
	}
	header := "Su Mo Tu We Th Fr Sa"
	if monday {
		header = "Mo Tu We Th Fr Sa Su"
	}
	lines := []string{strings.Repeat(" ", pad) + title, header}
	var row strings.Builder
	for i := 0; i < dow; i++ {
		row.WriteString("   ")
	}
	for d := 1; d <= days; d++ {
		n := d
		if julian {
			n = time.Date(year, time.Month(month), d, 0, 0, 0, 0, time.Local).YearDay()
		}
		fmt.Fprintf(&row, "%2d", n)
		if (dow+d)%7 == 0 || d == days {
			lines = append(lines, strings.TrimRight(row.String(), " "))
			row.Reset()
		} else {
			row.WriteByte(' ')
		}
	}
	return lines
}

// calYear renders the whole year: the year centered on top, then the twelve
// months as three columns of four rows with a two-space gutter.
func calYear(year int, monday, julian bool) []string {
	width := 3*20 + 2*2
	pad := (width - len(strconv.Itoa(year))) / 2
	out := []string{strings.Repeat(" ", pad) + strconv.Itoa(year), ""}
	for start := 1; start <= 12; start += 3 {
		var months []struct{ y, m int }
		for m := start; m < start+3; m++ {
			months = append(months, struct{ y, m int }{year, m})
		}
		out = append(out, calColumns(months, monday, julian, false)...)
	}
	return out
}

// calColumns lays out the given months side by side, padded to the tallest
// grid, each column 20 characters wide with a two-space gutter.
func calColumns(months []struct{ y, m int }, monday, julian, withYear bool) []string {
	grids := make([][]string, len(months))
	maxRows := 0
	for i, mo := range months {
		grids[i] = monthGrid(mo.y, mo.m, monday, julian, withYear)
		if len(grids[i]) > maxRows {
			maxRows = len(grids[i])
		}
	}
	var out []string
	for r := 0; r < maxRows; r++ {
		var b strings.Builder
		for i, g := range grids {
			if r < len(g) {
				b.WriteString(g[r])
			} else {
				b.WriteString(strings.Repeat(" ", 20))
			}
			if i < len(grids)-1 {
				b.WriteString("  ")
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}
