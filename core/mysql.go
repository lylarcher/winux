package core

// A pure-Go MySQL client: no client installation needed, only net + the
// standard library. It speaks the MySQL client/server wire protocol directly:
// handshake, mysql_native_password and caching_sha2_password authentication
// (including the RSA full-auth exchange), COM_QUERY result sets (text
// protocol), batch and interactive modes with the familiar `mysql>` prompts.

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func init() {
	addCmd("mysql", "MySQL client (pure Go, no client install; only TCP)", cmdMysql)
}

// MySQL client capability flags we ask for.
const (
	mysqlCapConnectWithDB    = 0x00000008
	mysqlCapProtocol41       = 0x00000200
	mysqlCapMultiStatements  = 0x00010000
	mysqlCapMultiResults     = 0x00020000
	mysqlCapSecureConnection = 0x00008000
	mysqlCapPluginAuth       = 0x00080000
)

const mysqlClientCaps = mysqlCapProtocol41 | mysqlCapSecureConnection | mysqlCapPluginAuth |
	mysqlCapConnectWithDB | mysqlCapMultiStatements | mysqlCapMultiResults

type mysqlConn struct {
	conn net.Conn
	rd   *bufio.Reader
	seq  byte
}

func (c *mysqlConn) writePacket(payload []byte) error {
	for {
		chunk := payload
		if len(chunk) > 0xffffff {
			chunk = chunk[:0xffffff]
		}
		hdr := []byte{byte(len(chunk)), byte(len(chunk) >> 8), byte(len(chunk) >> 16), c.seq}
		c.seq++
		if _, err := c.conn.Write(hdr); err != nil {
			return err
		}
		if _, err := c.conn.Write(chunk); err != nil {
			return err
		}
		payload = payload[len(chunk):]
		if len(payload) == 0 {
			return nil
		}
	}
}

func (c *mysqlConn) readPacket() ([]byte, error) {
	var full []byte
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(c.rd, hdr[:]); err != nil {
			return nil, err
		}
		n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
		seq := hdr[3]
		_ = seq
		buf := make([]byte, n)
		if _, err := io.ReadFull(c.rd, buf); err != nil {
			return nil, err
		}
		full = append(full, buf...)
		if n < 0xffffff {
			return full, nil
		}
	}
}

func (c *mysqlConn) close() { c.conn.Close() }

// lenenc reads a MySQL length-encoded value; isNull marks the 0xfb NULL.
func lenenc(b []byte, off *int) (uint64, bool, bool) {
	if *off >= len(b) {
		return 0, false, true
	}
	c := b[*off]
	*off++
	switch {
	case c < 0xfb:
		return uint64(c), false, false
	case c == 0xfb:
		return 0, true, false
	case c == 0xfc:
		if *off+2 > len(b) {
			return 0, false, true
		}
		v := uint64(b[*off]) | uint64(b[*off+1])<<8
		*off += 2
		return v, false, false
	case c == 0xfd:
		if *off+3 > len(b) {
			return 0, false, true
		}
		v := uint64(b[*off]) | uint64(b[*off+1])<<8 | uint64(b[*off+2])<<16
		*off += 3
		return v, false, false
	default: // 0xfe
		if *off+8 > len(b) {
			return 0, false, true
		}
		var v uint64
		for i := 0; i < 8; i++ {
			v |= uint64(b[*off+i]) << (8 * i)
		}
		*off += 8
		return v, false, false
	}
}

// ------------------------------------------------------------------ auth ----

// scrambleNative implements mysql_native_password.
func scrambleNative(password, seed []byte) []byte {
	stage1 := sha1.Sum(password)
	stage2 := sha1.Sum(stage1[:])
	h := sha1.New()
	h.Write(seed)
	h.Write(stage2[:])
	digest := h.Sum(nil)
	out := make([]byte, len(stage1))
	for i := range stage1 {
		out[i] = stage1[i] ^ digest[i]
	}
	return out
}

