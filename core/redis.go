package core

// A pure-Go redis-cli: speaks the RESP2 wire protocol directly (net + the
// standard library), so no Redis client installation is needed. Supports
// authentication, database selection, non-interactive commands with -r/-x,
// and an interactive REPL with redis-cli style reply formatting.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func init() {
	addCmd("redis-cli", "Redis client (pure Go, no client install)", cmdRedisCli)
}

// redisReply kinds
const (
	replySimple = iota
	replyError
	replyInt
	replyBulk
	replyArray
	replyNilBulk
	replyNilArray
)

type redisReply struct {
	kind  int
	str   string
	num   int64
	items []*redisReply
}

func parseRedisReply(r *bufio.Reader) (*redisReply, error) {
	line, err := readLineCRLF(r)
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, fmt.Errorf("empty reply line")
	}
	switch line[0] {
	case '+':
		return &redisReply{kind: replySimple, str: string(line[1:])}, nil
	case '-':
		return &redisReply{kind: replyError, str: string(line[1:])}, nil
	case ':':
		n, err := strconv.ParseInt(string(line[1:]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad integer reply %q", line)
		}
		return &redisReply{kind: replyInt, num: n}, nil
	case '$':
		n, err := strconv.Atoi(string(line[1:]))
		if err != nil {
			return nil, fmt.Errorf("bad bulk length %q", line)
		}
		if n < 0 {
			return &redisReply{kind: replyNilBulk}, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return &redisReply{kind: replyBulk, str: string(buf[:n])}, nil
	case '*':
		n, err := strconv.Atoi(string(line[1:]))
		if err != nil {
			return nil, fmt.Errorf("bad array length %q", line)
		}
		if n < 0 {
			return &redisReply{kind: replyNilArray}, nil
		}
		rep := &redisReply{kind: replyArray}
		for i := 0; i < n; i++ {
			item, err := parseRedisReply(r)
			if err != nil {
				return nil, err
			}
			rep.items = append(rep.items, item)
		}
		return rep, nil
	default:
		return nil, fmt.Errorf("unexpected reply byte %q", line[0])
	}
}

func readLineCRLF(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func writeRedisCommand(w io.Writer, args []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// splitArgs splits a command line like redis-cli does: whitespace separated,
// double quotes group words, \" and \\ unescape inside quotes.
func splitArgs(line string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inQuote := false
	esc := false
	started := false
	flush := func() {
		if started || cur.Len() > 0 {
			args = append(args, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range line {
		if esc {
			if inQuote {
				cur.WriteRune(r)
			} else {
				cur.WriteRune(r)
			}
			esc = false
			started = true
			continue
		}
		switch {
		case r == '\\':
			esc = true
			started = true
		case r == '"':
			inQuote = !inQuote
			started = true
		case r == ' ' || r == '\t':
			if inQuote {
				cur.WriteRune(r)
			} else {
				flush()
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unbalanced quotes in: %s", line)
	}
	if esc {
		cur.WriteRune('\\')
		started = true
	}
	flush()
	return args, nil
}

// ------------------------------------------------------------------ main ----

// splitRedisArgs separates the option prefix from the command: everything from
// the first non-option token on is the command verbatim (so negative numbers
// like `-1` reach the server). Options that take a value consume theirs.
func splitRedisArgs(args []string) (opts, cmd []string) {
	valueOpts := map[string]bool{
		"host": true, "port": true, "auth": true, "db": true,
		"repeat": true, "interval": true, "delimiter": true, "timeout": true,
	}
	shortValue := func(ch byte) bool {
		switch ch {
		case 'h', 'p', 'a', 'n', 'r', 'i', 'd':
			return true
		}
		return false
	}
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			return opts, args[i+1:]
		}
		if len(a) > 1 && a[0] == '-' {
			opts = append(opts, a)
			if strings.HasPrefix(a, "--") {
				name := a[2:]
				if eq := strings.IndexByte(name, '='); eq >= 0 {
					name = name[:eq]
				}
				if valueOpts[name] && !strings.Contains(a, "=") && i+1 < len(args) {
					i++
					opts = append(opts, args[i])
				}
			} else if len(a) == 2 && shortValue(a[1]) && i+1 < len(args) {
				i++
				opts = append(opts, args[i])
			}
			i++
			continue
		}
		return opts, args[i:]
	}
	return opts, nil
}

func cmdRedisCli(args []string) int {
	opts, cmd := splitRedisArgs(args)
	f := NewFlags(
		&Flag{Name: "host", Short: 'h', Long: "host", HasArg: true, ArgName: "HOST", Help: "server host (default 127.0.0.1)"},
		&Flag{Name: "port", Short: 'p', Long: "port", HasArg: true, ArgName: "PORT", Help: "server port (default 6379)"},
		&Flag{Name: "auth", Short: 'a', Long: "auth", HasArg: true, ArgName: "PASSWORD", Help: "password to use when connecting"},
		&Flag{Name: "db", Short: 'n', Long: "db", HasArg: true, ArgName: "DB", Help: "database number"},
		&Flag{Name: "repeat", Short: 'r', Long: "repeat", HasArg: true, ArgName: "COUNT", Help: "repeat the command COUNT times"},
		&Flag{Name: "interval", Short: 'i', Long: "interval", HasArg: true, ArgName: "SECONDS", Help: "delay between repeats (seconds, may be fractional)"},
		&Flag{Name: "delimiter", Short: 'd', Long: "delimiter", HasArg: true, ArgName: "DELIM", Help: "delimiter between array elements in non-interactive mode (default \\n)"},
		&Flag{Name: "stdin", Short: 'x', Long: "stdin", Help: "read the last argument from stdin"},
		&Flag{Name: "raw", Long: "raw", Help: "print bulk strings without quotes"},
		&Flag{Name: "exit", Short: 'e', Long: "exit", Help: "exit on error"},
		&Flag{Name: "no-auth-warning", Long: "no-auth-warning", Help: "do not warn when -a is used"},
		&Flag{Name: "timeout", Long: "timeout", HasArg: true, ArgName: "SECONDS", Help: "connection timeout (default 10)"},
	)
	if !parseOrDie("redis-cli", f, opts) {
		return 2
	}
	f.Positional = cmd
	if handleHelpVersion("redis-cli", "[OPTION]... [COMMAND [ARG]...]", f) {
		return 0
	}
	host := f.Str("host")
	if host == "" {
		host = "127.0.0.1"
	}
	port, err := f.Int("port", 6379)
	if err != nil {
		warn("redis-cli", "%v", err)
		return 2
	}
	timeout := 10 * time.Second
	if t, e := f.Int("timeout", 0); e == nil && t > 0 {
		timeout = time.Duration(t) * time.Second
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "redis-cli: %v\n", err)
		return 1
	}
	defer conn.Close()
	rd := bufio.NewReader(conn)

	auth := f.Str("auth")
	if auth != "" {
		if err := sendAndCheck(rd, conn, []string{"AUTH", auth}); err != nil {
			fmt.Fprintf(os.Stderr, "redis-cli: %v\n", err)
			return 1
		}
	}
	if db, e := f.Int("db", 0); e == nil && db != 0 {
		if err := sendAndCheck(rd, conn, []string{"SELECT", strconv.Itoa(db)}); err != nil {
			fmt.Fprintf(os.Stderr, "redis-cli: %v\n", err)
			return 1
		}
	}

	repeat, err := f.Int("repeat", 1)
	if err != nil {
		warn("redis-cli", "%v", err)
		return 2
	}
	interval := 0.0
	if s := f.Str("interval"); s != "" {
		interval, err = strconv.ParseFloat(s, 64)
		if err != nil {
			warn("redis-cli", "invalid interval '%s'", s)
			return 2
		}
	}

	// non-interactive: positional args are the command
	if len(f.Positional) > 0 {
		cmd := f.Positional
		if f.Has("stdin") {
			data, err := io.ReadAll(stripBOM(os.Stdin))
			if err != nil {
				fmt.Fprintf(os.Stderr, "redis-cli: %v\n", err)
				return 1
			}
			cmd = append(cmd, strings.TrimRight(string(data), "\r\n"))
		}
		exitCode := 0
		for i := 0; i < repeat; i++ {
			rep, err := runOne(rd, conn, cmd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "redis-cli: %v\n", err)
				return 1
			}
			if rep.kind == replyError {
				fmt.Fprintf(os.Stderr, "(error) %s\n", rep.str)
				exitCode = 1
			} else {
				printReplyNonInteractive(rep, f.Str("delimiter"))
			}
			if interval > 0 && i < repeat-1 {
				time.Sleep(time.Duration(interval * float64(time.Second)))
			}
		}
		return exitCode
	}

	// interactive REPL
	warned := false
	fmt.Fprintf(os.Stderr, "winux redis-cli %s (pure Go client)\n", Version)
	if auth != "" && !f.Has("no-auth-warning") {
		fmt.Fprintln(os.Stderr, "Warning: Using a password with '-a' (or '-u URL') on the command line may not be safe.")
	}
	in, cleanup := consoleInput()
	defer cleanup()
	rdIn := bufio.NewReader(in)
	for {
		conn.SetDeadline(time.Now().Add(5 * time.Minute))
		fmt.Fprintf(os.Stdout, "%s:%d> ", host, port)
		line, err := rdIn.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(line)
			if strings.EqualFold(trimmed, "quit") || strings.EqualFold(trimmed, "exit") {
				fmt.Fprintln(os.Stdout)
				return 0
			}
			cmd, err := splitArgs(trimmed)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				continue
			}
			if len(cmd) == 0 {
				continue
			}
			rep, rerr := runOne(rd, conn, cmd)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "redis-cli: %v\n", rerr)
				continue
			}
			if rep.kind == replyError && !warned && auth != "" && strings.HasPrefix(rep.str, "NOAUTH") {
				fmt.Fprintf(os.Stderr, "redis-cli: warning: no password given or wrong password; use -a PASSWORD\n")
				warned = true
			}
			printReplyInteractive(rep, 0, f.Has("raw"))
		}
		if err == io.EOF {
			fmt.Fprintln(os.Stdout)
			return 0
		}
	}
}

