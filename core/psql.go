package core

// psql is a pure-Go PostgreSQL client speaking the v3 wire protocol directly
// (no psql / PostgreSQL install needed): startup handshake, cleartext / MD5 /
// SCRAM-SHA-256 authentication and the simple query protocol, all on the
// standard library.
//
//	psql -U postgres -d mydb -c "SELECT 1"
//	echo "SELECT version()" | psql -U postgres
//	psql -U postgres -W mydb    # interactive REPL (db=> prompt)
//
// The REPL supports \q, \l (databases), \dt (tables), \d NAME (columns),
// \c DB (reconnect) and \? help. Output is a psql-style aligned table unless
// -t (tuples only) or -A (unaligned) is given.

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

func init() {
	addCmd("psql", "PostgreSQL client over the wire protocol (no psql needed)", cmdPsql)
}

const pgProtocolVersion = 196608 // 3.0

type pgConn struct {
	conn    net.Conn
	timeout time.Duration
}

// ---------------------------------------------------------------------------
// Message I/O

func (c *pgConn) sendRaw(payload []byte) error {
	if c.timeout > 0 {
		c.conn.SetDeadline(time.Now().Add(c.timeout))
	}
	_, err := c.conn.Write(payload)
	return err
}

func (c *pgConn) send(typ byte, payload []byte) error {
	var msg bytes.Buffer
	msg.WriteByte(typ)
	binary.Write(&msg, binary.BigEndian, int32(len(payload)+4))
	msg.Write(payload)
	return c.sendRaw(msg.Bytes())
}

func (c *pgConn) sendStartup(params map[string]string) error {
	var msg bytes.Buffer
	var body bytes.Buffer
	binary.Write(&body, binary.BigEndian, int32(pgProtocolVersion))
	for k, v := range params {
		body.WriteString(k)
		body.WriteByte(0)
		body.WriteString(v)
		body.WriteByte(0)
	}
	body.WriteByte(0)
	binary.Write(&msg, binary.BigEndian, int32(body.Len()+4))
	msg.Write(body.Bytes())
	return c.sendRaw(msg.Bytes())
}

// readMsg reads one backend message, returning its type and payload.
func (c *pgConn) readMsg() (byte, []byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(c.conn, hdr); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(hdr[1:]))
	if n < 4 || n > 64*1024*1024 {
		return 0, nil, fmt.Errorf("invalid message length %d", n)
	}
	payload := make([]byte, n-4)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

// ---------------------------------------------------------------------------
// Startup + authentication

