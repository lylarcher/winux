package core_test

import (
	"testing"
	"time"

	"winux/core"
)

func TestFlagParsing(t *testing.T) {
	f := core.NewFlags(
		&core.Flag{Name: "recursive", Short: 'r', Long: "recursive"},
		&core.Flag{Name: "recursive", Short: 'R'},
		&core.Flag{Name: "force", Short: 'f', Long: "force"},
		&core.Flag{Name: "verbose", Short: 'v', Long: "verbose"},
		&core.Flag{Name: "lines", Short: 'n', Long: "lines", HasArg: true},
		&core.Flag{Name: "target", Short: 't', Long: "target-directory", HasArg: true},
	)
	if err := f.Parse([]string{"-rfv", "a", "b"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"recursive", "force", "verbose"} {
		if !f.Has(name) {
			t.Errorf("expected -%s to be set", name)
		}
	}
	if len(f.Positional) != 2 {
		t.Fatalf("positional = %v", f.Positional)
	}
	if err := f.Parse([]string{"-n5", "--lines=7", "-t", "dir"}); err != nil {
		t.Fatal(err)
	}
	if got := f.Strs("lines"); len(got) != 2 || got[0] != "5" || got[1] != "7" {
		t.Fatalf("lines values = %v", got)
	}
	if f.Str("target") != "dir" {
		t.Fatalf("target = %q", f.Str("target"))
	}
	// -R is an alias for -r
	f2 := core.NewFlags(&core.Flag{Name: "recursive", Short: 'r'}, &core.Flag{Name: "recursive", Short: 'R'})
	if err := f2.Parse([]string{"-R"}); err != nil {
		t.Fatal(err)
	}
	if !f2.Has("recursive") {
		t.Fatal("-R should set recursive")
	}
	// "--" ends option parsing
	f3 := core.NewFlags(&core.Flag{Name: "force", Short: 'f'})
	if err := f3.Parse([]string{"--", "-f"}); err != nil {
		t.Fatal(err)
	}
	if f3.Has("force") || len(f3.Positional) != 1 || f3.Positional[0] != "-f" {
		t.Fatalf("-- handling broken: %v %v", f3.Has("force"), f3.Positional)
	}
	// unknown option is an error
	if err := f.Parse([]string{"--nope"}); err == nil {
		t.Fatal("expected an error for an unknown option")
	}
	// optional argument form: --preserve=all
	f4 := core.NewFlags(&core.Flag{Name: "preserve", Short: 'p', Long: "preserve", OptArg: true})
	if err := f4.Parse([]string{"--preserve=all", "-p"}); err != nil {
		t.Fatal(err)
	}
	if !f4.Has("preserve") || f4.Count("preserve") != 2 || f4.Str("preserve") != "all" {
		t.Fatalf("optarg flag: has=%v count=%d value=%q", f4.Has("preserve"), f4.Count("preserve"), f4.Str("preserve"))
	}
	// an optional argument is only accepted with '='
	f5 := core.NewFlags(&core.Flag{Name: "preserve", Short: 'p', Long: "preserve", OptArg: true})
	if err := f5.Parse([]string{"--preserve", "all"}); err != nil {
		t.Fatal(err)
	}
	if len(f5.Positional) != 1 || f5.Positional[0] != "all" {
		t.Fatalf("bare --preserve should not swallow the next argument: %v", f5.Positional)
	}
}

func TestWildcardMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "main.txt", false},
		{"main.?", "main.c", true},
		{"[a-c]x", "bx", true},
		{"[a-c]x", "dx", false},
		{"[!a-c]x", "dx", true},
		{"src/*.go", `src\main.go`, true},
		{"**/*.go", "a/b/c.go", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"文档*.txt", "文档1.txt", true},
	}
	for _, c := range cases {
		if got := core.WildcardMatch(c.pattern, c.name); got != c.want {
			t.Errorf("WildcardMatch(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestParseListSpec(t *testing.T) {
	r, err := core.ParseListSpec("1,3-5,8-")
	if err != nil {
		t.Fatal(err)
	}
	in := func(i int) bool { return core.RangeSelected(r, i) }
	for _, i := range []int{1, 3, 4, 5, 8, 9, 100} {
		if !in(i) {
			t.Errorf("field %d should be selected", i)
		}
	}
	for _, i := range []int{2, 6, 7} {
		if in(i) {
			t.Errorf("field %d should not be selected", i)
		}
	}
	if _, err := core.ParseListSpec("5-2"); err == nil {
		t.Error("expected an error for a decreasing range")
	}
	if _, err := core.ParseListSpec("x"); err == nil {
		t.Error("expected an error for a non numeric list")
	}
}

func TestParseDateAndStrftime(t *testing.T) {
	for _, s := range []string{"2024-01-02", "2024-01-02 03:04:05", "2024/01/02", "now", "yesterday", "1700000000"} {
		if _, err := core.ParseDate(s); err != nil {
			t.Errorf("ParseDate(%q) failed: %v", s, err)
		}
	}
	if _, err := core.ParseDate("not a date"); err == nil {
		t.Error("ParseDate should reject garbage")
	}
	when := time.Date(2024, 2, 3, 14, 5, 6, 0, time.Local)
	got := core.Strftime(when, "%Y-%m-%d %H:%M:%S")
	if got != "2024-02-03 14:05:06" {
		t.Fatalf("Strftime = %q", got)
	}
	if got := core.Strftime(when, "%F %T"); got != "2024-02-03 14:05:06" {
		t.Fatalf("Strftime %%F %%T = %q", got)
	}
	if got := core.Strftime(when, "%a %b %e"); got != "Sat Feb  3" {
		t.Fatalf("Strftime = %q", got)
	}
}