func sendAndCheck(rd *bufio.Reader, conn net.Conn, args []string) error {
	rep, err := runOne(rd, conn, args)
	if err != nil {
		return err
	}
	if rep.kind == replyError {
		return fmt.Errorf("%s", rep.str)
	}
	return nil
}

func runOne(rd *bufio.Reader, conn net.Conn, args []string) (*redisReply, error) {
	if err := writeRedisCommand(conn, args); err != nil {
		return nil, err
	}
	return parseRedisReply(rd)
}

func printReplyNonInteractive(rep *redisReply, delim string) {
	if delim == "" {
		delim = "\n"
	}
	switch rep.kind {
	case replySimple, replyBulk:
		fmt.Println(rep.str)
	case replyInt:
		fmt.Println(rep.num)
	case replyNilBulk, replyNilArray:
		fmt.Println()
	case replyArray:
		parts := make([]string, 0, len(rep.items))
		for _, it := range rep.items {
			parts = append(parts, replyToString(it))
		}
		fmt.Println(strings.Join(parts, delim))
	}
}

func replyToString(rep *redisReply) string {
	switch rep.kind {
	case replySimple, replyBulk:
		return rep.str
	case replyInt:
		return strconv.FormatInt(rep.num, 10)
	case replyNilBulk, replyNilArray:
		return ""
	case replyArray:
		parts := make([]string, 0, len(rep.items))
		for _, it := range rep.items {
			parts = append(parts, replyToString(it))
		}
		return strings.Join(parts, ",")
	}
	return ""
}