// scrambleSha2 implements caching_sha2_password's fast path.
func scrambleSha2(password, seed []byte) []byte {
	p1 := sha256.Sum256(password)
	p2 := sha256.Sum256(p1[:])
	h := sha256.New()
	h.Write(p2[:])
	h.Write(seed)
	digest := h.Sum(nil)
	out := make([]byte, len(p1))
	for i := range p1 {
		out[i] = p1[i] ^ digest[i]
	}
	return out
}

type mysqlGreeting struct {
	version    string
	threadID   uint32
	seed       []byte
	plugin     string
	serverCaps uint32
}

func parseGreeting(p []byte) (*mysqlGreeting, error) {
	g := &mysqlGreeting{}
	i := 0
	if i >= len(p) {
		return nil, fmt.Errorf("empty greeting")
	}
	i++ // protocol version
	if j := bytes.IndexByte(p[i:], 0); j < 0 {
		return nil, fmt.Errorf("malformed greeting (no version terminator)")
	} else {
		g.version = string(p[i : i+j])
		i += j + 1
	}
	if i+4 > len(p) {
		return nil, fmt.Errorf("malformed greeting")
	}
	g.threadID = uint32(p[i]) | uint32(p[i+1])<<8 | uint32(p[i+2])<<16 | uint32(p[i+3])<<24
	i += 4
	if i+8 > len(p) {
		return nil, fmt.Errorf("malformed greeting")
	}
	g.seed = append(g.seed, p[i:i+8]...)
	i += 8
	if i+1 > len(p) {
		return nil, fmt.Errorf("malformed greeting")
	}
	i++ // filler
	if i+2 > len(p) {
		return nil, fmt.Errorf("malformed greeting")
	}
	g.serverCaps = uint32(p[i]) | uint32(p[i+1])<<8
	i += 2
	if i+1 > len(p) {
		return nil, fmt.Errorf("malformed greeting")
	}
	i++    // charset
	i += 2 // status
	if i+2 > len(p) {
		return nil, fmt.Errorf("malformed greeting")
	}
	g.serverCaps |= uint32(p[i]) | uint32(p[i+1])<<16
	i += 2
	authLen := 0
	if i < len(p) {
		authLen = int(p[i])
		i++
	}
	i += 10 // reserved
	if authLen > 8 && i+authLen-8 <= len(p) {
		part2 := p[i : i+authLen-8]
		if j := bytes.IndexByte(part2, 0); j >= 0 {
			part2 = part2[:j]
		}
		g.seed = append(g.seed, part2...)
		i += authLen - 8
	}
	if len(g.seed) > 20 {
		g.seed = g.seed[:20]
	}
	if i < len(p) {
		if j := bytes.IndexByte(p[i:], 0); j >= 0 {
			g.plugin = string(p[i : i+j])
		}
	}
	return g, nil
}

