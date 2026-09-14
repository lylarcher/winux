package core

// mongodb is a pure-Go MongoDB client speaking the wire protocol directly
// (no mongosh / mongo CLI needed): hand-rolled BSON, OP_MSG messages and
// SCRAM-SHA-256 authentication, all on the standard library.
//
//	mongodb --eval '{"ping":1}'
//	echo '{"find":"users","filter":{"age":{"$gt":30}},"limit":10}' | mongodb
//	mongodb -u alice -p --db test    # interactive REPL
//
// The REPL understands `use <db>`, `show dbs`, `show collections` and the
// mongosh-style shorthand db.<collection>.<method>(<json>) for the common
// CRUD/aggregation methods; anything else starting with '{' is sent verbatim
// as a command document. No JavaScript is interpreted.

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func init() {
	addCmd("mongodb", "MongoDB client over the wire protocol (no mongosh needed)", cmdMongodb)
}

// ---------------------------------------------------------------------------
// BSON

// BSON element types (the subset we encode and decode).
const (
	bsonDouble   = 0x01
	bsonString   = 0x02
	bsonDoc      = 0x03
	bsonArray    = 0x04
	bsonBinary   = 0x05
	bsonObjectID = 0x07
	bsonBool     = 0x08
	bsonDateTime = 0x09
	bsonNull     = 0x0A
	bsonInt32    = 0x10
	bsonInt64    = 0x12
)

type mongoBinary struct {
	Subtype byte
	Data    []byte
}

func bsonEncode(v any) []byte {
	var b bytes.Buffer
	bsonWrite(&b, v)
	return b.Bytes()
}

func bsonWrite(b *bytes.Buffer, v any) {
	switch t := v.(type) {
	case nil:
		bsonWriteNull(b)
	case bool:
		bsonWriteBool(b, t)
	case string:
		bsonWriteString(b, t)
	case int32:
		bsonWriteInt32(b, t)
	case int64:
		bsonWriteInt64(b, t)
	case int:
		bsonWriteInt64(b, int64(t))
	case float64:
		bsonWriteDouble(b, t)
	case time.Time:
		bsonWriteDateTime(b, t)
	case map[string]any:
		bsonWriteDoc(b, t)
	case []any:
		bsonWriteArray(b, t)
	case mongoBinary:
		bsonWriteBinary(b, t)
	case mongoOID:
		bsonWriteObjectID(b, t)
	default:
		bsonWriteNull(b)
	}
}

func bsonWriteDoc(b *bytes.Buffer, m map[string]any) {
	start := b.Len()
	b.Write(make([]byte, 4)) // length placeholder
	// stable key order keeps output deterministic for tests
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		bsonWriteField(b, k, m[k])
	}
	b.WriteByte(0)
	binary.LittleEndian.PutUint32(b.Bytes()[start:], uint32(b.Len()-start))
}

func bsonWriteArray(b *bytes.Buffer, a []any) {
	start := b.Len()
	b.Write(make([]byte, 4))
	for i, v := range a {
		bsonWriteField(b, fmt.Sprintf("%d", i), v)
	}
	b.WriteByte(0)
	binary.LittleEndian.PutUint32(b.Bytes()[start:], uint32(b.Len()-start))
}

func bsonWriteField(b *bytes.Buffer, name string, v any) {
	switch v.(type) {
	case nil:
		b.WriteByte(bsonNull)
	case bool:
		b.WriteByte(bsonBool)
	case string:
		b.WriteByte(bsonString)
	case int32:
		b.WriteByte(bsonInt32)
	case int64, int:
		b.WriteByte(bsonInt64)
	case float64:
		b.WriteByte(bsonDouble)
	case time.Time:
		b.WriteByte(bsonDateTime)
	case map[string]any:
		b.WriteByte(bsonDoc)
	case []any:
		b.WriteByte(bsonArray)
	case mongoBinary:
		b.WriteByte(bsonBinary)
	case mongoOID:
		b.WriteByte(bsonObjectID)
	default:
		b.WriteByte(bsonNull)
	}
	b.WriteString(name)
	b.WriteByte(0)
	bsonWrite(b, v)
}