func printReplyInteractive(rep *redisReply, depth int, raw bool) {
	indent := strings.Repeat(" ", depth)
	switch rep.kind {
	case replySimple:
		fmt.Println(rep.str)
	case replyError:
		fmt.Fprintf(os.Stderr, "%s(error) %s\n", indent, rep.str)
	case replyInt:
		fmt.Fprintf(os.Stdout, "%s(integer) %d\n", indent, rep.num)
	case replyBulk:
		if raw {
			fmt.Fprintln(os.Stdout, rep.str)
		} else {
			fmt.Fprintf(os.Stdout, "%s\"%s\"\n", indent, rep.str)
		}
	case replyNilBulk:
		fmt.Fprintf(os.Stdout, "%s(nil)\n", indent)
	case replyNilArray:
		fmt.Fprintf(os.Stdout, "%s(nil)\n", indent)
	case replyArray:
		if len(rep.items) == 0 {
			fmt.Fprintf(os.Stdout, "%s(empty array)\n", indent)
			return
		}
		for i, it := range rep.items {
			prefix := fmt.Sprintf("%s%d) ", indent, i+1)
			switch it.kind {
			case replyArray, replyNilArray:
				if it.kind == replyNilArray {
					fmt.Fprintf(os.Stdout, "%s(nil)\n", prefix)
					continue
				}
				fmt.Fprintln(os.Stdout, prefix)
				printReplyInteractive(it, depth+1, raw)
			default:
				fmt.Fprintf(os.Stdout, "%s", prefix)
				switch it.kind {
				case replyBulk:
					if raw {
						fmt.Fprintln(os.Stdout, it.str)
					} else {
						fmt.Fprintf(os.Stdout, "\"%s\"\n", it.str)
					}
				case replyInt:
					fmt.Fprintf(os.Stdout, "(integer) %d\n", it.num)
				case replyNilBulk:
					fmt.Fprintln(os.Stdout, "(nil)")
				default:
					fmt.Fprintln(os.Stdout, it.str)
				}
			}
		}
	}
}