// handshakeResponse builds the client greeting for the given auth method.
func handshakeResponse(user, pass, db, plugin string, scramble []byte) []byte {
	var b []byte
	putU32 := func(v uint32) { b = append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
	putU32(mysqlClientCaps)
	b = append(b, 0, 0, 0, 1) // max packet 16MB
	b = append(b, 45)         // utf8mb4_general_ci
	for i := 0; i < 23; i++ {
		b = append(b, 0)
	}
	b = append(b, []byte(user)...)
	b = append(b, 0)
	b = append(b, byte(len(scramble)))
	b = append(b, scramble...)
	// CLIENT_CONNECT_WITH_DB is set, so the (possibly empty) database field is
	// always present, otherwise a following plugin name could be misparsed.
	b = append(b, []byte(db)...)
	b = append(b, 0)
	if plugin != "" {
		b = append(b, []byte(plugin)...)
		b = append(b, 0)
	}
	return b
}

// mysqlError is a server error packet turned into a Go error.
type mysqlError struct {
	code     uint16
	sqlState string
	message  string
}

func (e *mysqlError) Error() string {
	return fmt.Sprintf("ERROR %d (%s): %s", e.code, e.sqlState, e.message)
}

func parseErrPacket(p []byte) *mysqlError {
	e := &mysqlError{}
	if len(p) < 3 {
		e.message = string(p)
		return e
	}
	e.code = uint16(p[1]) | uint16(p[2])<<8
	off := 3
	if off+6 <= len(p) && p[off] == '#' {
		e.sqlState = string(p[off+1 : off+6])
		off += 6
	}
	e.message = string(p[off:])
	return e
}

func isErrPacket(p []byte) bool { return len(p) > 0 && p[0] == 0xff }

// connect opens a connection and authenticates. pass == "" means no password.
func mysqlConnect(host string, port int, user, pass, db string, timeout time.Duration) (*mysqlConn, *mysqlGreeting, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	nc, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, nil, err
	}
	c := &mysqlConn{conn: nc, rd: bufio.NewReader(nc), seq: 0}
	greetPkt, err := c.readPacket()
	if err != nil {
		c.close()
		return nil, nil, err
	}
	g, err := parseGreeting(greetPkt)
	if err != nil {
		c.close()
		return nil, nil, err
	}
	if g.plugin == "" {
		g.plugin = "mysql_native_password"
	}
	var scramble []byte
	if pass != "" {
		switch g.plugin {
		case "mysql_native_password":
			scramble = scrambleNative([]byte(pass), g.seed)
		case "caching_sha2_password":
			scramble = scrambleSha2([]byte(pass), g.seed)
		default:
			c.close()
			return nil, nil, fmt.Errorf("unsupported authentication plugin '%s' (supported: mysql_native_password, caching_sha2_password)", g.plugin)
		}
	}
	resp := handshakeResponse(user, pass, db, g.plugin, scramble)
	c.seq = 1
	if err := c.writePacket(resp); err != nil {
		c.close()
		return nil, nil, err
	}
	// caching_sha2_password full-auth exchange (no SSL: RSA-encrypt the
	// password XORed with the seed using the server's public key).
	if pass != "" && g.plugin == "caching_sha2_password" {
		authPkt, err := c.readPacket()
		if err != nil {
			c.close()
			return nil, nil, err
		}
		if len(authPkt) == 1 && authPkt[0] == 0x04 { // full auth required
			reqKey := []byte{0x02}
			c.seq = 2
			if err := c.writePacket(reqKey); err != nil {
				c.close()
				return nil, nil, err
			}
			keyPkt, err := c.readPacket()
			if err != nil {
				c.close()
				return nil, nil, err
			}
			pemData := keyPkt
			if len(pemData) > 0 && pemData[0] == 0x01 { // strip the bulk-string marker
				pemData = pemData[1:]
			}
			block, _ := pem.Decode(pemData)
			if block == nil {
				c.close()
				return nil, nil, fmt.Errorf("could not parse the server's RSA public key")
			}
			pub, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				c.close()
				return nil, nil, fmt.Errorf("could not parse the server's RSA public key: %v", err)
			}
			rsaPub, ok := pub.(*rsa.PublicKey)
			if !ok {
				c.close()
				return nil, nil, fmt.Errorf("server public key is not RSA")
			}
			plain := append([]byte(pass), 0)
			for i := range plain {
				plain[i] ^= g.seed[i%len(g.seed)]
			}
			enc, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPub, plain)
			if err != nil {
				c.close()
				return nil, nil, err
			}
			c.seq = 3
			if err := c.writePacket(enc); err != nil {
				c.close()
				return nil, nil, err
			}
		} else if len(authPkt) == 1 && authPkt[0] == 0x01 {
			// fast auth succeeded; the OK/ERR packet follows
			okPkt, err := c.readPacket()
			if err != nil {
				c.close()
				return nil, nil, err
			}
			if isErrPacket(okPkt) {
				c.close()
				return nil, nil, parseErrPacket(okPkt)
			}
			return c, g, nil
		} else if isErrPacket(authPkt) {
			c.close()
			return nil, nil, parseErrPacket(authPkt)
		}
	}
	okPkt, err := c.readPacket()
	if err != nil {
		c.close()
		return nil, nil, err
	}
	if isErrPacket(okPkt) {
		c.close()
		return nil, nil, parseErrPacket(okPkt)
	}
	return c, g, nil
}

