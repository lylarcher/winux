package core

// clickhouse is a pure-Go ClickHouse client speaking the HTTP interface
// (default port 8123), so no clickhouse-cli install is needed. It mirrors the
// clickhouse-client option surface for the common cases: --host/--port/--user/
// --password/--database/--query/--format/--multiline/--timeout/--secure.
//
//	clickhouse -q "SELECT 1"
//	echo "SELECT version()" | clickhouse
//	clickhouse client -h 10.0.0.5 --password secret   # interactive REPL
//
// Queries are POSTed with the X-ClickHouse-User / X-ClickHouse-Key headers and
// the database + default_format URL parameters; the server does the
// formatting (PrettyCompact on a terminal, TabSeparated otherwise).

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func init() {
	addCmd("clickhouse", "ClickHouse client over HTTP (no clickhouse-cli needed)", cmdClickhouse)
}

type chOpts struct {
	scheme   string
	host     string
	port     int
	user     string
	password string
	database string
	format   string
	timeout  time.Duration
	client   *http.Client
}

func cmdClickhouse(args []string) int {
	// --password accepts an attached (=SECRET) or a following value, and a bare
	// --password prompts (hidden) when the session is interactive — the same
	// convention as mysql and clickhouse-client.
	var rest []string
	password := ""
	promptPassword := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--password":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				password = args[i+1]
				i++
			} else {
				promptPassword = true
			}
		case strings.HasPrefix(a, "--password="):
			password = strings.TrimPrefix(a, "--password=")
		default:
			rest = append(rest, a)
		}
	}

	f := NewFlags(
		&Flag{Name: "host", Short: 'h', Long: "host", HasArg: true, ArgName: "HOST", Help: "server host (default: localhost)"},
		&Flag{Name: "port", Long: "port", HasArg: true, ArgName: "PORT", Help: "HTTP port (default: 8123; 8443 with --secure)"},
		&Flag{Name: "user", Short: 'u', Long: "user", HasArg: true, ArgName: "USER", Help: "user name (default: default)"},
		&Flag{Name: "database", Short: 'd', Long: "database", HasArg: true, ArgName: "DB", Help: "database (default: default)"},
		&Flag{Name: "query", Short: 'q', Long: "query", HasArg: true, ArgName: "SQL", Help: "run SQL non-interactively (repeatable)"},
		&Flag{Name: "execute", Short: 'e', Long: "execute", HasArg: true, ArgName: "SQL", Help: "alias of --query"},
		&Flag{Name: "format", Short: 'f', Long: "format", HasArg: true, ArgName: "FMT", Help: "output format, e.g. Pretty, TSV, CSV, JSON (default: PrettyCompact on a terminal, TabSeparated otherwise)"},
		&Flag{Name: "multiline", Short: 'm', Long: "multiline", Help: "REPL: keep reading until ';' (default: one query per Enter)"},
		&Flag{Name: "secure", Long: "secure", Help: "use HTTPS"},
		&Flag{Name: "timeout", Long: "timeout", HasArg: true, ArgName: "SECS", Help: "request timeout in seconds (default: 30)"},
	)
	if !parseOrDie("clickhouse", f, rest) {
		return 2
	}
	if handleHelpVersion("clickhouse", "client [ OPTIONS ]", f) {
		return 0
	}

	pos := f.Positional
	if len(pos) > 0 && pos[0] == "client" { // clickhouse-cli invocation style
		pos = pos[1:]
	}
	if len(pos) > 0 {
		warn("clickhouse", "unexpected argument '%s' (use --query / -q or pipe SQL on stdin)", pos[0])
		return 1
	}

	o := &chOpts{
		scheme:   "http",
		host:     "localhost",
		port:     8123,
		user:     "default",
		database: "default",
		timeout:  30 * time.Second,
	}
	if f.Has("host") {
		o.host = f.Str("host")
	}
	if f.Has("port") {
		n, err := f.Int("port", 8123)
		if err != nil || n < 1 || n > 65535 {
			warn("clickhouse", "invalid port '%s'", f.Str("port"))
			return 1
		}
		o.port = n
	}
	if f.Has("user") {
		o.user = f.Str("user")
	}
	if f.Has("database") {
		o.database = f.Str("database")
	}
	if f.Has("secure") {
		o.scheme = "https"
		if o.port == 8123 {
			o.port = 8443
		}
	}
	if f.Has("timeout") {
		n, err := f.Int("timeout", 30)
		if err != nil || n < 0 {
			warn("clickhouse", "invalid timeout '%s'", f.Str("timeout"))
			return 1
		}
		o.timeout = time.Duration(n) * time.Second
	}
	if promptPassword && isTerminal(os.Stdin) {
		line, hidden := readPassword("Enter password: ")
		password = line
		if !hidden {
			fmt.Fprintln(os.Stderr)
		}
	}
	o.password = password

	if format := f.Str("format"); format != "" {
		o.format = format
	} else if isTerminal(os.Stdout) {
		o.format = "PrettyCompact"
	} else {
		o.format = "TabSeparated"
	}
	o.client = &http.Client{Timeout: o.timeout}

	// --query / --execute (repeatable)
	queries := f.Strs("query")
	queries = append(queries, f.Strs("execute")...)
	if len(queries) > 0 {
		return chRunStatements(o, strings.Join(queries, "; "))
	}

	// piped SQL on stdin when not a console
	if !isTerminal(os.Stdin) {
		data, err := io.ReadAll(stripBOM(os.Stdin))
		if err != nil {
			warn("clickhouse", "%v", err)
			return 1
		}
		return chRunStatements(o, string(data))
	}

	return chRepl(o, f.Has("multiline"))
}