func bsonWriteNull(b *bytes.Buffer) { /* nothing follows */ }
func bsonWriteBool(b *bytes.Buffer, v bool) {
	if v {
		b.WriteByte(1)
	} else {
		b.WriteByte(0)
	}
}
func bsonWriteString(b *bytes.Buffer, s string) {
	binary.Write(b, binary.LittleEndian, int32(len(s)+1))
	b.WriteString(s)
	b.WriteByte(0)
}
func bsonWriteInt32(b *bytes.Buffer, v int32) { binary.Write(b, binary.LittleEndian, v) }
func bsonWriteInt64(b *bytes.Buffer, v int64) { binary.Write(b, binary.LittleEndian, v) }
func bsonWriteDouble(b *bytes.Buffer, v float64) {
	var u [8]byte
	binary.LittleEndian.PutUint64(u[:], math.Float64bits(v))
	b.Write(u[:])
}
func bsonWriteDateTime(b *bytes.Buffer, t time.Time) {
	bsonWriteInt64(b, t.UnixMilli())
}
func bsonWriteBinary(b *bytes.Buffer, v mongoBinary) {
	binary.Write(b, binary.LittleEndian, int32(len(v.Data)))
	b.WriteByte(v.Subtype)
	b.Write(v.Data)
}

func bsonWriteObjectID(b *bytes.Buffer, v mongoOID) {
	raw, err := hex.DecodeString(string(v))
	if err != nil || len(raw) != 12 {
		raw = make([]byte, 12)
	}
	b.Write(raw)
}

// bsonDecodeDoc decodes a BSON document starting at p[off:], returning the
// map and the byte offset just past the document.
func bsonDecodeDoc(p []byte, off int) (map[string]any, int) {
	if off+4 > len(p) {
		return nil, off
	}
	total := int(binary.LittleEndian.Uint32(p[off:]))
	if off+total > len(p) {
		return nil, off
	}
	end := off + total
	m := map[string]any{}
	pos := off + 4
	for pos < end {
		t := p[pos]
		pos++
		// cstring name
		n := bytes.IndexByte(p[pos:end], 0)
		if n < 0 {
			break
		}
		name := string(p[pos : pos+n])
		pos += n + 1
		v, next := bsonDecodeValue(p, pos, t)
		m[name] = v
		pos = next
	}
	return m, end
}

func bsonDecodeValue(p []byte, off int, t byte) (any, int) {
	switch t {
	case bsonDouble:
		return math.Float64frombits(binary.LittleEndian.Uint64(p[off:])), off + 8
	case bsonString:
		n := int(binary.LittleEndian.Uint32(p[off:]))
		return string(p[off+4 : off+4+n-1]), off + 4 + n
	case bsonDoc:
		m, next := bsonDecodeDoc(p, off)
		return m, next
	case bsonArray:
		a, next := bsonDecodeArray(p, off)
		return a, next
	case bsonBinary:
		n := int(binary.LittleEndian.Uint32(p[off:]))
		st := p[off+4]
		data := append([]byte(nil), p[off+5:off+5+n]...)
		return mongoBinary{Subtype: st, Data: data}, off + 5 + n
	case bsonObjectID:
		return fmt.Sprintf("%x", p[off:off+12]), off + 12
	case bsonBool:
		return p[off] != 0, off + 1
	case bsonDateTime:
		return time.UnixMilli(int64(binary.LittleEndian.Uint64(p[off:]))), off + 8
	case bsonNull:
		return nil, off
	case bsonInt32:
		return int32(binary.LittleEndian.Uint32(p[off:])), off + 4
	case bsonInt64:
		return int64(binary.LittleEndian.Uint64(p[off:])), off + 8
	default:
		// unknown type: skip the rest of the document defensively
		return nil, off
	}
}

func bsonDecodeArray(p []byte, off int) ([]any, int) {
	if off+4 > len(p) {
		return nil, off
	}
	total := int(binary.LittleEndian.Uint32(p[off:]))
	end := off + total
	a := []any{}
	pos := off + 4
	for pos < end {
		t := p[pos]
		pos++
		n := bytes.IndexByte(p[pos:end], 0)
		if n < 0 {
			break
		}
		pos += n + 1
		v, next := bsonDecodeValue(p, pos, t)
		a = append(a, v)
		pos = next
	}
	return a, end
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ---------------------------------------------------------------------------
// Wire protocol (OP_MSG, opcode 2013)

type mongoConn struct {
	conn    net.Conn
	reqID   int32
	user    string
	authDB  string
	timeout time.Duration
}

func mongoDial(host string, port int, timeout time.Duration) (*mongoConn, error) {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return nil, err
	}
	return &mongoConn{conn: c, timeout: timeout, authDB: "admin"}, nil
}

func (c *mongoConn) close() { c.conn.Close() }