// ------------------------------------------------------------- statements ----

type mysqlColumn struct {
	name string
}

type mysqlRow struct {
	values []string
	isNull []bool
}

// exec runs one statement and returns either rows (isSelect) or the OK info.
func (c *mysqlConn) exec(sql string) (cols []mysqlColumn, rows []mysqlRow, okRows uint64, isSelect bool, err error) {
	c.seq = 0
	payload := append([]byte{0x03}, []byte(sql)...)
	if err := c.writePacket(payload); err != nil {
		return nil, nil, 0, false, err
	}
	pkt, err := c.readPacket()
	if err != nil {
		return nil, nil, 0, false, err
	}
	switch {
	case len(pkt) > 0 && pkt[0] == 0x00:
		off := 1
		affected, _, _ := lenenc(pkt, &off)
		return nil, nil, affected, false, nil
	case isErrPacket(pkt):
		return nil, nil, 0, false, parseErrPacket(pkt)
	case len(pkt) > 0 && pkt[0] == 0xfb:
		return nil, nil, 0, false, fmt.Errorf("LOCAL INFILE is not supported")
	}
	// result set
	off := 0
	nCols, _, _ := lenenc(pkt, &off)
	for i := uint64(0); i < nCols; i++ {
		col, err := c.readPacket()
		if err != nil {
			return nil, nil, 0, false, err
		}
		cols = append(cols, parseColumnDef(col))
	}
	// column terminator: EOF (0xfe 0x00) when the server does not deprecate EOF
	term, err := c.readPacket()
	if err != nil {
		return nil, nil, 0, false, err
	}
	if len(term) > 0 && term[0] == 0xfe {
		// rows follow
	} else if len(term) > 0 && term[0] == 0x00 && len(term) >= 7 {
		// DEPRECATE_EOF with an empty result: the terminator IS the OK packet
		return cols, nil, 0, true, nil
	} else {
		// treat it as the first row
		parseRow(term, nCols, &rows)
	}
	for {
		pkt, err := c.readPacket()
		if err != nil {
			return nil, nil, 0, false, err
		}
		if len(pkt) == 0 {
			continue
		}
		if pkt[0] == 0xfe && len(pkt) <= 8 { // EOF terminator
			break
		}
		if pkt[0] == 0x00 && len(pkt) >= 7 { // OK terminator (DEPRECATE_EOF)
			break
		}
		parseRow(pkt, nCols, &rows)
	}
	return cols, rows, 0, true, nil
}

func parseColumnDef(p []byte) mysqlColumn {
	off := 0
	for i := 0; i < 4; i++ { // catalog, schema, table, org_table
		v, _, _ := lenenc(p, &off)
		off += int(v)
	}
	nameLen, _, _ := lenenc(p, &off)
	name := ""
	if int(nameLen) <= len(p)-off {
		name = string(p[off : off+int(nameLen)])
	}
	return mysqlColumn{name: name}
}

func parseRow(p []byte, nCols uint64, rows *[]mysqlRow) {
	off := 0
	row := mysqlRow{}
	for i := uint64(0); i < nCols; i++ {
		v, isNull, _ := lenenc(p, &off)
		row.isNull = append(row.isNull, isNull)
		if isNull {
			row.values = append(row.values, "")
		} else if int(v) <= len(p)-off {
			row.values = append(row.values, string(p[off:off+int(v)]))
			off += int(v)
		} else {
			row.values = append(row.values, "")
		}
	}
	*rows = append(*rows, row)
}

// ------------------------------------------------------------------ output ---

