package core

// Windows PowerShell (5.1 and earlier) parses the command line before running
// anything, and `&&` is not a valid separator there — PowerShell 7 added it. So
// a pasted Linux one-liner such as
//
//	nohup kill -9 1 && kill -9 2 && mv a.exe b\ && cd b && .\b.exe
//
// dies with four parser errors and nothing runs.
//
// The activation hook (winux-activate.ps1) binds the Enter key and asks this
// hidden command to rewrite such a line into PowerShell that means the same
// thing:
//
//	a && b        ->  a; $__winuxChainOk = $?; if ($__winuxChainOk) { b; $__winuxChainOk = $? }
//	a || b        ->  a; $__winuxChainOk = $?; if (-not $__winuxChainOk) { b; $__winuxChainOk = $? }
//
// Chaining works left to right exactly like the shell does, including
// `a && b || c` (c runs when a failed *or* when b failed), because every step
// only looks at the status of the step before it. `$?` is PowerShell's
// "previous statement succeeded" flag, which for a native program means exit
// code 0 — the same test the shell applies.
//
// The rewrite never re-quotes anything: it only splits the line at top-level
// `&&`/`||` and re-joins the segments verbatim, so quotes, parentheses,
// redirections and pipes inside a segment survive untouched. If the line is not
// a well formed chain (unbalanced quotes or brackets, empty segment) the
// original line is returned unchanged and PowerShell reports its own error as
// before.

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func init() {
	addHiddenCmd("__ps-chain", "internal: rewrite a PowerShell 5.1 && / || line (used by the activation hook)", cmdPSChain)
}

// psChainVar is the scratch variable the rewritten line uses for the status of
// the previous segment. The name is unlikely to collide with real code.
const psChainVar = "$__winuxChainOk"

// cmdPSChain prints the rewritten line (or the input unchanged). The activation
// hook feeds the typed line on stdin: PowerShell 5.1 rebuilds a native command
// line from the argument list, and a line holding quotes does not survive that
// round trip (it arrives with the quotes stripped, so `echo "a && b"` would be
// split). Reading stdin keeps the bytes exactly as typed. Arguments are still
// accepted for direct use and for the tests.
func cmdPSChain(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "--help", "-h":
			fmt.Println("Usage: winux __ps-chain [LINE]")
			fmt.Println("       (the activated PowerShell 5.1 prompt pipes the typed line on stdin)")
			fmt.Println()
			fmt.Println("Prints a PowerShell line with the same meaning when LINE contains && or ||,")
			fmt.Println("and LINE unchanged otherwise. Internal helper behind the Enter-key hook.")
			return 0
		case "--version", "-V":
			printVersion("__ps-chain")
			return 0
		}
	}
	line := strings.Join(args, " ")
	if len(args) == 0 {
		r := bufio.NewReader(os.Stdin)
		if l, err := r.ReadString('\n'); err == nil || len(l) > 0 {
			line = strings.TrimRight(l, "\r\n")
		}
	}
	out, _ := psChainRewrite(line)
	fmt.Println(out)
	return 0
}

// psChainRewrite turns `a && b || c` into a single PowerShell line with the
// same meaning. changed is false when there was nothing to rewrite, in which
// case out is the input unchanged.
//
// The line is shown verbatim at the prompt after Enter (the line editor rewrites
// the buffer in place), so the rewrite is kept as short and as readable as the
// semantics allow:
//
//	a && b          ->  a; if ($?) { b }
//	a && b && c     ->  a; if ($?) { b; if ($?) { c } }
//	a || b          ->  a; if (-not $?) { b }
//	a && b || c      ->  a; $__winuxChainOk = $?; if ($__winuxChainOk) { b; ... }
//
// A chain of one operator type nests, which is exactly the shell's left-to-right
// "did the previous step succeed?" test. A mixed chain needs the status variable,
// because after `a && b` the next step must look at b's status (and still fall
// through to the || branch when a itself failed) — the shell's rule.
func psChainRewrite(line string) (out string, changed bool) {
	segs, ops, ok := psSplitChain(line)
	if !ok || len(ops) == 0 {
		return line, false
	}
	if allOps(ops, "&&") || allOps(ops, "||") {
		cond := "$?"
		if ops[0] == "||" {
			cond = "-not $?"
		}
		// nest from the end so each level wraps the rest
		body := segs[len(segs)-1]
		for i := len(ops) - 1; i >= 0; i-- {
			body = segs[i] + "; if (" + cond + ") { " + body + " }"
		}
		return body, true
	}
	var b strings.Builder
	b.WriteString(segs[0])
	b.WriteString("; ")
	b.WriteString(psChainVar)
	b.WriteString(" = $?")
	for i, op := range ops {
		seg := segs[i+1]
		b.WriteString("; if (")
		if op == "||" {
			b.WriteString("-not ")
		}
		b.WriteString(psChainVar)
		b.WriteString(") { ")
		b.WriteString(seg)
		b.WriteString("; ")
		b.WriteString(psChainVar)
		b.WriteString(" = $? }")
	}
	return b.String(), true
}

func allOps(ops []string, want string) bool {
	for _, op := range ops {
		if op != want {
			return false
		}
	}
	return len(ops) > 0
}

// psSplitChain splits a PowerShell line at its top-level `&&` and `||`
// operators. Everything inside quotes, (), [], {} or after a # comment is left
// alone, and a trailing backslash-free scan is used rather than a full parser
// because the line is often not valid PowerShell yet (that `&&` is the problem).
func psSplitChain(line string) (segs, ops []string, ok bool) {
	var (
		start  int
		depth  int
		single bool
		double bool
		i      int
	)
	for i = 0; i < len(line); i++ {
		c := line[i]
		switch {
		case single:
			// inside '...' only a doubled '' means a quote; && is literal text
			if c == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' {
					i++
					continue
				}
				single = false
			}
		case double:
			// inside "..." the backtick escapes the next character
			if c == '`' && i+1 < len(line) {
				i++
				continue
			}
			if c == '"' {
				double = false
			}
		default:
			switch c {
			case '`':
				if i+1 < len(line) {
					i++
				}
			case '\'':
				single = true
			case '"':
				double = true
			case '#':
				// a comment runs to the end of the line: no more operators
				i = len(line)
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				if depth > 0 {
					depth--
				}
			case '&':
				if depth == 0 && i+1 < len(line) && line[i+1] == '&' {
					segs = append(segs, strings.TrimSpace(line[start:i]))
					ops = append(ops, "&&")
					i++
					start = i + 1
				}
			case '|':
				if depth == 0 && i+1 < len(line) && line[i+1] == '|' {
					segs = append(segs, strings.TrimSpace(line[start:i]))
					ops = append(ops, "||")
					i++
					start = i + 1
				}
			}
		}
	}
	segs = append(segs, strings.TrimSpace(line[start:]))
	if single || double || depth != 0 {
		return nil, nil, false
	}
	for _, s := range segs {
		if s == "" {
			return nil, nil, false
		}
	}
	return segs, ops, true
}