// roundTrip sends one OP_MSG command document and decodes the reply document.
func (c *mongoConn) roundTrip(cmd map[string]any) (map[string]any, error) {
	body := bsonEncodeDocWithFlags(cmd)
	c.reqID++
	var msg bytes.Buffer
	binary.Write(&msg, binary.LittleEndian, int32(21+len(body))) // header+flagBits+kind+doc
	binary.Write(&msg, binary.LittleEndian, c.reqID)
	binary.Write(&msg, binary.LittleEndian, int32(0))    // responseTo
	binary.Write(&msg, binary.LittleEndian, int32(2013)) // OP_MSG
	binary.Write(&msg, binary.LittleEndian, int32(0))    // flagBits
	msg.WriteByte(0)                                     // section kind 0
	msg.Write(body)
	if c.timeout > 0 {
		c.conn.SetDeadline(time.Now().Add(c.timeout))
	}
	if _, err := c.conn.Write(msg.Bytes()); err != nil {
		return nil, err
	}
	hdr := make([]byte, 16)
	if _, err := io.ReadFull(c.conn, hdr); err != nil {
		return nil, err
	}
	msgLen := int(binary.LittleEndian.Uint32(hdr[0:]))
	if msgLen < 16 || msgLen > 64*1024*1024 {
		return nil, fmt.Errorf("invalid OP_MSG length %d", msgLen)
	}
	rest := make([]byte, msgLen-16)
	if _, err := io.ReadFull(c.conn, rest); err != nil {
		return nil, err
	}
	// rest: flagBits(4) + sections
	flags := binary.LittleEndian.Uint32(rest[0:])
	if flags&1 != 0 {
		return nil, fmt.Errorf("server requires more steps (checksum/session error)")
	}
	pos := 4
	var doc map[string]any
	for pos < len(rest) {
		kind := rest[pos]
		pos++
		switch kind {
		case 0:
			doc, pos = bsonDecodeDoc(rest, pos)
		case 1:
			// document sequence: int32 size + cstring identifier + docs
			size := int(binary.LittleEndian.Uint32(rest[pos:]))
			pos += 4 + size
		default:
			return nil, fmt.Errorf("unknown OP_MSG section kind %d", kind)
		}
	}
	if doc == nil {
		return nil, fmt.Errorf("empty OP_MSG reply")
	}
	return doc, nil
}

func bsonEncodeDocWithFlags(cmd map[string]any) []byte { return bsonEncode(cmd) }

// command sends a command to the given database, adding the required $db
// field, and fails on a server error reply (ok != 1).
func (c *mongoConn) command(db string, cmd map[string]any) (map[string]any, error) {
	cmd["$db"] = db
	doc, err := c.roundTrip(cmd)
	if err != nil {
		return doc, err
	}
	if !mongoOK(doc) {
		return doc, mongoErr(doc)
	}
	return doc, nil
}

// mongoOK reports whether a command reply carries ok == 1.
func mongoOK(doc map[string]any) bool {
	switch v := doc["ok"].(type) {
	case float64:
		return v == 1
	case int32:
		return v == 1
	case int64:
		return v == 1
	}
	return false
}

func mongoErr(doc map[string]any) error {
	msg, _ := doc["errmsg"].(string)
	if msg == "" {
		msg = "command failed"
	}
	var code int32
	switch v := doc["code"].(type) {
	case int32:
		code = v
	case int64:
		code = int32(v)
	case float64:
		code = int32(v)
	}
	if code != 0 {
		return fmt.Errorf("server error %s (code %d)", msg, code)
	}
	return fmt.Errorf("server error %s", msg)
}

// ---------------------------------------------------------------------------
// SCRAM-SHA-256 authentication