func sanitizeCell(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("\\t")
		case r == '\n':
			b.WriteString("\\n")
		case r == '\r':
			b.WriteString("\\r")
		case r == 0:
			b.WriteString("\\0")
		case r < 0x20 || r == 0x7f:
			b.WriteString(fmt.Sprintf("\\x%02x", r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func mysqlCellWidth(s string) int {
	w := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			w += 4 // escaped forms above are 2..4 chars; approximate
		} else {
			w++
		}
	}
	return w
}

// printTable renders the default mysql client border table.
func printTable(cols []mysqlColumn, rows []mysqlRow, batch, noNames, raw bool, vertical bool, out io.Writer) {
	if batch {
		for _, r := range rows {
			vals := make([]string, len(r.values))
			for i, v := range r.values {
				if r.isNull[i] {
					vals[i] = "NULL"
				} else {
					vals[i] = v
				}
			}
			fmt.Fprintln(out, strings.Join(vals, "\t"))
		}
		return
	}
	if vertical {
		maxW := 0
		for _, c := range cols {
			if len(c.name) > maxW {
				maxW = len(c.name)
			}
		}
		for i, r := range rows {
			fmt.Fprintf(out, "*************************** %d. row ***************************\n", i+1)
			for j := range cols {
				v := "NULL"
				if !r.isNull[j] {
					v = r.values[j]
				}
				fmt.Fprintf(out, "%-*s: %s\n", maxW, cols[j].name, v)
			}
		}
		return
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = mysqlCellWidth(c.name)
	}
	for _, r := range rows {
		for i := range cols {
			v := "NULL"
			if !r.isNull[i] {
				v = r.values[i]
			}
			if w := mysqlCellWidth(v); w > widths[i] {
				widths[i] = w
			}
		}
	}
	sep := "+"
	for _, w := range widths {
		sep += strings.Repeat("-", w+2) + "+"
	}
	render := func(cells []string) {
		line := "|"
		for i, w := range widths {
			v := cells[i]
			line += " " + v + strings.Repeat(" ", w-mysqlCellWidth(v)) + " |"
		}
		fmt.Fprintln(out, line)
	}
	if !noNames {
		fmt.Fprintln(out, sep)
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = c.name
		}
		render(cells)
		fmt.Fprintln(out, sep)
	}
	for _, r := range rows {
		cells := make([]string, len(r.values))
		for i, v := range r.values {
			if r.isNull[i] {
				cells[i] = "NULL"
			} else if raw {
				cells[i] = v
			} else {
				cells[i] = sanitizeCell(v)
			}
		}
		render(cells)
	}
	if !noNames && len(rows) > 0 {
		fmt.Fprintln(out, sep)
	}
}

// ----------------------------------------------------------- interactive -----

type execStmt struct {
	text     string
	vertical bool
}

// splitExecStatements splits -e input on statement terminators: ';' (also \g)
// for table output and \G for vertical output. Quotes are respected.
func splitExecStatements(src string) []execStmt {
	var out []execStmt
	var cur strings.Builder
	var quote byte
	esc := false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		if esc {
			cur.WriteByte(ch)
			esc = false
			continue
		}
		if ch == '\\' && quote != 0 {
			cur.WriteByte(ch)
			esc = true
			continue
		}
		if quote != 0 {
			cur.WriteByte(ch)
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			quote = ch
			cur.WriteByte(ch)
		case ';':
			flushExec(&out, &cur, false)
		default:
			cur.WriteByte(ch)
		}
	}
	// a trailing \G / \g terminator
	trimmed := strings.TrimSpace(cur.String())
	if strings.HasSuffix(trimmed, `\G`) {
		cur.Reset()
		cur.WriteString(strings.TrimSuffix(trimmed, `\G`))
		flushExec(&out, &cur, true)
	} else if strings.HasSuffix(trimmed, `\g`) {
		cur.Reset()
		cur.WriteString(strings.TrimSuffix(trimmed, `\g`))
		flushExec(&out, &cur, false)
	} else {
		flushExec(&out, &cur, false)
	}
	return out
}

func flushExec(out *[]execStmt, cur *strings.Builder, vertical bool) {
	if s := strings.TrimSpace(cur.String()); s != "" {
		*out = append(*out, execStmt{text: s, vertical: vertical})
	}
	cur.Reset()
}