func pgDial(host string, port int, timeout time.Duration, useSSL bool) (*pgConn, error) {
	addr := net.JoinHostPort(host, itoa(port))
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	pc := &pgConn{conn: conn, timeout: timeout}
	if useSSL {
		if err := pc.sslRequest(); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return pc, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// sslRequest offers SSL and upgrades the connection when the server agrees.
func (c *pgConn) sslRequest() error {
	var msg bytes.Buffer
	binary.Write(&msg, binary.BigEndian, int32(8))
	binary.Write(&msg, binary.BigEndian, int32(80877103))
	if err := c.sendRaw(msg.Bytes()); err != nil {
		return err
	}
	resp := make([]byte, 1)
	if _, err := io.ReadFull(c.conn, resp); err != nil {
		return err
	}
	switch resp[0] {
	case 'S':
		tlsConn := tls.Client(c.conn, &tls.Config{InsecureSkipVerify: true})
		if err := tlsConn.Handshake(); err != nil {
			return fmt.Errorf("TLS handshake failed: %v", err)
		}
		c.conn = tlsConn
		return nil
	case 'N':
		return nil // server declined SSL; continue in plaintext
	default:
		return fmt.Errorf("unexpected SSL negotiation response %q", resp[0])
	}
}

// startup performs the startup handshake including authentication; password
// is fetched lazily via the getPassword callback (cleartext / MD5 / SCRAM).
func (c *pgConn) startup(user, db string, getPassword func() (string, error)) error {
	params := map[string]string{
		"user":             user,
		"database":         db,
		"application_name": "winux",
		"client_encoding":  "UTF8",
	}
	if err := c.sendStartup(params); err != nil {
		return err
	}
	var scram *scramClient
	password := ""
	for {
		typ, payload, err := c.readMsg()
		if err != nil {
			return err
		}
		switch typ {
		case 'R':
			code := binary.BigEndian.Uint32(payload[0:4])
			switch code {
			case 0: // AuthenticationOk — keep reading until ReadyForQuery
			case 3: // cleartext password
				if password == "" {
					if password, err = getPassword(); err != nil {
						return err
					}
				}
				if err := c.send('p', append([]byte(password), 0)); err != nil {
					return err
				}
			case 5: // MD5 password
				if password == "" {
					if password, err = getPassword(); err != nil {
						return err
					}
				}
				if err := c.send('p', append([]byte(pgMD5Password(user, password, payload[4:8])), 0)); err != nil {
					return err
				}
			case 10: // SASL
				mechs := strings.Split(strings.TrimRight(string(payload[4:]), "\x00"), "\x00")
				ok := false
				for _, m := range mechs {
					if m == "SCRAM-SHA-256" {
						ok = true
						break
					}
				}
				if !ok {
					return fmt.Errorf("server offers no SCRAM-SHA-256 (got %v)", mechs)
				}
				if password == "" {
					if password, err = getPassword(); err != nil {
						return err
					}
				}
				if scram, err = newScramClient(user, password); err != nil {
					return err
				}
				cf := []byte("n,," + scram.clientFirstBare)
				var init bytes.Buffer
				init.WriteString("SCRAM-SHA-256")
				init.WriteByte(0)
				binary.Write(&init, binary.BigEndian, int32(len(cf)))
				init.Write(cf)
				if err := c.send('p', init.Bytes()); err != nil {
					return err
				}
			case 11: // SASL continue (server-first)
				next, _, err := scram.step(string(payload[4:]))
				if err != nil {
					return err
				}
				if err := c.send('p', []byte(next)); err != nil {
					return err
				}
			case 12: // SASL final (server-final "v=...")
				if _, _, err := scram.step(string(payload[4:])); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported authentication request %d", code)
			}
		case 'S', 'K', 'N':
			// ParameterStatus / BackendKeyData / NoticeResponse
		case 'Z':
			return nil
		case 'E':
			return pgError(payload)
		default:
			return fmt.Errorf("unexpected message %q during startup", typ)
		}
	}
}

func pgMD5Password(user, password string, salt []byte) string {
	h := md5.Sum([]byte(password + user))
	inner := hex.EncodeToString(h[:])
	h2 := md5.Sum(append([]byte(inner), salt...))
	return "md5" + hex.EncodeToString(h2[:])
}

func pgError(payload []byte) error {
	// ErrorResponse: fields like 'S'severity 'C'code 'M'message, terminated by \0
	msg := ""
	severity := ""
	rest := payload
	for len(rest) > 0 {
		f := rest[0]
		rest = rest[1:]
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			break
		}
		val := string(rest[:i])
		rest = rest[i+1:]
		switch f {
		case 'M':
			msg = val
		case 'S':
			severity = val
		case 'C':
			if msg == "" {
				msg = val
			}
		}
	}
	if msg == "" {
		msg = "server error"
	}
	if severity != "" {
		return fmt.Errorf("%s: %s", severity, msg)
	}
	return fmt.Errorf("%s", msg)
}

// ---------------------------------------------------------------------------
// Simple query protocol

type pgResult struct {
	cols []string
	rows [][]string
	tag  string
}

// simpleQuery runs one SQL text and collects every result set until
// ReadyForQuery. On an ErrorResponse it returns the partial results plus the
// error.
func (c *pgConn) simpleQuery(sql string) ([]pgResult, error) {
	if err := c.send('Q', append([]byte(sql), 0)); err != nil {
		return nil, err
	}
	var results []pgResult
	var cur *pgResult
	var qerr error
	for {
		typ, payload, err := c.readMsg()
		if err != nil {
			return results, err
		}
		switch typ {
		case 'T': // RowDescription
			cols, off := pgParseCols(payload)
			cur = &pgResult{cols: cols}
			_ = off
		case 'D': // DataRow
			if cur == nil {
				continue
			}
			n := int(binary.BigEndian.Uint16(payload[0:2]))
			row := make([]string, 0, n)
			pos := 2
			for i := 0; i < n; i++ {
				l := int(binary.BigEndian.Uint32(payload[pos:]))
				pos += 4
				if l < 0 {
					row = append(row, "")
					continue
				}
				row = append(row, string(payload[pos:pos+l]))
				pos += l
			}
			cur.rows = append(cur.rows, row)
		case 'C': // CommandComplete
			if cur != nil {
				cur.tag = strings.TrimRight(string(payload), "\x00")
				results = append(results, *cur)
				cur = nil
			} else {
				results = append(results, pgResult{tag: strings.TrimRight(string(payload), "\x00")})
			}
		case 'I': // EmptyQueryResponse
		case 'N': // NoticeResponse
		case 'E':
			if qerr == nil {
				qerr = pgError(payload)
			}
		case 'Z': // ReadyForQuery
			return results, qerr
		default:
			return results, fmt.Errorf("unexpected message %q during query", typ)
		}
	}
}

func pgParseCols(payload []byte) ([]string, int) {
	n := int(binary.BigEndian.Uint16(payload[0:2]))
	cols := make([]string, 0, n)
	pos := 2
	for i := 0; i < n; i++ {
		// name cstring
		j := bytes.IndexByte(payload[pos:], 0)
		cols = append(cols, string(payload[pos:pos+j]))
		pos += j + 1
		pos += 18 // tableOID(4) attnum(2) typeOID(4) typlen(2) typmod(4) format(2)
	}
	return cols, pos
}

// ---------------------------------------------------------------------------
// Command entry point

func cmdPsql(args []string) int {
	// -W / --password asks for a password; PGPASSWORD takes precedence.
	f := NewFlags(
		&Flag{Name: "host", Short: 'h', Long: "host", HasArg: true, ArgName: "HOST", Help: "server host (default: localhost)"},
		&Flag{Name: "port", Long: "port", HasArg: true, ArgName: "PORT", Help: "server port (default: 5432)"},
		&Flag{Name: "username", Short: 'U', Long: "username", HasArg: true, ArgName: "USER", Help: "user name (default: postgres)"},
		&Flag{Name: "dbname", Short: 'd', Long: "dbname", HasArg: true, ArgName: "DB", Help: "database name (default: postgres; or the first positional argument)"},
		&Flag{Name: "password", Short: 'W', Long: "password", Help: "prompt for the password (PGPASSWORD env var is used if set)"},
		&Flag{Name: "no-password", Short: 'w', Long: "no-password", Help: "never prompt for a password"},
		&Flag{Name: "command", Short: 'c', Long: "command", HasArg: true, ArgName: "SQL", Help: "run SQL and exit (repeatable)"},
		&Flag{Name: "file", Short: 'f', Long: "file", HasArg: true, ArgName: "FILE", Help: "run SQL from a file and exit"},
		&Flag{Name: "tuples-only", Short: 't', Long: "tuples-only", Help: "print rows only, no header or command tags"},
		&Flag{Name: "no-align", Short: 'A', Long: "no-align", Help: "unaligned output (tab-separated columns)"},
		&Flag{Name: "quiet", Short: 'q', Long: "quiet", Help: "no banner"},
		&Flag{Name: "ssl", Long: "ssl", Help: "connect with SSL (server certificate is not verified)"},
		&Flag{Name: "timeout", Long: "timeout", HasArg: true, ArgName: "SECS", Help: "operation timeout in seconds (default: 30)"},
	)
	if !parseOrDie("psql", f, args) {
		return 2
	}
	if handleHelpVersion("psql", "[OPTIONS] [DBNAME]", f) {
		return 0
	}

	host, port, user, db := "localhost", 5432, "postgres", ""
	timeout := 30 * time.Second
	if f.Has("host") {
		host = f.Str("host")
	}
	if f.Has("port") {
		n, err := f.Int("port", 5432)
		if err != nil || n < 1 || n > 65535 {
			warn("psql", "invalid port '%s'", f.Str("port"))
			return 1
		}
		port = n
	}
	if f.Has("username") {
		user = f.Str("username")
	}
	if f.Has("dbname") {
		db = f.Str("dbname")
	} else if len(f.Positional) > 0 {
		db = f.Positional[0]
	}
	if db == "" {
		db = user
	}
	if f.Has("timeout") {
		n, err := f.Int("timeout", 30)
		if err != nil || n < 0 {
			warn("psql", "invalid timeout '%s'", f.Str("timeout"))
			return 1
		}
		timeout = time.Duration(n) * time.Second
	}
	wantPassword := f.Has("password")
	noPassword := f.Has("no-password")
	tuplesOnly := f.Has("tuples-only")
	noAlign := f.Has("no-align")
	quiet := f.Has("quiet")
	useSSL := f.Has("ssl")

	envPwd := os.Getenv("PGPASSWORD")
	getPassword := func() (string, error) {
		if envPwd != "" {
			return envPwd, nil
		}
		if noPassword {
			return "", fmt.Errorf("password required but -w/--no-password given")
		}
		if !isTerminal(os.Stdin) {
			return "", fmt.Errorf("password required (use PGPASSWORD or -W on a terminal)")
		}
		line, _ := readPassword("Password: ")
		fmt.Fprintln(os.Stderr)
		return line, nil
	}
	if wantPassword && isTerminal(os.Stdin) && envPwd == "" {
		line, _ := readPassword("Password: ")
		fmt.Fprintln(os.Stderr)
		envPwd = line
	}

	conn, err := pgDial(host, port, timeout, useSSL)
	if err != nil {
		warn("psql", "could not connect to %s:%d: %v", host, port, err)
		return 1
	}
	defer conn.conn.Close()
	if err := conn.startup(user, db, getPassword); err != nil {
		warn("psql", "connection to %s:%d failed: %v", host, port, err)
		return 1
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "winux psql %s (pure Go client) — connected to \"%s\" as user \"%s\" at %s:%d\n",
			Version, db, user, host, port)
	}

	var sqls []string
	if f.Has("command") {
		sqls = append(sqls, f.Strs("command")...)
	}
	if file := f.Str("file"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			warn("psql", "%v", err)
			return 1
		}
		sqls = append(sqls, string(data))
	}
	if len(sqls) > 0 {
		exitCode := 0
		for _, s := range sqls {
			if err := pgExec(conn, s, tuplesOnly, noAlign); err != nil {
				fmt.Fprintf(os.Stderr, "psql: error: %s\n", err)
				exitCode = 1
			}
		}
		return exitCode
	}
	if !isTerminal(os.Stdin) {
		data, err := io.ReadAll(stripBOM(os.Stdin))
		if err != nil {
			warn("psql", "%v", err)
			return 1
		}
		if err := pgExec(conn, string(data), tuplesOnly, noAlign); err != nil {
			return 1
		}
		return 0
	}
	return pgRepl(conn, host, port, user, db, tuplesOnly, noAlign, quiet)
}