// pbkdf2SHA256 is SCRAM's Hi(): PBKDF2-HMAC-SHA256 (standard library only).
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	h := hmac.New(sha256.New, password)
	h.Write(salt)
	h.Write([]byte{0, 0, 0, 1})
	u := h.Sum(nil)
	out := append([]byte(nil), u...)
	for i := 1; i < iter; i++ {
		h = hmac.New(sha256.New, password)
		h.Write(u)
		u = h.Sum(nil)
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return out[:keyLen]
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

type scramClient struct {
	user, password  string
	nonce           string
	clientFirstBare string
	salted          []byte // SaltedPassword, kept for the server-final check
	authMessage     string
}

func newScramClient(user, password string) (*scramClient, error) {
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	nonce := fmt.Sprintf("%x", nonceBytes)
	cf := "n,,n=" + user + ",r=" + nonce
	return &scramClient{
		user: user, password: password, nonce: nonce,
		clientFirstBare: strings.TrimPrefix(cf, "n,,"),
	}, nil
}

func scramAttrs(msg string) map[string]string {
	attrs := map[string]string{}
	for _, part := range strings.Split(msg, ",") {
		if len(part) >= 2 && part[1] == '=' {
			attrs[part[:1]] = part[2:]
		}
	}
	return attrs
}

// step processes one server message and returns the next client message plus
// done. The first call consumes the server-first message and produces the
// client-final proof; a later call with the server-final "v=..." message
// verifies the server signature and reports done.
func (s *scramClient) step(serverMsg string) (string, bool, error) {
	attrs := scramAttrs(serverMsg)
	if v := attrs["v"]; v != "" {
		serverKey := hmacSHA256(s.salted, []byte("Server Key"))
		serverSignature := hmacSHA256(serverKey, []byte(s.authMessage))
		want, err := base64.StdEncoding.DecodeString(v)
		if err != nil || !hmac.Equal(want, serverSignature) {
			return "", false, fmt.Errorf("server authentication failed")
		}
		return "", true, nil
	}
	combinedNonce := attrs["r"]
	if !strings.HasPrefix(combinedNonce, s.nonce) {
		return "", false, fmt.Errorf("server nonce mismatch")
	}
	salt, err := base64.StdEncoding.DecodeString(attrs["s"])
	if err != nil {
		return "", false, fmt.Errorf("bad salt: %v", err)
	}
	iter := 4096
	fmt.Sscanf(attrs["i"], "%d", &iter)
	if iter < 1 {
		return "", false, fmt.Errorf("bad iteration count")
	}
	s.salted = pbkdf2SHA256([]byte(s.password), salt, iter, 32)
	clientKey := hmacSHA256(s.salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	clientFinalNoProof := "c=biws,r=" + combinedNonce
	s.authMessage = s.clientFirstBare + "," + serverMsg + "," + clientFinalNoProof
	clientSignature := hmacSHA256(storedKey[:], []byte(s.authMessage))
	proof := make([]byte, len(clientKey))
	for i := range clientKey {
		proof[i] = clientKey[i] ^ clientSignature[i]
	}
	return clientFinalNoProof + ",p=" + base64.StdEncoding.EncodeToString(proof), false, nil
}

// authenticate runs the SCRAM-SHA-256 conversation via saslStart/saslContinue.
func (c *mongoConn) authenticate(user, password, authDB string) error {
	sc, err := newScramClient(user, password)
	if err != nil {
		return err
	}
	payload := mongoBinary{Subtype: 0, Data: []byte("n,," + sc.clientFirstBare)}
	resp, err := c.command(authDB, map[string]any{
		"saslStart":     int32(1),
		"mechanism":     "SCRAM-SHA-256",
		"payload":       payload,
		"autoAuthorize": int32(1),
		"options":       map[string]any{"skipEmptyExchange": true},
	})
	if err != nil {
		return err
	}
	var convID int64
	if v, ok := resp["conversationId"].(int64); ok {
		convID = v
	} else if v, ok := resp["conversationId"].(int32); ok {
		convID = int64(v)
	}
	// Exchange: every server message carries a payload; step() verifies the
	// server-final "v=" signature and reports done once the conversation ends.
	for step := 0; step < 10; step++ {
		serverMsg, ok := resp["payload"].(mongoBinary)
		if !ok {
			return fmt.Errorf("missing payload in server reply")
		}
		next, isDone, err := sc.step(string(serverMsg.Data))
		if err != nil {
			return err
		}
		if isDone {
			return nil // step() validated the server signature
		}
		resp, err = c.command(authDB, map[string]any{
			"saslContinue":   int32(1),
			"conversationId": convID,
			"payload":        mongoBinary{Subtype: 0, Data: []byte(next)},
		})
		if err != nil {
			return err
		}
		if d, _ := resp["done"].(bool); d {
			// server finished with this reply: verify its final payload
			if p, ok := resp["payload"].(mongoBinary); ok {
				if _, _, err := sc.step(string(p.Data)); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return fmt.Errorf("SCRAM conversation did not complete")
}

// ---------------------------------------------------------------------------
// Command layer: mongo shell-ish shorthand db.<collection>.<method>(<json>),
// verbatim JSON command documents, `use`/`show`, and the interactive REPL.

// mongoOID marks an ObjectId hex value to be BSON-encoded as type 0x07.
type mongoOID string

type mongoOpts struct {
	host, user, password, authDB, database, format string
	port                                           int
	timeout                                        time.Duration
	tls                                            bool
	tlsInsecure                                    bool
	quiet                                          bool
}

func cmdMongodb(args []string) int {
	// --password accepts =SECRET / SECRET / bare (interactive hidden prompt),
	// same convention as clickhouse.
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
		&Flag{Name: "port", Long: "port", HasArg: true, ArgName: "PORT", Help: "server port (default: 27017)"},
		&Flag{Name: "user", Short: 'u', Long: "username", HasArg: true, ArgName: "USER", Help: "user name (triggers SCRAM-SHA-256 authentication)"},
		&Flag{Name: "authenticationDatabase", Long: "authenticationDatabase", HasArg: true, ArgName: "DB", Help: "database holding the user (default: admin)"},
		&Flag{Name: "database", Short: 'd', Long: "database", HasArg: true, ArgName: "DB", Help: "default database (default: test)"},
		&Flag{Name: "eval", Long: "eval", HasArg: true, ArgName: "CMD", Help: "run a command document or db.<col>.<method>(...) and exit"},
		&Flag{Name: "quiet", Short: 'q', Long: "quiet", Help: "hide the connection banner"},
		&Flag{Name: "json", Long: "json", Help: "pretty-print reply documents as JSON"},
		&Flag{Name: "tls", Long: "tls", Help: "connect with TLS (server certificate is verified)"},
		&Flag{Name: "tlsInsecure", Long: "tlsInsecure", Help: "do not verify the server certificate (use with --tls)"},
		&Flag{Name: "timeout", Long: "timeout", HasArg: true, ArgName: "SECS", Help: "operation timeout in seconds (default: 30)"},
	)
	if !parseOrDie("mongodb", f, rest) {
		return 2
	}
	if handleHelpVersion("mongodb", "[OPTIONS]", f) {
		return 0
	}
	if len(f.Positional) > 0 {
		warn("mongodb", "unexpected argument '%s' (use --eval or pipe commands on stdin)", f.Positional[0])
		return 1
	}

	o := &mongoOpts{host: "localhost", port: 27017, authDB: "admin", database: "test", timeout: 30 * time.Second}
	if f.Has("host") {
		o.host = f.Str("host")
	}
	if f.Has("port") {
		n, err := f.Int("port", 27017)
		if err != nil || n < 1 || n > 65535 {
			warn("mongodb", "invalid port '%s'", f.Str("port"))
			return 1
		}
		o.port = n
	}
	if f.Has("user") {
		o.user = f.Str("user")
	}
	if f.Has("authenticationDatabase") {
		o.authDB = f.Str("authenticationDatabase")
	}
	if f.Has("database") {
		o.database = f.Str("database")
	}
	if f.Has("timeout") {
		n, err := f.Int("timeout", 30)
		if err != nil || n < 0 {
			warn("mongodb", "invalid timeout '%s'", f.Str("timeout"))
			return 1
		}
		o.timeout = time.Duration(n) * time.Second
	}
	o.tls = f.Has("tls")
	o.tlsInsecure = f.Has("tlsInsecure")
	o.quiet = f.Has("quiet")
	o.format = f.Str("format")
	if promptPassword && isTerminal(os.Stdin) {
		line, hidden := readPassword("Enter password: ")
		password = line
		if !hidden {
			fmt.Fprintln(os.Stderr)
		}
	}
	o.password = password

	c, err := mongoDialOpts(o)
	if err != nil {
		warn("mongodb", "could not connect to %s:%d: %v", o.host, o.port, err)
		return 1
	}
	defer c.close()

	// handshake: learn the server version (isMaster works on every wire version)
	hello, err := c.command("admin", map[string]any{"isMaster": int32(1)})
	if err != nil {
		warn("mongodb", "handshake failed: %v", err)
		return 1
	}
	if o.user != "" {
		if err := c.authenticate(o.user, o.password, o.authDB); err != nil {
			warn("mongodb", "authentication failed: %v", err)
			return 1
		}
	}
	if !o.quiet {
		ver := mongoStr(hello, "serverVersion")
		if ver == "" {
			ver = mongoStr(hello, "version")
		}
		fmt.Fprintf(os.Stderr, "winux mongodb %s (pure Go client) — connected to %s:%d (MongoDB %s)\n",
			Version, o.host, o.port, ver)
	}

	if eval := f.Str("eval"); eval != "" {
		if err := mongoRun(o, c, eval); err != nil {
			fmt.Fprintf(os.Stderr, "mongodb: %s\n", err)
			return 1
		}
		return 0
	}
	if !isTerminal(os.Stdin) {
		data, err := io.ReadAll(stripBOM(os.Stdin))
		if err != nil {
			warn("mongodb", "%v", err)
			return 1
		}
		exitCode := 0
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if err := mongoRun(o, c, line); err != nil {
				fmt.Fprintf(os.Stderr, "mongodb: %s\n", err)
				if exitCode == 0 {
					exitCode = 1
				}
			}
		}
		return exitCode
	}
	return mongoRepl(o, c)
}

func mongoDialOpts(o *mongoOpts) (*mongoConn, error) {
	addr := net.JoinHostPort(o.host, strconv.Itoa(o.port))
	d := net.Dialer{Timeout: o.timeout}
	if o.tls {
		cfg := &tls.Config{ServerName: o.host, InsecureSkipVerify: o.tlsInsecure}
		conn, err := tls.DialWithDialer(&d, "tcp", addr, cfg)
		if err != nil {
			return nil, err
		}
		return &mongoConn{conn: conn, timeout: o.timeout, authDB: o.authDB}, nil
	}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &mongoConn{conn: conn, timeout: o.timeout, authDB: o.authDB}, nil
}

func mongoStr(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// mongoRun executes one REPL/batch line: `use x`, `show dbs|collections`,
// db.<col>.<method>(...), or a verbatim JSON command document.
func mongoRun(o *mongoOpts, c *mongoConn, line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	switch {
	case strings.HasPrefix(line, "use "):
		o.database = strings.TrimSpace(strings.TrimPrefix(line, "use "))
		return nil
	case strings.HasPrefix(line, "show dbs"):
		return mongoShowDbs(o, c)
	case strings.HasPrefix(line, "show collections") || strings.HasPrefix(line, "show tables"):
		return mongoShowCollections(o, c)
	case strings.HasPrefix(line, "db."):
		return mongoShorthand(o, c, line)
	case strings.HasPrefix(line, "{"):
		return mongoRawCommand(o, c, line)
	default:
		return fmt.Errorf("cannot parse '%s' (use db.<col>.<method>({...}) or a JSON command like {\"ping\":1})", line)
	}
}

func mongoShowDbs(o *mongoOpts, c *mongoConn) error {
	resp, err := c.command("admin", map[string]any{"listDatabases": int32(1)})
	if err != nil {
		return err
	}
	dbs, _ := resp["databases"].([]any)
	for _, d := range dbs {
		m, _ := d.(map[string]any)
		name := mongoStr(m, "name")
		size, _ := m["sizeOnDisk"].(float64)
		fmt.Fprintf(os.Stdout, "%-20s %10.1f MB\n", name, size/1024/1024)
	}
	return nil
}

func mongoShowCollections(o *mongoOpts, c *mongoConn) error {
	resp, err := c.command(o.database, map[string]any{"listCollections": int32(1)})
	if err != nil {
		return err
	}
	cursor, _ := resp["cursor"].(map[string]any)
	batch, _ := cursor["firstBatch"].([]any)
	for _, col := range batch {
		m, _ := col.(map[string]any)
		fmt.Fprintln(os.Stdout, mongoStr(m, "name"))
	}
	return nil
}

// mongoShorthand parses db.<collection>.<method>(<json args>) and maps the
// method onto the corresponding server command.
func mongoShorthand(o *mongoOpts, c *mongoConn, line string) error {
	body := strings.TrimPrefix(line, "db.")
	dot := strings.IndexByte(body, '.')
	if dot <= 0 {
		return fmt.Errorf("cannot parse '%s'", line)
	}
	col := body[:dot]
	call := body[dot+1:]
	paren := strings.IndexByte(call, '(')
	if paren <= 0 || !strings.HasSuffix(call, ")") {
		return fmt.Errorf("expected db.%s.<method>(<json>)", col)
	}
	method := strings.TrimSpace(call[:paren])
	inner := strings.TrimSpace(call[paren+1 : len(call)-1])

	args, err := parseMongoJSONArgs(inner)
	if err != nil {
		return fmt.Errorf("bad JSON arguments: %v", err)
	}

	var cmd map[string]any
	switch method {
	case "find", "findOne":
		filter := map[string]any{}
		if len(args) > 0 {
			filter = mongoMap(args[0])
		}
		cmd = map[string]any{"find": col, "filter": filter}
		if len(args) > 1 {
			for k, v := range mongoMap(args[1]) {
				cmd[k] = v
			}
		}
		if method == "findOne" {
			cmd["limit"] = int64(1)
		}
		resp, err := c.command(o.database, cmd)
		if err != nil {
			return err
		}
		batch := mongoFirstBatch(resp)
		if method == "findOne" {
			if len(batch) > 0 {
				return mongoPrintDoc(o, batch[0])
			}
			return nil
		}
		return mongoPrintBatch(o, batch)
	case "insertOne", "insertMany":
		if len(args) == 0 {
			return fmt.Errorf("%s requires a document", method)
		}
		docs := mongoDocs(args[0])
		cmd = map[string]any{"insert": col, "documents": docs}
	case "updateOne", "updateMany", "replaceOne":
		if len(args) < 2 {
			return fmt.Errorf("%s requires (filter, update)", method)
		}
		u := map[string]any{"q": mongoMap(args[0]), "u": mongoMap(args[1])}
		if method == "updateOne" || method == "replaceOne" {
			u["multi"] = false
		} else {
			u["multi"] = true
		}
		if len(args) > 2 {
			for k, v := range mongoMap(args[2]) {
				u[k] = v
			}
		}
		cmd = map[string]any{"update": col, "updates": []any{u}}
	case "deleteOne", "deleteMany":
		if len(args) == 0 {
			return fmt.Errorf("%s requires a filter", method)
		}
		limit := int64(0)
		if method == "deleteOne" {
			limit = 1
		}
		cmd = map[string]any{"delete": col, "deletes": []any{map[string]any{"q": mongoMap(args[0]), "limit": limit}}}
	case "aggregate":
		pipeline := []any{}
		if len(args) > 0 {
			pipeline = mongoDocs(args[0])
		}
		cmd = map[string]any{"aggregate": col, "pipeline": pipeline, "cursor": map[string]any{}}
	case "countDocuments":
		filter := map[string]any{}
		if len(args) > 0 {
			filter = mongoMap(args[0])
		}
		cmd = map[string]any{"count": col, "query": filter}
	case "distinct":
		if len(args) == 0 {
			return fmt.Errorf("distinct requires a field")
		}
		cmd = map[string]any{"distinct": col, "key": mongoStr(args[0].(map[string]any), "_k")}
		if len(args) > 1 {
			cmd["query"] = mongoMap(args[1])
		}
	case "drop":
		cmd = map[string]any{"drop": col}
	case "create":
		cmd = map[string]any{"create": col}
	case "createIndexes":
		if len(args) == 0 {
			return fmt.Errorf("createIndexes requires index documents")
		}
		cmd = map[string]any{"createIndexes": col, "indexes": mongoDocs(args[0])}
	default:
		return fmt.Errorf("unsupported method '%s' (supported: find, findOne, insertOne, insertMany, updateOne, updateMany, replaceOne, deleteOne, deleteMany, aggregate, countDocuments, distinct, drop, create, createIndexes)", method)
	}
	resp, err := c.command(o.database, cmd)
	if err != nil {
		return err
	}
	// CRUD commands return cursor.firstBatch; summaries show n/nInserted/...
	if batch := mongoFirstBatch(resp); len(batch) > 0 {
		return mongoPrintBatch(o, batch)
	}
	return mongoPrintDoc(o, resp)
}

// mongoRawCommand sends a verbatim JSON command document.
func mongoRawCommand(o *mongoOpts, c *mongoConn, line string) error {
	var doc map[string]any
	if err := json.Unmarshal([]byte(line), &doc); err != nil {
		return fmt.Errorf("bad command JSON: %v", err)
	}
	resp, err := c.command(o.database, normalizeJSON(doc).(map[string]any))
	if err != nil {
		return err
	}
	if batch := mongoFirstBatch(resp); len(batch) > 0 {
		return mongoPrintBatch(o, batch)
	}
	return mongoPrintDoc(o, resp)
}

func mongoFirstBatch(resp map[string]any) []any {
	cursor, _ := resp["cursor"].(map[string]any)
	if cursor == nil {
		return nil
	}
	batch, _ := cursor["firstBatch"].([]any)
	return batch
}

func mongoPrintBatch(o *mongoOpts, batch []any) error {
	for _, d := range batch {
		if err := mongoPrintDoc(o, d); err != nil {
			return err
		}
	}
	return nil
}

func mongoPrintDoc(o *mongoOpts, d any) error {
	doc, ok := d.(map[string]any)
	if !ok {
		doc = map[string]any{"value": d}
	}
	if o.format == "" {
		o.format = "json"
	}
	switch o.format {
	case "json":
		b, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(b))
	case "pretty":
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, string(b))
	default:
		return fmt.Errorf("unknown format '%s'", o.format)
	}
	return nil
}

func mongoMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func mongoDocs(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	if m, ok := v.(map[string]any); ok {
		return []any{m}
	}
	return nil
}

// parseMongoJSONArgs parses the parenthesised argument list of a shorthand
// call. Values are JSON (unquoted keys are tolerated, as in the mongo shell);
// ObjectId("hex") / ObjectId('hex') are translated to BSON ObjectIds.
func parseMongoJSONArgs(inner string) ([]any, error) {
	inner = mongoObjectIDRe.ReplaceAllStringFunc(inner, func(s string) string {
		hex := strings.Trim(s[len("ObjectId("):len(s)-1], `"'`)
		return `{"$oid":"` + hex + `"}`
	})
	inner = mongoUnquoteKeyRe.ReplaceAllString(inner, `$1"$2"$3`)
	if strings.TrimSpace(inner) == "" {
		return nil, nil
	}
	var args []any
	if err := json.Unmarshal([]byte("["+inner+"]"), &args); err != nil {
		return nil, err
	}
	out := make([]any, 0, len(args))
	for _, a := range args {
		out = append(out, normalizeJSON(a))
	}
	return out, nil
}

// normalizeJSON converts decoded JSON values into BSON-friendly ones and
// honours the extended-JSON wrappers $oid / $numberLong / $date.
func normalizeJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 1 {
			if s, ok := t["$oid"].(string); ok {
				return mongoOID(s)
			}
			if s, ok := t["$numberLong"].(string); ok {
				if n, err := parseInt64(s); err == nil {
					return n
				}
			}
			if s, ok := t["$date"].(string); ok {
				if d, err := time.Parse(time.RFC3339, s); err == nil {
					return d
				}
			}
		}
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeJSON(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			out = append(out, normalizeJSON(e))
		}
		return out
	default:
		return v
	}
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

var mongoObjectIDRe = regexp.MustCompile(`ObjectId\(["']([0-9a-fA-F]{24})["']\)`)

// mongoUnquoteKeyRe quotes unquoted object keys: `{age: 30}` -> `{"age": 30}`.
// It matches a key only after '{' or ',' so values inside strings are untouched.
var mongoUnquoteKeyRe = regexp.MustCompile(`([{,]\s*)([A-Za-z_$][A-Za-z0-9_$]*)(\s*:)`)

// mongoRepl is the interactive REPL (mongosh-style `dbname>` prompt).
func mongoRepl(o *mongoOpts, c *mongoConn) int {
	in, cleanup := consoleInput()
	defer cleanup()
	rd := bufio.NewReader(stripBOM(in))
	exitCode := 0
	for {
		fmt.Fprintf(os.Stdout, "%s> ", o.database)
		line, err := rd.ReadString('\n')
		if len(line) > 0 {
			t := strings.TrimSpace(line)
			switch {
			case t == "":
				// empty line: just re-prompt
			case strings.EqualFold(t, "exit"), strings.EqualFold(t, "quit"), t == `\q`:
				fmt.Fprintln(os.Stdout)
				return exitCode
			case strings.EqualFold(t, "help"), t == `\h`:
				fmt.Fprintln(os.Stdout, "mongodb help: use <db>; show dbs; show collections;")
				fmt.Fprintln(os.Stdout, "  db.<col>.<method>({...}) with find/findOne/insertOne/insertMany/updateOne/updateMany/replaceOne/")
				fmt.Fprintln(os.Stdout, "  deleteOne/deleteMany/aggregate/countDocuments/distinct/drop/create/createIndexes;")
				fmt.Fprintln(os.Stdout, "  or a raw JSON command like {\"ping\":1}. \\q/exit to quit.")
			default:
				if err := mongoRun(o, c, t); err != nil {
					fmt.Fprintf(os.Stderr, "mongodb: %s\n", err)
					if exitCode == 0 {
						exitCode = 1
					}
				}
			}
		}
		if err == io.EOF {
			fmt.Fprintln(os.Stdout)
			return exitCode
		}
	}
}