func cmdMysql(args []string) int {
	// Pre-process the password options, which accept an optional value in the
	// classic mysql way: -p prompts, -pSECRET / -p SECRET / --password[=SECRET].
	var rest []string
	password := ""
	promptPassword := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--password":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				password = args[i+1]
				i++
			} else {
				promptPassword = true
			}
		case strings.HasPrefix(a, "--password="):
			password = strings.TrimPrefix(a, "--password=")
		case len(a) > 2 && strings.HasPrefix(a, "-p"):
			password = a[2:]
		default:
			rest = append(rest, a)
		}
	}
	f := NewFlags(
		&Flag{Name: "host", Short: 'h', Long: "host", HasArg: true, ArgName: "HOST", Help: "server host (default localhost)"},
		&Flag{Name: "port", Short: 'P', Long: "port", HasArg: true, ArgName: "PORT", Help: "server port (default 3306)"},
		&Flag{Name: "user", Short: 'u', Long: "user", HasArg: true, ArgName: "USER", Help: "user name (default: the current Windows user)"},
		&Flag{Name: "database", Short: 'D', Long: "database", HasArg: true, ArgName: "DB", Help: "database to use"},
		&Flag{Name: "execute", Short: 'e', Long: "execute", HasArg: true, ArgName: "SQL", Help: "execute SQL and exit"},
		&Flag{Name: "batch", Short: 'B', Long: "batch", Help: "tab-separated output, no borders"},
		&Flag{Name: "skip-column-names", Short: 'N', Long: "skip-column-names", Help: "do not print column names"},
		&Flag{Name: "table", Short: 't', Long: "table", Help: "force table output even in batch mode"},
		&Flag{Name: "raw", Short: 'r', Long: "raw", Help: "print values without escaping"},
		&Flag{Name: "vertical", Short: 'E', Long: "vertical", Help: "vertical output (also \\G)"},
		&Flag{Name: "verbose", Short: 'v', Long: "verbose", Help: "echo each statement before running it"},
		&Flag{Name: "protocol", Long: "protocol", HasArg: true, ArgName: "NAME", Help: "only tcp is supported"},
		&Flag{Name: "connect-timeout", Long: "connect-timeout", HasArg: true, ArgName: "SECONDS", Help: "connection timeout in seconds"},
		&Flag{Name: "force", Short: 'f', Long: "force", Help: "continue even when a statement fails"},
	)
	if !parseOrDie("mysql", f, rest) {
		return 2
	}
	if handleHelpVersion("mysql", "[OPTION]... [DATABASE]", f) {
		return 0
	}
	if prot := f.Str("protocol"); prot != "" && !strings.EqualFold(prot, "tcp") {
		fmt.Fprintf(os.Stderr, "mysql: only --protocol=tcp is supported (got '%s')\n", prot)
		return 1
	}
	host := f.Str("host")
	if host == "" {
		host = "localhost"
	}
	port, err := f.Int("port", 3306)
	if err != nil {
		warn("mysql", "%v", err)
		return 2
	}
	user := f.Str("user")
	if user == "" {
		user = shortUserName(currentUserName())
	}
	db := f.Str("database")
	if len(f.Positional) > 0 && db == "" {
		db = f.Positional[0]
	}
	if promptPassword {
		var hidden bool
		password, hidden = readPassword("Enter password: ")
		if !hidden {
			fmt.Fprintln(os.Stderr, "mysql: cannot read a password securely (stdin is not a console); use -pPASSWORD or --password=PASSWORD")
			return 1
		}
	}
	timeout := 10 * time.Second
	if t, e := f.Int("connect-timeout", 0); e == nil && t > 0 {
		timeout = time.Duration(t) * time.Second
	}

	c, g, err := mysqlConnect(host, port, user, password, db, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mysql: %v\n", err)
		return 1
	}
	defer c.close()
	verbose := f.Count("verbose") > 0

	runStmt := func(sql string, vertical bool) error {
		if verbose {
			fmt.Println(sql)
		}
		start := time.Now()
		cols, rows, affected, isSelect, err := c.exec(sql)
		elapsed := time.Since(start).Seconds()
		if err != nil {
			if me, ok := err.(*mysqlError); ok {
				fmt.Fprintln(os.Stderr, me.Error())
			} else {
				fmt.Fprintf(os.Stderr, "mysql: %v\n", err)
			}
			return err
		}
		if isSelect {
			printTable(cols, rows, f.Has("batch") && !f.Has("table"), f.Has("skip-column-names"), f.Has("raw"), f.Has("vertical") || vertical, os.Stdout)
			fmt.Fprintf(os.Stdout, "%d row%s in set (%.2f sec)\n", len(rows), plural(len(rows)), elapsed)
		} else {
			fmt.Fprintf(os.Stdout, "Query OK, %d row(s) affected (%.2f sec)\n", affected, elapsed)
		}
		return nil
	}

	if sql := f.Str("execute"); sql != "" {
		exitCode := 0
		for _, st := range splitExecStatements(sql) {
			if err := runStmt(st.text, st.vertical); err != nil {
				if !f.Has("force") {
					return 1
				}
				exitCode = 1
			}
		}
		return exitCode
	}

	// interactive REPL (or a script on stdin when not a console)
	fmt.Fprintf(os.Stderr, "winux mysql %s (pure Go client)\n", g.version)
	in, cleanup := consoleInput()
	defer cleanup()
	rd := bufio.NewReader(stripBOM(in))
	interactive := isTerminal(os.Stdin)
	var buf strings.Builder
	exitCode := 0
	execute := func(full string, vertical bool) {
		if verbose {
			fmt.Println(full)
		}
		start := time.Now()
		cols, rows, affected, isSelect, rerr := c.exec(full)
		elapsed := time.Since(start).Seconds()
		if rerr != nil {
			if me, ok := rerr.(*mysqlError); ok {
				fmt.Fprintln(os.Stderr, me.Error())
			} else {
				fmt.Fprintf(os.Stderr, "mysql: %v\n", rerr)
			}
			exitCode = 1
		} else if isSelect {
			printTable(cols, rows, f.Has("batch") && !f.Has("table"), f.Has("skip-column-names"), f.Has("raw"), f.Has("vertical") || vertical, os.Stdout)
			fmt.Fprintf(os.Stdout, "%d row%s in set (%.2f sec)\n", len(rows), plural(len(rows)), elapsed)
		} else {
			fmt.Fprintf(os.Stdout, "Query OK, %d row(s) affected (%.2f sec)\n", affected, elapsed)
		}
	}
	for {
		if interactive {
			if buf.Len() == 0 {
				fmt.Fprint(os.Stdout, "mysql> ")
			} else {
				fmt.Fprint(os.Stdout, "    -> ")
			}
		}
		line, err := rd.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(line)
			// bare command words when nothing is being accumulated
			if interactive && buf.Len() == 0 {
				switch {
				case strings.EqualFold(trimmed, "exit"), strings.EqualFold(trimmed, "quit"), trimmed == `\q`:
					fmt.Fprintln(os.Stdout)
					return exitCode
				case trimmed == `\c`:
					continue
				case strings.EqualFold(trimmed, "help"), strings.EqualFold(trimmed, `\h`):
					fmt.Fprintln(os.Stdout, "mysql help: end a statement with ';' or \\g (table) / \\G (vertical); \\q or exit to quit; \\c cancels a pending statement")
					continue
				}
			}
			stmt := trimmed
			term := ""
			for _, t := range []string{`\G`, `\g`, ";"} {
				if strings.HasSuffix(stmt, t) {
					term = t
					stmt = strings.TrimSuffix(stmt, t)
					break
				}
			}
			if term == "" {
				buf.WriteString(line)
				continue
			}
			full := strings.TrimSpace(buf.String() + " " + stmt)
			buf.Reset()
			if full != "" {
				execute(full, term == `\G`)
			}
		}
		if err == io.EOF {
			// batch mode: run whatever was left without a terminator
			if !interactive {
				if full := strings.TrimSpace(buf.String()); full != "" {
					execute(full, false)
				}
			} else {
				fmt.Fprintln(os.Stdout)
			}
			break
		}
	}
	return exitCode
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