// pgExec runs one SQL text and prints every result set.
func pgExec(conn *pgConn, sql string, tuplesOnly, noAlign bool) error {
	results, err := conn.simpleQuery(sql)
	for _, r := range results {
		pgPrintResult(r, tuplesOnly, noAlign)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "psql: error: %s\n", err)
		return err
	}
	return nil
}

func pgPrintResult(r pgResult, tuplesOnly, noAlign bool) {
	if len(r.cols) == 0 && len(r.rows) == 0 {
		if r.tag != "" && !tuplesOnly {
			fmt.Fprintln(os.Stdout, r.tag)
		}
		return
	}
	if noAlign {
		for _, row := range r.rows {
			fmt.Fprintln(os.Stdout, strings.Join(row, "\t"))
		}
		return
	}
	widths := make([]int, len(r.cols))
	if !tuplesOnly {
		for i, c := range r.cols {
			widths[i] = runeLen(c)
		}
		for _, row := range r.rows {
			for i, v := range row {
				if i < len(widths) && runeLen(v) > widths[i] {
					widths[i] = runeLen(v)
				}
			}
		}
		var b strings.Builder
		for i, c := range r.cols {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(padRight(c, widths[i]))
		}
		fmt.Fprintln(os.Stdout, b.String())
		var sep strings.Builder
		for i, w := range widths {
			if i > 0 {
				sep.WriteString("-+-")
			}
			sep.WriteString(strings.Repeat("-", w))
		}
		fmt.Fprintln(os.Stdout, sep.String())
	}
	for _, row := range r.rows {
		var b strings.Builder
		for i, v := range row {
			if i > 0 {
				b.WriteString(" | ")
			}
			if i < len(widths) {
				b.WriteString(padRight(v, widths[i]))
			} else {
				b.WriteString(v)
			}
		}
		fmt.Fprintln(os.Stdout, b.String())
	}
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func padRight(s string, w int) string {
	n := runeLen(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

// ---------------------------------------------------------------------------
// Interactive REPL

func pgRepl(conn *pgConn, host string, port int, user, db string, tuplesOnly, noAlign, quiet bool) int {
	in, cleanup := consoleInput()
	defer cleanup()
	rd := bufio.NewReader(stripBOM(in))
	exitCode := 0
	for {
		fmt.Fprintf(os.Stdout, "%s=> ", db)
		line, err := rd.ReadString('\n')
		if len(line) > 0 {
			t := strings.TrimSpace(line)
			switch {
			case t == "":
			case t == `\q`:
				fmt.Fprintln(os.Stdout)
				conn.send('X', nil)
				return exitCode
			case t == `\?` || t == `\h`:
				fmt.Fprintln(os.Stdout, "psql help: \\q quit, \\l list databases, \\dt list tables, \\d NAME describe table, \\c DB reconnect")
			case t == `\l`:
				if err := pgExec(conn, "SELECT datname AS name, pg_size_pretty(pg_database_size(datname)) AS size FROM pg_database ORDER BY datname", tuplesOnly, noAlign); err != nil {
					fmt.Fprintf(os.Stderr, "psql: error: %s\n", err)
					exitCode = 1
				}
			case t == `\dt` || t == `\dt+`:
				q := "SELECT schemaname AS schema, tablename AS table_name FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') ORDER BY schemaname, tablename"
				if err := pgExec(conn, q, tuplesOnly, noAlign); err != nil {
					fmt.Fprintf(os.Stderr, "psql: error: %s\n", err)
					exitCode = 1
				}
			case strings.HasPrefix(t, `\d `) || t == `\d`:
				name := strings.TrimSpace(strings.TrimPrefix(t, `\d`))
				q := `SELECT column_name AS column, data_type AS type, is_nullable AS nullable FROM information_schema.columns WHERE table_name = '` + strings.ReplaceAll(name, "'", "''") + `' ORDER BY ordinal_position`
				if err := pgExec(conn, q, tuplesOnly, noAlign); err != nil {
					fmt.Fprintf(os.Stderr, "psql: error: %s\n", err)
					exitCode = 1
				}
			case strings.HasPrefix(t, `\c `):
				newDB := strings.TrimSpace(strings.TrimPrefix(t, `\c`))
				newConn, err := pgDial(host, port, 30*time.Second, false)
				if err == nil {
					if err = newConn.startup(user, newDB, func() (string, error) {
						return "", fmt.Errorf("password prompt unsupported on \\c; set PGPASSWORD")
					}); err == nil {
						conn.conn.Close()
						conn = newConn
						db = newDB
						continue
					}
					newConn.conn.Close()
				}
				fmt.Fprintf(os.Stderr, "psql: error: could not switch to database \"%s\": %v\n", newDB, err)
				exitCode = 1
			case strings.HasPrefix(t, `\`):
				fmt.Fprintf(os.Stderr, "psql: invalid command \\%s\n", strings.Fields(strings.TrimPrefix(t, `\`))[0])
				exitCode = 1
			default:
				if err := pgExec(conn, t, tuplesOnly, noAlign); err != nil {
					exitCode = 1
				}
			}
		}
		if err == io.EOF {
			fmt.Fprintln(os.Stdout)
			conn.send('X', nil)
			return exitCode
		}
	}
}
