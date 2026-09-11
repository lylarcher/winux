package core

// sh is a small POSIX-flavoured shell implemented on top of the winux command
// table. It exists because neither PowerShell nor cmd.exe support heredoc
// syntax (`<<EOF … EOF`), so winux provides its own shell surface where
// redirects, heredocs, pipes, a handful of variables and quoting rules work.
//
// Supported subset (documented in README):
//   - one- and multi-command lines, `;` separators, `&&` / `||` conditionals
//   - pipelines with `|` (each stage runs as a winux.exe subprocess)
//   - redirections `>`, `>>`, `<`, `2>`, `2>>`, `2>&1`
//   - heredocs `<<EOF` (variable-expanded body) and `<<'EOF'` (literal body)
//   - single- and double-quoted words, `\` escapes, `#` comments
//   - `$VAR` / `${VAR}` / `$?` expansion
//   - `NAME=value` assignments (persistent when standalone, per-command when
//     followed by a command)
//   - builtins: cd, exit, :, true, false
//
// Not implemented: `$(...)`, `$(())`, globals are handled by each command
// itself (globs/tilde), background `&` runs synchronously with a warning.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func init() {
	addCmd("sh", "a small POSIX-style shell: heredocs, pipes, variables", cmdSh)
}

// ---------------------------------------------------------------- state ----

type shCtx struct {
	lastStatus int
	exitCode   *int // set by the `exit` builtin
	bgWarned   bool
}

// ------------------------------------------------------------- tokenizer ----

type shTok struct {
	text    string
	quoted  bool // was a quoted token (used by heredoc delimiters)
	op      int  // 0 = word, else operator
	fd      int  // for redir ops: 0 = stdout/stdin, 2 = stderr
	append  bool // for > / 2>
	heredoc bool // <<
	kind    int  // 1 = pipe, 2 = semi, 3 = &&, 4 = ||, 5 = &, 6 = redir
}

const (
	shOpNone = iota
	shOpPipe
	shOpSemi
	shOpAnd
	shOpOr
	shOpBg
	shOpRedir
	shRedirIn        = 1 // <
	shRedirOut       = 2 // >
	shRedirApp       = 3 // >>
	shRedirHere      = 4 // <<
	shRedirErrOut    = 5 // 2>
	shRedirErrApp    = 6 // 2>>
	shRedirErrMirror = 7 // 2>&1
)

func isShNameStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isShIdent(c byte) bool     { return isShNameStart(c) || c >= '0' && c <= '9' }

