package core

import (
	"fmt"
	"strconv"
	"strings"
)

// Flag describes one command line option.
type Flag struct {
	Name    string // canonical name used by the accessors
	Short   byte   // 0 = no short form
	Long    string // "" = no long form
	HasArg  bool
	OptArg  bool // GNU style optional argument: --preserve[=ATTR]
	ArgName string
	Help    string
	aliases []byte // extra short forms merged into the same option
}

// Flags is a small GNU-getopt style parser. It understands short option
// clustering (-rf), attached short values (-n5), long options (--lines=5),
// abbreviations-free long names and the "--" end-of-options marker, so that
// the option syntax the user knows from Linux keeps working in cmd.exe and
// PowerShell.
type Flags struct {
	specs      []*Flag
	byShort    map[byte]*Flag
	byLong     map[string]*Flag
	values     map[string][]string
	counts     map[string]int
	Positional []string
}

func NewFlags(specs ...*Flag) *Flags {
	f := &Flags{
		byShort: map[byte]*Flag{},
		byLong:  map[string]*Flag{},
		values:  map[string][]string{},
		counts:  map[string]int{},
	}
	for _, s := range specs {
		f.add(s)
	}
	f.add(&Flag{Name: "help", Long: "help", Help: "display this help and exit"})
	f.add(&Flag{Name: "version", Long: "version", Help: "output version information and exit"})
	return f
}

func (f *Flags) add(s *Flag) {
	for _, e := range f.specs {
		if e.Name == s.Name {
			if s.Short != 0 {
				if e.Short == 0 {
					e.Short = s.Short
				} else {
					e.aliases = append(e.aliases, s.Short)
				}
				f.byShort[s.Short] = e
			}
			if s.Long != "" {
				f.byLong[s.Long] = e
			}
			return
		}
	}
	f.specs = append(f.specs, s)
	if s.Short != 0 {
		f.byShort[s.Short] = s
	}
	if s.Long != "" {
		f.byLong[s.Long] = s
	}
}

func (f *Flags) Parse(args []string) error { return f.parse(args, false) }

// ParseUntilCommand parses options only up to the first non-option word and
// puts that word plus everything after it into Positional. This is GNU getopt's
// "+" prefix, which the tools that wrap another program use: `nohup tail -f
// app.log` must hand "-f app.log" to tail instead of rejecting -f as its own
// option. Full case below (Parse) keeps permuting, like GNU getopt without "+".
func (f *Flags) ParseUntilCommand(args []string) error { return f.parse(args, true) }

func (f *Flags) parse(args []string, untilCommand bool) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			f.Positional = append(f.Positional, args[i+1:]...)
			return nil
		case len(a) > 1 && a[0] == '-':
			var err error
			if strings.HasPrefix(a, "--") {
				i, err = f.parseLong(a[2:], args, i)
			} else {
				i, err = f.parseShorts(a[1:], args, i)
			}
			if err != nil {
				return err
			}
		default:
			if untilCommand {
				f.Positional = append(f.Positional, args[i:]...)
				return nil
			}
			f.Positional = append(f.Positional, a)
		}
	}
	return nil
}

func (f *Flags) parseLong(body string, args []string, i int) (int, error) {
	name, val, hasVal := body, "", false
	if eq := strings.IndexByte(body, '='); eq >= 0 {
		name, val, hasVal = body[:eq], body[eq+1:], true
	}
	fl := f.byLong[name]
	if fl == nil {
		return i, fmt.Errorf("unrecognized option '--%s'", name)
	}
	if fl.HasArg && !fl.OptArg {
		if !hasVal {
			i++
			if i >= len(args) {
				return i, fmt.Errorf("option '--%s' requires an argument", name)
			}
			val = args[i]
		}
		f.values[fl.Name] = append(f.values[fl.Name], val)
		return i, nil
	}
	if fl.OptArg {
		// an optional argument may only be attached with '=' (GNU behaviour)
		if hasVal {
			f.values[fl.Name] = append(f.values[fl.Name], val)
		}
		f.counts[fl.Name]++
		return i, nil
	}
	if hasVal {
		return i, fmt.Errorf("option '--%s' doesn't allow an argument", name)
	}
	f.counts[fl.Name]++
	return i, nil
}

func (f *Flags) parseShorts(body string, args []string, i int) (int, error) {
	for j := 0; j < len(body); j++ {
		ch := body[j]
		fl := f.byShort[ch]
		if fl == nil {
			return i, fmt.Errorf("invalid option -- '%c'", ch)
		}
		if fl.HasArg && !fl.OptArg {
			val := body[j+1:]
			if val == "" {
				i++
				if i >= len(args) {
					return i, fmt.Errorf("option requires an argument -- '%c'", ch)
				}
				val = args[i]
			}
			f.values[fl.Name] = append(f.values[fl.Name], val)
			return i, nil
		}
		f.counts[fl.Name]++
	}
	return i, nil
}

func (f *Flags) Has(name string) bool {
	return f.counts[name] > 0 || len(f.values[name]) > 0
}

func (f *Flags) Count(name string) int { return f.counts[name] }

func (f *Flags) Str(name string) string {
	v := f.values[name]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func (f *Flags) Strs(name string) []string { return f.values[name] }

func (f *Flags) Int(name string, def int) (int, error) {
	s := f.Str(name)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def, fmt.Errorf("invalid number '%s'", s)
	}
	return n, nil
}

// optString renders the option part of a synopsis line, e.g. "[-rfvn]".
func (f *Flags) shortCluster() string {
	var b strings.Builder
	for _, s := range f.specs {
		if s.Short != 0 && s.Name != "help" && s.Name != "version" {
			b.WriteByte(s.Short)
		}
	}
	return b.String()
}