// chRunStatements splits SQL on statement boundaries (quote-aware, like mysql)
// and executes each one, reporting errors on stderr and keeping the first
// failure's exit code.
func chRunStatements(o *chOpts, sql string) int {
	stmts := splitExecStatements(sql)
	if len(stmts) == 0 {
		return 0
	}
	exitCode := 0
	for _, st := range stmts {
		if err := chExecOne(o, st.text, false); err != nil {
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}
	return exitCode
}

// chExecOne sends one statement and writes the server's formatted reply to
// stdout; interactive mode prints "Ok." for statements with no output.
func chExecOne(o *chOpts, sql string, interactive bool) error {
	body, err := o.query(sql)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clickhouse: %v\n", err)
		return err
	}
	if len(body) > 0 {
		os.Stdout.Write(body)
		if len(body) > 0 && body[len(body)-1] != '\n' {
			fmt.Fprintln(os.Stdout)
		}
	} else if interactive {
		fmt.Fprintln(os.Stdout, "Ok.")
	}
	return nil
}

// query POSTs one SQL statement to the ClickHouse HTTP endpoint.
func (o *chOpts) query(sql string) ([]byte, error) {
	url := fmt.Sprintf("%s://%s:%d/", o.scheme, o.host, o.port)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(sql))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ClickHouse-User", o.user)
	if o.password != "" {
		req.Header.Set("X-ClickHouse-Key", o.password)
	}
	q := req.URL.Query()
	q.Set("database", o.database)
	q.Set("default_format", o.format)
	req.URL.RawQuery = q.Encode()

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		if code := resp.Header.Get("X-ClickHouse-Exception-Code"); code != "" {
			return nil, fmt.Errorf("%s (code %s): %s", resp.Status, code, msg)
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, msg)
	}
	return body, nil
}

// chRepl is the interactive REPL: a query per Enter by default, or
// accumulate-until-';' with --multiline. Same conventions as mysql.
func chRepl(o *chOpts, multiline bool) int {
	fmt.Fprintf(os.Stderr, "winux clickhouse %s (pure Go HTTP client) — end queries with ';' or just press Enter in single-line mode\n", Version)
	in, cleanup := consoleInput()
	defer cleanup()
	rd := bufio.NewReader(stripBOM(in))
	exitCode := 0
	var buf strings.Builder
	for {
		if buf.Len() == 0 {
			fmt.Fprintf(os.Stdout, "%s:%d> ", o.host, o.port)
		} else {
			fmt.Fprint(os.Stdout, "    -> ")
		}
		line, err := rd.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(line)
			if buf.Len() == 0 {
				switch {
				case strings.EqualFold(trimmed, "exit"), strings.EqualFold(trimmed, "quit"), trimmed == `\q`:
					fmt.Fprintln(os.Stdout)
					return exitCode
				case trimmed == `\c`:
					continue
				case strings.EqualFold(trimmed, "help"), strings.EqualFold(trimmed, `\h`):
					fmt.Fprintln(os.Stdout, "clickhouse help: type SQL and press Enter (single-line mode) or end with ';' (-m multiline); \\q or exit to quit")
					continue
				}
			}
			if trimmed == "" && buf.Len() == 0 {
				continue
			}
			buf.WriteString(line)
			full := strings.TrimSpace(buf.String())
			if multiline {
				if !strings.HasSuffix(full, ";") {
					continue
				}
				full = strings.TrimSpace(strings.TrimSuffix(full, ";"))
			} else {
				full = strings.TrimSuffix(full, ";")
				buf.Reset()
			}
			buf.Reset()
			if full != "" {
				if err := chExecOne(o, full, true); err != nil && exitCode == 0 {
					exitCode = 1
				}
			}
		}
		if err == io.EOF {
			fmt.Fprintln(os.Stdout)
			return exitCode
		}
	}
}
