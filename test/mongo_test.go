package core_test

// mongodb is exercised against an in-test fake MongoDB wire server: it speaks
// OP_MSG, answers isMaster, runs a real server-side SCRAM-SHA-256 exchange
// (verifying the client's proof) and replies to find/count/listCommands etc.
// The client under test is winux/core.Execute("mongodb", ...).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"winux/core"
)

type mongoFake struct {
	ln             net.Listener
	mu             sync.Mutex
	commands       []string // decoded command names in arrival order
	failAuth       bool
	lastFind       map[string]any
	serverVerifier func(string) string
	t              *testing.T
}

func newMongoFake(t *testing.T, failAuth bool) *mongoFake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &mongoFake{ln: ln, failAuth: failAuth, t: t}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *mongoFake) addr() string { return f.ln.Addr().String() }

func (f *mongoFake) port(t *testing.T) string {
	t.Helper()
	_, port, err := net.SplitHostPort(f.addr())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// The test-side copies of the SCRAM primitives (core's are unexported).
func pbkdf2Test(password string, salt []byte, iter, keyLen int) []byte {
	h := hmac.New(sha256.New, []byte(password))
	h.Write(salt)
	h.Write([]byte{0, 0, 0, 1})
	u := h.Sum(nil)
	out := append([]byte(nil), u...)
	for i := 1; i < iter; i++ {
		h = hmac.New(sha256.New, []byte(password))
		h.Write(u)
		u = h.Sum(nil)
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return out[:keyLen]
}

func hmacSHA256Test(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

func (f *mongoFake) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *mongoFake) handle(conn net.Conn) {
	defer conn.Close()
	for {
		hdr := make([]byte, 16)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		msgLen := int(binary.LittleEndian.Uint32(hdr[0:]))
		opcode := binary.LittleEndian.Uint32(hdr[12:])
		body := make([]byte, msgLen-16)
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		if opcode != 2013 {
			return
		}
		pos := 4 // flagBits
		doc := map[string]any{}
		if body[pos] == 0 {
			pos++
			doc, _ = core.BsonDecodeDoc(body[pos:])
		}
		f.mu.Lock()
		for name := range doc {
			if name != "$db" {
				f.commands = append(f.commands, name)
			}
		}
		f.mu.Unlock()
		f.reply(conn, doc)
	}
}

func (f *mongoFake) reply(conn net.Conn, cmd map[string]any) {
	var resp map[string]any
	switch {
	case cmd["isMaster"] != nil:
		resp = map[string]any{"ok": 1.0, "ismaster": true, "maxWireVersion": int32(13), "minWireVersion": int32(0), "serverVersion": "6.0.0"}
	case cmd["saslStart"] != nil:
		if f.failAuth {
			resp = map[string]any{"ok": 0.0, "errmsg": "Authentication failed.", "code": int32(18)}
			break
		}
		payload, _ := cmd["payload"].(core.MongoBinary)
		sf, verifier := fakeSCRAMStep(string(payload.Data))
		resp = map[string]any{"ok": 1.0, "conversationId": int64(1), "payload": core.MongoBinary{Subtype: 0, Data: []byte(sf)}, "done": false}
		f.mu.Lock()
		f.serverVerifier = verifier
		f.mu.Unlock()
	case cmd["saslContinue"] != nil:
		payload, _ := cmd["payload"].(core.MongoBinary)
		f.mu.Lock()
		verifier := f.serverVerifier
		f.mu.Unlock()
		v := verifier(string(payload.Data))
		if v == "" {
			resp = map[string]any{"ok": 0.0, "errmsg": "bad proof", "code": int32(18)}
		} else {
			resp = map[string]any{"ok": 1.0, "conversationId": int64(1), "payload": core.MongoBinary{Subtype: 0, Data: []byte(v)}, "done": true}
		}
	case cmd["ping"] != nil:
		resp = map[string]any{"ok": 1.0}
	case cmd["find"] != nil:
		f.mu.Lock()
		f.lastFind = cmd
		f.mu.Unlock()
		resp = map[string]any{
			"ok": 1.0,
			"cursor": map[string]any{
				"id": int64(0), "ns": "test.users",
				"firstBatch": []any{
					map[string]any{"_id": int32(1), "name": "alice", "age": int32(30)},
					map[string]any{"_id": int32(2), "name": "bob", "age": int32(25)},
				},
			},
		}
	case cmd["count"] != nil:
		resp = map[string]any{"ok": 1.0, "n": 3.0}
	case cmd["listDatabases"] != nil:
		resp = map[string]any{"ok": 1.0, "databases": []any{
			map[string]any{"name": "admin", "sizeOnDisk": 4096.0, "empty": false},
			map[string]any{"name": "test", "sizeOnDisk": 8192.0, "empty": false},
		}}
	case cmd["listCollections"] != nil:
		resp = map[string]any{"ok": 1.0, "cursor": map[string]any{
			"id": int64(0), "firstBatch": []any{map[string]any{"name": "users", "type": "collection"}},
		}}
	default:
		resp = map[string]any{"ok": 0.0, "errmsg": "test error: unknown command", "code": int32(2)}
	}
	body := core.BsonEncode(resp)
	var msg []byte
	var h [16]byte
	binary.LittleEndian.PutUint32(h[0:], uint32(21+len(body))) // header+flagBits+kind+doc
	binary.LittleEndian.PutUint32(h[4:], 1)
	binary.LittleEndian.PutUint32(h[8:], 0)
	binary.LittleEndian.PutUint32(h[12:], 2013)
	msg = append(msg, h[:]...)
	var f4 [4]byte
	msg = append(msg, f4[:]...)
	msg = append(msg, 0)
	msg = append(msg, body...)
	conn.Write(msg)
}

func fakeSCRAMStep(clientFirst string) (string, func(string) string) {
	parts := strings.Split(clientFirst, ",")
	attrs := map[string]string{}
	for _, p := range parts {
		if len(p) >= 2 && p[1] == '=' {
			attrs[p[:1]] = p[2:]
		}
	}
	nonce := attrs["r"]
	salt := []byte("0123456789abcdef")[:16]
	serverNonce := nonce + "SERVERSIDE"
	serverFirst := fmt.Sprintf("r=%s,s=%s,i=4096", serverNonce, base64.StdEncoding.EncodeToString(salt))
	return serverFirst, func(clientFinal string) string {
		salted := pbkdf2Test("secretpw", salt, 4096, 32)
		clientKey := hmacSHA256Test(salted, []byte("Client Key"))
		storedKey := sha256.Sum256(clientKey)
		clientFirstBare := strings.TrimPrefix(clientFirst, "n,,")
		authMsg := clientFirstBare + "," + serverFirst + "," + "c=biws,r=" + serverNonce
		sig := hmacSHA256Test(storedKey[:], []byte(authMsg))
		proof := make([]byte, len(clientKey))
		for i := range clientKey {
			proof[i] = clientKey[i] ^ sig[i]
		}
		want := "p=" + base64.StdEncoding.EncodeToString(proof)
		if !strings.HasSuffix(clientFinal, want) {
			return ""
		}
		serverKey := hmacSHA256Test(salted, []byte("Server Key"))
		serverSig := hmacSHA256Test(serverKey, []byte(authMsg))
		return "v=" + base64.StdEncoding.EncodeToString(serverSig)
	}
}

func TestMongoPing(t *testing.T) {
	f := newMongoFake(t, false)
	code, out, errOut := core.Execute("mongodb",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q", "--eval", `{"ping":1}`},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, `"ok"`) {
		t.Fatalf("out=%q", out)
	}
}

func TestMongoFindShorthand(t *testing.T) {
	f := newMongoFake(t, false)
	code, out, errOut := core.Execute("mongodb",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q", "--eval", `db.users.find({"age":30})`},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "alice") || !strings.Contains(out, "bob") {
		t.Fatalf("out=%q", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	filter, _ := f.lastFind["filter"].(map[string]any)
	if filter["age"] != 30.0 {
		t.Fatalf("filter=%v", f.lastFind["filter"])
	}
}

func TestMongoFindOne(t *testing.T) {
	f := newMongoFake(t, false)
	code, out, errOut := core.Execute("mongodb",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q", "--eval", `db.users.findOne({})`},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "alice") || strings.Contains(out, "bob") {
		t.Fatalf("findOne should return a single doc, out=%q", out)
	}
}

func TestMongoAuth(t *testing.T) {
	f := newMongoFake(t, false)
	code, out, errOut := core.Execute("mongodb",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-u", "alice", "--password", "secretpw", "-q", "--eval", `{"ping":1}`},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, `"ok"`) {
		t.Fatalf("out=%q", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for _, c := range f.commands {
		if c == "saslStart" || c == "saslContinue" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected SCRAM commands, got %v", f.commands)
	}
}

func TestMongoBadPassword(t *testing.T) {
	f := newMongoFake(t, false)
	code, _, errOut := core.Execute("mongodb",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-u", "alice", "--password", "wrongpw", "-q", "--eval", `{"ping":1}`},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1 (bad proof must fail)", code)
	}
	if !strings.Contains(errOut, "authentication failed") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestMongoCommandError(t *testing.T) {
	f := newMongoFake(t, false)
	code, _, errOut := core.Execute("mongodb",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q", "--eval", `{"boom":1}`},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errOut, "test error") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestMongoShowDbsStdin(t *testing.T) {
	f := newMongoFake(t, false)
	code, out, errOut := core.Execute("mongodb",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q"},
		strings.NewReader("show dbs\n"))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "admin") || !strings.Contains(out, "test") {
		t.Fatalf("out=%q", out)
	}
}

func TestMongoConnectionRefused(t *testing.T) {
	code, _, errOut := core.Execute("mongodb",
		[]string{"-h", "127.0.0.1", "--port", "1", "--timeout", "2", "-q", "--eval", `{"ping":1}`},
		strings.NewReader(""))
	if code != 1 {
		t.Fatalf("code=%d want 1", code)
	}
	if !strings.Contains(errOut, "mongodb:") {
		t.Fatalf("stderr=%q", errOut)
	}
}

func TestMongoBadArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"unknown option", []string{"--nope"}, 2},
		{"bad port", []string{"--port", "abc"}, 1},
		{"unexpected positional", []string{"junk"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := core.Execute("mongodb", tc.args, strings.NewReader(""))
			if code != tc.code {
				t.Fatalf("code=%d want %d", code, tc.code)
			}
		})
	}
}

func TestMongoObjectIdShorthand(t *testing.T) {
	f := newMongoFake(t, false)
	oid := "507f1f77bcf86cd799439011"
	code, _, errOut := core.Execute("mongodb",
		[]string{"-h", "127.0.0.1", "--port", f.port(t), "-q", "--eval", fmt.Sprintf(`db.users.findOne({_id: ObjectId("%s")})`, oid)},
		strings.NewReader(""))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	// the fake replies firstBatch with the same docs regardless; the point is
	// that ObjectId() was translated into a valid command, not a parse error
}