// shExpand replaces $VAR, ${VAR} and $? in s using the shell context. Unknown
// variables expand to the empty string, like bash.
func shExpand(ctx *shCtx, s string) string {
	if !strings.ContainsRune(s, '$') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' || i+1 >= len(s) {
			b.WriteByte(c)
			i++
			continue
		}
		switch {
		case s[i+1] == '?':
			b.WriteString(strconv.Itoa(ctx.lastStatus))
			i += 2
		case s[i+1] == '{':
			j := strings.IndexByte(s[i+2:], '}')
			if j < 0 {
				b.WriteString(s[i:])
				i = len(s)
				continue
			}
			b.WriteString(os.Getenv(s[i+2 : i+2+j]))
			i += 2 + j + 1
		case isShNameStart(s[i+1]):
			j := i + 1
			for j < len(s) && isShIdent(s[j]) {
				j++
			}
			b.WriteString(os.Getenv(s[i+1 : j]))
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// shTokenize splits a command line into tokens. Words inside single quotes are
// literal; double quotes expand variables; `\` escapes the next character;
// `#` starts a comment at the beginning of a token. `$VAR`/`$?` are expanded
// immediately using ctx (this is why lines are tokenised/executed one at a
// time: `$?` must see the status of the previous line).
func shTokenize(line string, ctx *shCtx) []shTok {
	var toks []shTok
	var w strings.Builder
	wQuoted := false // the word being built contains at least one quoted part
	flush := func() {
		if w.Len() > 0 || wQuoted {
			toks = append(toks, shTok{text: w.String(), quoted: wQuoted})
			w.Reset()
			wQuoted = false
		}
	}
	emitOp := func(op int, fd int, app bool, here bool, kind int) {
		flush()
		toks = append(toks, shTok{op: op, fd: fd, append: app, heredoc: here, kind: kind})
	}
	i := 0
	for i < len(line) {
		c := line[i]
		switch {
		case c == ' ' || c == '\t':
			flush()
			i++
		case c == '#':
			flush()
			i = len(line) // rest of the line is a comment
		case c == '\'':
			// a quoted part joins the current word (msg='a b' is ONE word)
			j := i + 1
			for j < len(line) && line[j] != '\'' {
				j++
			}
			if j < len(line) {
				w.WriteString(line[i+1 : j])
				wQuoted = true
				i = j + 1
			} else { // unterminated quote: take the rest literally
				w.WriteString(line[i+1:])
				wQuoted = true
				i = len(line)
			}
		case c == '"':
			flush()
			j := i + 1
			var inner strings.Builder
			for j < len(line) && line[j] != '"' {
				if line[j] == '\\' && j+1 < len(line) {
					switch line[j+1] {
					case '"', '\\', '$', '`':
						inner.WriteByte(line[j+1])
						j += 2
						continue
					}
				}
				inner.WriteByte(line[j])
				j++
			}
			w.WriteString(shExpand(ctx, inner.String()))
			wQuoted = true
			if j < len(line) {
				i = j + 1
			} else {
				i = len(line)
			}
		case c == '\\':
			if i+1 < len(line) {
				n := line[i+1]
				// backslash only escapes shell metacharacters; Windows paths
				// like C:\Users\... and \\server\share keep their backslashes
				// intact (note: `\` itself is NOT in the set, so \\ stays \\)
				if strings.ContainsRune("|&;<>()$ \"'`", rune(n)) {
					w.WriteByte(n)
					i += 2
				} else {
					w.WriteByte(c)
					i++
				}
			} else {
				w.WriteByte(c)
				i++
			}
		case c == '$':
			// expand inline (outside quotes)
			j := i + 1
			name := ""
			if j < len(line) && line[j] == '{' {
				k := strings.IndexByte(line[j+1:], '}')
				if k >= 0 {
					name = line[j+1 : j+1+k]
					i = j + 1 + k + 1
				} else {
					i = len(line)
				}
			} else if j < len(line) && line[j] == '?' {
				name = "?"
				i = j + 1
			} else if j < len(line) && isShNameStart(line[j]) {
				k := j
				for k < len(line) && isShIdent(line[k]) {
					k++
				}
				name = line[j:k]
				i = k
			} else {
				w.WriteByte(c)
				i++
			}
			if name != "" {
				if name == "?" {
					w.WriteString(strconv.Itoa(ctx.lastStatus))
				} else {
					w.WriteString(os.Getenv(name))
				}
			}
		case c == '|':
			if i+1 < len(line) && line[i+1] == '|' {
				emitOp(shOpOr, 0, false, false, 0)
				i += 2
			} else {
				emitOp(shOpPipe, 0, false, false, 0)
				i++
			}
		case c == '&':
			if i+1 < len(line) && line[i+1] == '&' {
				emitOp(shOpAnd, 0, false, false, 0)
				i += 2
			} else {
				emitOp(shOpBg, 0, false, false, 0)
				i++
			}
		case c == ';':
			emitOp(shOpSemi, 0, false, false, 0)
			i++
		case c == '>' || c == '<':
			// glued fd (e.g. 2>f, 1>f, 2>&1) — no whitespace between the
			// digit and the operator; must peek before flushing the word
			fd := 0
			if w.Len() == 1 && (w.String() == "1" || w.String() == "2") {
				fd, _ = strconv.Atoi(w.String())
				w.Reset()
			}
			flush()
			if c == '<' {
				if i+1 < len(line) && line[i+1] == '<' {
					emitOp(shOpRedir, 0, false, true, shRedirHere)
					i += 2
				} else {
					emitOp(shOpRedir, fd, false, false, shRedirIn)
					i++
				}
				continue
			}
			// '>'
			if i+1 < len(line) && line[i+1] == '>' {
				if i+2 < len(line) && line[i+2] == '&' && fd == 2 {
					emitOp(shOpRedir, 2, false, false, shRedirErrMirror)
					i += 3
					if i < len(line) && (line[i] == '1' || line[i] == '2') {
						i++ // swallow the target of 2>&1 / 2>&2
					}
				} else if fd == 2 {
					emitOp(shOpRedir, 2, true, false, shRedirErrApp)
					i += 2
				} else {
					emitOp(shOpRedir, 0, true, false, shRedirApp)
					i += 2
				}
				continue
			}
			if i+1 < len(line) && line[i+1] == '&' && fd == 2 {
				emitOp(shOpRedir, 2, false, false, shRedirErrMirror)
				i += 2
				if i < len(line) && (line[i] == '1' || line[i] == '2') {
					i++ // swallow the target of 2>&1 / 2>&2
				}
				continue
			}
			if fd == 2 {
				emitOp(shOpRedir, 2, false, false, shRedirErrOut)
			} else {
				emitOp(shOpRedir, 0, false, false, shRedirOut)
			}
			i++
		default:
			w.WriteByte(c)
			i++
		}
	}
	flush()
	return toks
}

// ----------------------------------------------------------- parse (line) ---

type shHeredoc struct {
	delim  string
	quoted bool
	body   string
}

type shRedir struct {
	kind int // shRedir* or 0
	path string
}

type shCmd struct {
	name     string
	args     []string // without the name
	env      []string // NAME=value pairs (expanded at exec)
	in, out  shRedir
	err2     shRedir
	heredocs []shHeredoc
}

type shUnit struct {
	cmds []shCmd
	cond int // 0 = sequential, 1 = after &&, 2 = after ||, 3 = after &
	// cond describes the operator that precedes this unit on the line.
}

const (
	shCondSeq = iota
	shCondAnd
	shCondOr
	shCondBg
)

func isShAssignment(w string) bool {
	for i := 0; i < len(w); i++ {
		c := w[i]
		if c == '=' {
			return i > 0
		}
		if i == 0 {
			if !isShNameStart(c) {
				return false
			}
		} else if !isShIdent(c) {
			return false
		}
	}
	return false
}

// shParseLine turns tokens (without heredoc bodies) into units. It consumes
// redirection targets as the token following each redir operator.
func shParseLine(toks []shTok) ([]shUnit, error) {
	var units []shUnit
	cur := shCmd{}
	cmds := []shCmd{}
	cond := shCondSeq
	flushCmd := func() {
		if cur.name == "" && len(cur.args) == 0 && len(cur.env) == 0 {
			return
		}
		cmds = append(cmds, cur)
		cur = shCmd{}
	}
	flushUnit := func(c int) {
		flushCmd()
		if len(cmds) == 0 {
			return // nothing accumulated on this side of the operator
		}
		units = append(units, shUnit{cmds: cmds, cond: c})
		cmds = nil
	}
	nextTarget := func(i int) (string, bool, error) {
		if i >= len(toks) || toks[i].op != 0 {
			return "", false, fmt.Errorf("sh: syntax error: missing operand after redirection")
		}
		return toks[i].text, toks[i].quoted, nil
	}
	i := 0
	for i < len(toks) {
		t := toks[i]
		switch t.op {
		case 0: // word
			if cur.name == "" && len(cur.args) == 0 && isShAssignment(t.text) {
				cur.env = append(cur.env, t.text)
			} else if cur.name == "" {
				cur.name = t.text
			} else {
				cur.args = append(cur.args, t.text)
			}
			i++
		case shOpPipe:
			flushCmd()
			i++
		case shOpSemi:
			flushUnit(cond)
			cond = shCondSeq
			i++
		case shOpAnd:
			flushUnit(cond)
			cond = shCondAnd
			i++
		case shOpOr:
			flushUnit(cond)
			cond = shCondOr
			i++
		case shOpBg:
			flushUnit(cond)
			cond = shCondBg
			i++
		case shOpRedir:
			switch t.kind {
			case shRedirHere:
				delim, quoted, err := nextTarget(i + 1)
				if err != nil {
					return nil, err
				}
				cur.heredocs = append(cur.heredocs, shHeredoc{delim: delim, quoted: quoted})
				i += 2
			case shRedirIn:
				p, _, err := nextTarget(i + 1)
				if err != nil {
					return nil, err
				}
				cur.in = shRedir{kind: shRedirIn, path: p}
				i += 2
			case shRedirOut:
				p, _, err := nextTarget(i + 1)
				if err != nil {
					return nil, err
				}
				cur.out = shRedir{kind: shRedirOut, path: p}
				i += 2
			case shRedirApp:
				p, _, err := nextTarget(i + 1)
				if err != nil {
					return nil, err
				}
				cur.out = shRedir{kind: shRedirApp, path: p}
				i += 2
			case shRedirErrOut, shRedirErrApp:
				p, _, err := nextTarget(i + 1)
				if err != nil {
					return nil, err
				}
				cur.err2 = shRedir{kind: t.kind, path: p}
				i += 2
			case shRedirErrMirror:
				cur.err2 = shRedir{kind: shRedirErrMirror}
				i++
			default:
				i++
			}
		default:
			i++
		}
	}
	flushUnit(cond)
	if len(units) == 0 {
		units = append(units, shUnit{cmds: cmds, cond: cond})
	}
	return units, nil
}

// ------------------------------------------------------------ heredocs ------

// shCollectHeredocs pulls the body lines of every pending heredoc from the
// line reader (bash semantics: the bodies follow the command line verbatim).
// Returns the new reader position.
func shCollectHeredocs(units []shUnit, rl func() (string, bool)) {
	for i := range units {
		for j := range units[i].cmds {
			c := &units[i].cmds[j]
			if len(c.heredocs) == 0 {
				continue
			}
			for k := range c.heredocs {
				h := &c.heredocs[k]
				var body []string
				for {
					ln, ok := rl()
					if !ok {
						warn("sh", "unexpected end of input while reading heredoc '%s'", h.delim)
						h.body = strings.Join(body, "\n")
						return
					}
					ln = strings.TrimRight(ln, "\r")
					if ln == h.delim {
						break
					}
					body = append(body, ln)
				}
				h.body = strings.Join(body, "\n")
			}
		}
	}
}

// --------------------------------------------------------------- execute ----

// shRunUnit executes one unit (pipeline or single command). Single commands
// run in-process against the winux command table; multi-command pipelines
// spawn winux.exe subprocesses so the OS pipes can flow concurrently.
func shRunUnit(u shUnit, ctx *shCtx) int {
	if u.cond == shCondBg && !ctx.bgWarned {
		warn("sh", "background '&' is not supported; running synchronously")
		ctx.bgWarned = true
	}
	if len(u.cmds) == 1 {
		return shRunOne(u.cmds[0], ctx)
	}
	return shRunPipeline(u.cmds, ctx)
}

func shRunOne(c shCmd, ctx *shCtx) int {
	// builtins
	switch c.name {
	case "exit":
		n := 0
		if len(c.args) > 0 {
			if v, err := strconv.Atoi(c.args[0]); err == nil {
				n = v
			}
		}
		ctx.exitCode = &n
		return n
	case "cd":
		return shRunCd(c, ctx)
	case ":", "true":
		return 0
	case "false":
		return 1
	}
	cc, ok := cmdTable[c.name]
	if !ok {
		// a standalone assignment (`X=winux`) carries no command: persist the
		// variable for later lines (bash behaves the same way)
		if c.name == "" {
			for _, a := range c.env {
				eq := strings.IndexByte(a, '=')
				os.Setenv(a[:eq], shExpand(ctx, a[eq+1:]))
			}
			return 0
		}
		fmt.Fprintf(os.Stderr, "sh: %s: command not found\n", c.name)
		return 127
	}
	// environment assignments
	var savedEnv []string
	if len(c.env) > 0 {
		for _, a := range c.env {
			eq := strings.IndexByte(a, '=')
			k, v := a[:eq], shExpand(ctx, a[eq+1:])
			savedEnv = append(savedEnv, os.Getenv(k))
			os.Setenv(k, v)
		}
	}
	// streams
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	in, out, errw := oldIn, oldOut, oldErr
	var toClose []*os.File
	var tmpFile *os.File
	if len(c.heredocs) > 0 {
		tmpFile, _ = os.CreateTemp("", "sh-heredoc")
		if tmpFile != nil {
			for _, h := range c.heredocs {
				b := h.body
				if !h.quoted {
					b = shExpand(ctx, b)
				}
				tmpFile.WriteString(b)
				tmpFile.WriteString("\n")
			}
			tmpFile.Seek(0, io.SeekStart)
			in = tmpFile
		}
	}
	if c.in.kind == shRedirIn {
		if f, err := os.Open(c.in.path); err == nil {
			in = f
			toClose = append(toClose, f)
		} else {
			shRestoreStreams(oldIn, oldOut, oldErr, toClose, tmpFile, c, savedEnv)
			fmt.Fprintf(os.Stderr, "sh: %s: cannot open '%s': %v\n", c.name, c.in.path, err)
			return 1
		}
	}
	if c.out.kind != 0 {
		flags := os.O_CREATE | os.O_WRONLY
		if c.out.kind == shRedirApp {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
		}
		if f, err := os.OpenFile(c.out.path, flags, 0o666); err == nil {
			out = f
			toClose = append(toClose, f)
		} else {
			shRestoreStreams(oldIn, oldOut, oldErr, toClose, tmpFile, c, savedEnv)
			fmt.Fprintf(os.Stderr, "sh: %s: cannot create '%s': %v\n", c.name, c.out.path, err)
			return 1
		}
	}
	if c.err2.kind != 0 {
		switch c.err2.kind {
		case shRedirErrMirror:
			errw = out
		default:
			flags := os.O_CREATE | os.O_WRONLY
			if c.err2.kind == shRedirErrApp {
				flags |= os.O_APPEND
			} else {
				flags |= os.O_TRUNC
			}
			if f, err := os.OpenFile(c.err2.path, flags, 0o666); err == nil {
				errw = f
				toClose = append(toClose, f)
			}
		}
	}
	os.Stdin, os.Stdout, os.Stderr = in, out, errw
	code := cc.run(c.args)
	shRestoreStreams(oldIn, oldOut, oldErr, toClose, tmpFile, c, savedEnv)
	return code
}

func shRestoreStreams(oldIn, oldOut, oldErr *os.File, toClose []*os.File, tmp *os.File, c shCmd, savedEnv []string) {
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	for _, f := range toClose {
		f.Close()
	}
	if tmp != nil {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	if len(savedEnv) > 0 {
		for i, k := range c.env {
			os.Setenv(k[:strings.IndexByte(k, '=')], savedEnv[i])
		}
	}
}

func shRunPipeline(cmds []shCmd, ctx *shCtx) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sh: cannot locate winux executable: %v\n", err)
		return 1
	}
	// resolve the real executable path (os.Executable may return a .exe in use)
	exe, _ = filepath.Abs(exe)

	var procs []*exec.Cmd
	var prevPipe *os.File // read end handed to the next stage
	defer func() {
		for _, p := range procs {
			p.Wait()
		}
	}()
	lastCode := 0
	for i, c := range cmds {
		args := append([]string{c.name}, c.args...)
		cmd := exec.Command(exe, args...)
		if ne := appendAssignments(c.env, ctx); len(ne) > 0 {
			cmd.Env = ne
		}
		// stdin
		if len(c.heredocs) > 0 {
			tmp, _ := os.CreateTemp("", "sh-heredoc")
			if tmp != nil {
				for _, h := range c.heredocs {
					b := h.body
					if !h.quoted {
						b = shExpand(ctx, b)
					}
					tmp.WriteString(b)
					tmp.WriteString("\n")
				}
				tmp.Seek(0, io.SeekStart)
				cmd.Stdin = tmp
				defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
			}
		} else if c.in.kind == shRedirIn {
			if f, err := os.Open(c.in.path); err == nil {
				cmd.Stdin = f
				defer f.Close()
			} else {
				fmt.Fprintf(os.Stderr, "sh: %s: cannot open '%s': %v\n", c.name, c.in.path, err)
				return 1
			}
		} else if prevPipe != nil {
			cmd.Stdin = prevPipe
		} else {
			cmd.Stdin = os.Stdin // first stage without a redirect reads our stdin
		}
		// stdout / stderr
		isLast := i == len(cmds)-1
		if isLast {
			if c.out.kind != 0 {
				flags := os.O_CREATE | os.O_WRONLY
				if c.out.kind == shRedirApp {
					flags |= os.O_APPEND
				} else {
					flags |= os.O_TRUNC
				}
				if f, err := os.OpenFile(c.out.path, flags, 0o666); err == nil {
					cmd.Stdout = f
					defer f.Close()
				} else {
					fmt.Fprintf(os.Stderr, "sh: %s: cannot create '%s': %v\n", c.name, c.out.path, err)
					return 1
				}
			}
			if c.err2.kind == shRedirErrMirror {
				cmd.Stderr = cmd.Stdout
			} else if c.err2.kind != 0 {
				flags := os.O_CREATE | os.O_WRONLY
				if c.err2.kind == shRedirErrApp {
					flags |= os.O_APPEND
				} else {
					flags |= os.O_TRUNC
				}
				if f, err := os.OpenFile(c.err2.path, flags, 0o666); err == nil {
					cmd.Stderr = f
					defer f.Close()
				}
			}
			// without an explicit target, the last stage inherits our streams
			// (a nil *os.File would silently become the null device)
			if cmd.Stdout == nil {
				cmd.Stdout = os.Stdout
			}
			if cmd.Stderr == nil {
				cmd.Stderr = os.Stderr
			}
		} else {
			pr, pw, _ := os.Pipe()
			cmd.Stdout = pw
			cmd.Stderr = os.Stderr
			prevPipe = pr
			defer pr.Close()
			startErr := cmd.Start()
			pw.Close()
			if startErr != nil {
				fmt.Fprintf(os.Stderr, "sh: %s: %v\n", c.name, startErr)
				return 127
			}
			procs = append(procs, cmd)
			continue
		}
		startErr := cmd.Start()
		if startErr != nil {
			fmt.Fprintf(os.Stderr, "sh: %s: %v\n", c.name, startErr)
			return 127
		}
		procs = append(procs, cmd)
		lastCode = waitShExit(cmd)
	}
	return lastCode
}

