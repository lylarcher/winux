package core

import "testing"

func TestRuneWidth(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{'a', 1}, {'Z', 1}, {'0', 1}, {' ', 1}, {'\t', 1},
		{'.', 1}, {'-', 1}, {'@', 1},
		{'中', 2}, {'文', 2}, {'测', 2},
		{'한', 2},           // Hangul
		{'Ａ', 2},           // fullwidth form
		{'￥', 2},           // fullwidth yen
		{'😀', 2},           // emoji
		{'𠀀', 2},           // CJK ext B (0x20000)
		{'é', 1}, {'ñ', 1}, // Latin-1 supplement are single width
	}
	for _, c := range cases {
		if got := runewidth(c.r); got != c.want {
			t.Errorf("runewidth(%q) = %d, want %d", c.r, got, c.want)
		}
	}
}

func TestCellWidth(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"中文", 4},
		{"a中b", 4},
		{"中文abc", 7},
		{"😀!", 3},
	}
	for _, c := range cases {
		if got := cellWidth([]rune(c.s)); got != c.want {
			t.Errorf("cellWidth(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

func TestTruncToCells(t *testing.T) {
	cases := []struct {
		s     string
		cells int
		want  string
	}{
		{"", 10, ""},
		{"abcdef", 3, "abc"},
		{"abcdef", 100, "abcdef"},
		{"中文x", 3, "中"},    // 中 takes 2 cells, 文 would exceed 3
		{"中文", 4, "中文"},    // exactly fits
		{"中文ab", 5, "中文a"}, // 2+2+1
		{"abc", 2, "ab"},
		{"中文", 1, ""}, // first wide rune does not fit
		{"abc", 0, ""},
	}
	for _, c := range cases {
		got := string(truncToCells([]rune(c.s), c.cells))
		if got != c.want {
			t.Errorf("truncToCells(%q, %d) = %q, want %q", c.s, c.cells, got, c.want)
		}
	}
}

func TestDisplayLineCellTruncation(t *testing.T) {
	e := &Editor{Lines: [][]rune{[]rune("中文abc后面还有")}}
	// 5 cells: 中(2) 文(2) a(1) → the line must be cut at 5 cells,
	// i.e. "中文a", leaving no overflow past the right edge.
	if got := e.DisplayLine(0, 5); got != "中文a" {
		t.Errorf("DisplayLine with CJK: %q, want %q", got, "中文a")
	}
	// a wide enough window shows the whole line uncut
	if got := e.DisplayLine(0, 100); got != "中文abc后面还有" {
		t.Errorf("DisplayLine full width: %q", got)
	}
	// with a number prefix the text area shrinks by 6 cells: 9 total → 3 cells
	e.Number = true
	if got := e.DisplayLine(0, 9); got != "    1 中" {
		t.Errorf("DisplayLine with numbers: %q", got)
	}
}