func appendAssignments(env []string, ctx *shCtx) []string {
	if len(env) == 0 {
		return nil
	}
	// build child env: os.Environ() but honour per-command assignments
	m := map[string]string{}
	for _, e := range os.Environ() {
		if eq := strings.IndexByte(e, '='); eq > 0 {
			m[e[:eq]] = e[eq+1:]
		}
	}
	for _, a := range env {
		eq := strings.IndexByte(a, '=')
		m[a[:eq]] = shExpand(ctx, a[eq+1:])
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func waitShExit(cmd *exec.Cmd) int {
	err := cmd.Wait()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

// ---------------------------------------------------------------- driver ----

func cmdSh(args []string) int {
	f := NewFlags(
		&Flag{Name: "command", Short: 'c', Long: "command", HasArg: true, ArgName: "SCRIPT", Help: "run the commands in SCRIPT"},
	)
	if !parseOrDie("sh", f, args) {
		return 2
	}
	if handleHelpVersion("sh", "[-c SCRIPT] [FILE]", f) {
		return 0
	}
	ctx := &shCtx{}
	if scr := f.Str("command"); scr != "" {
		return shRunSource(strings.NewReader(scr), ctx, false)
	}
	pos := f.Positional
	if len(pos) > 0 && pos[0] != "-" {
		b, err := os.ReadFile(pos[0])
		if err != nil {
			warn("sh", "cannot open '%s': %v", pos[0], err)
			return 1
		}
		return shRunSource(strings.NewReader(string(b)), ctx, false)
	}
	if !isTerminal(os.Stdin) {
		data, _ := io.ReadAll(os.Stdin)
		return shRunSource(strings.NewReader(string(data)), ctx, false)
	}
	return shRepl(ctx)
}

// shLineReader pulls logical script lines one at a time; the runner needs raw
// lines so heredoc bodies can be consumed verbatim.
type shLineReader func() (string, bool)

func shReadAllLines(r io.Reader) []string {
	data, _ := io.ReadAll(r)
	raw := strings.Split(string(data), "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		out = append(out, strings.TrimRight(l, "\r"))
	}
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func shRunSource(r io.Reader, ctx *shCtx, interactive bool) int {
	lines := shReadAllLines(r)
	idx := 0
	rl := func() (string, bool) {
		if idx >= len(lines) {
			return "", false
		}
		l := lines[idx]
		idx++
		return l, true
	}
	return shRunLines(rl, ctx)
}

func shRunLines(rl func() (string, bool), ctx *shCtx) int {
	for {
		line, ok := rl()
		if !ok {
			return ctx.lastStatus
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		toks := shTokenize(line, ctx)
		if len(toks) == 0 {
			continue
		}
		units, err := shParseLine(toks)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		if len(units) == 0 {
			continue
		}
		shCollectHeredocs(units, rl)
		prevStatus := 0
		for _, u := range units {
			run := true
			switch u.cond {
			case shCondAnd:
				run = prevStatus == 0
			case shCondOr:
				run = prevStatus != 0
			}
			if !run {
				continue
			}
			prevStatus = shRunUnit(u, ctx)
			ctx.lastStatus = prevStatus
			if ctx.exitCode != nil {
				return *ctx.exitCode
			}
		}
	}
}

func shRepl(ctx *shCtx) int {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	// a small pipe that lets the REPL feed heredoc bodies to shRunLines
	fmt.Print("sh> ")
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			fmt.Print("sh> ")
			continue
		}
		toks := shTokenize(line, ctx)
		if len(toks) == 0 {
			fmt.Print("sh> ")
			continue
		}
		units, err := shParseLine(toks)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			fmt.Print("sh> ")
			continue
		}
		// heredoc bodies come from the same console
		rl := func() (string, bool) {
			if !sc.Scan() {
				return "", false
			}
			return strings.TrimRight(sc.Text(), "\r"), true
		}
		shCollectHeredocs(units, rl)
		prevStatus := 0
		for _, u := range units {
			run := true
			switch u.cond {
			case shCondAnd:
				run = prevStatus == 0
			case shCondOr:
				run = prevStatus != 0
			}
			if !run {
				continue
			}
			prevStatus = shRunUnit(u, ctx)
			ctx.lastStatus = prevStatus
			if ctx.exitCode != nil {
				return *ctx.exitCode
			}
		}
		fmt.Print("sh> ")
	}
	return ctx.lastStatus
}
