package core

// This file exposes the small public surface of the winux engine: the
// in-process execution harness used by the test suite in winux/test and
// available to embedders, plus thin wrappers around internal helpers whose
// behaviour the tests need to assert directly.

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"time"
)

// Execute runs a registered command in-process with stdout, stderr and stdin
// redirected to captures. It returns the command's exit code plus the captured
// stdout and stderr. An unknown command yields exit code 127, mirroring a
// shell's "command not found" — unless it is a well-known path keyword, which
// prints the reminder and succeeds, exactly like the real dispatch in Main.
func Execute(name string, args []string, stdin io.Reader) (int, string, string) {
	c, ok := cmdTable[name]
	if !ok {
		if text, matched := pathHint(name, args); matched {
			return 0, text, ""
		}
		suggestion := pathSuggestion(name)
		return 127, "", fmt.Sprintf("winux: unknown command '%s'\n%s", name, suggestion)
	}
	oldOut, oldErr, oldIn := os.Stdout, os.Stderr, os.Stdin
	defer func() { os.Stdout, os.Stderr, os.Stdin = oldOut, oldErr, oldIn }()

	rOut, wOut, err := os.Pipe()
	if err != nil {
		return 2, "", err.Error()
	}
	defer rOut.Close()
	rErr, wErr, err := os.Pipe()
	if err != nil {
		return 2, "", err.Error()
	}
	defer rErr.Close()
	os.Stdout, os.Stderr = wOut, wErr

	var inFile *os.File
	if stdin != nil {
		inFile, err = os.CreateTemp("", "winux-stdin")
		if err != nil {
			return 2, "", err.Error()
		}
		defer os.Remove(inFile.Name())
		defer inFile.Close()
		if _, err := io.Copy(inFile, stdin); err != nil {
			return 2, "", err.Error()
		}
		if _, err := inFile.Seek(0, io.SeekStart); err != nil {
			return 2, "", err.Error()
		}
		os.Stdin = inFile
	}

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(rOut); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(rErr); errCh <- string(b) }()

	code := c.run(args)
	wOut.Close()
	wErr.Close()
	out := <-outCh
	errOut := <-errCh
	return code, out, errOut
}

// CommandNames lists every public command under its primary name (aliases such
// as vim are folded into their primary vi), sorted. Hidden helpers such as
// __ps-chain are not part of the surface and are left out.
func CommandNames() []string {
	var names []string
	for name, c := range cmdTable {
		if c.name == name {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// IsAdmin reports whether the current process runs with administrator rights
// (an elevated token). The test suite uses it to skip the service-manager
// tests that need the SCM. It is portable: non-Windows builds report false.
func IsAdmin() bool { return isElevated() }

// ------------------------------------------------------------------ helpers --

// WildcardMatch reports whether name matches the glob pattern (both sides are
// normalised so that `\` and `/` are interchangeable on Windows).
func WildcardMatch(pattern, name string) bool { return wildcardMatch(pattern, name) }

// ParseListSpec parses a GNU-style list of fields ("1,3-5,8-").
func ParseListSpec(s string) ([]RangeSpec, error) { return parseListSpec(s) }

// RangeSelected reports whether idx (1-based) is covered by one of the specs.
func RangeSelected(specs []RangeSpec, idx int) bool { return selected(specs, idx) }

// ParseDate parses the date forms accepted by `date -d` / `touch -d`.
func ParseDate(s string) (time.Time, error) { return parseDate(s) }

// Strftime renders t with a GNU strftime format string.
func Strftime(t time.Time, format string) string { return strftime(t, format) }

// FileExists reports whether the path exists as a file or directory.
func FileExists(p string) bool { return fileExists(p) }

// IsDir reports whether the path exists and is a directory.
func IsDir(p string) bool { return isDir(p) }

// UTF8SeqLen returns the number of bytes in the UTF-8 sequence starting at b.
func UTF8SeqLen(b byte) int { return utf8SeqLen(b) }

// TruncRun truncates s to at most cols runes.
func TruncRun(s string, cols int) string { return truncRun(s, cols) }

// SqueezeBlankLines collapses runs of empty lines into a single one.
func SqueezeBlankLines(lines []string) []string { return squeezeBlankLines(lines) }

// CompilePattern builds the less/vi search regexp (smart case aware).
func CompilePattern(pat string, ignoreCase bool) (*regexp.Regexp, error) {
	return compilePattern(pat, ignoreCase)
}

// SearchForward finds the first line >= from matching re.
func SearchForward(lines []string, re *regexp.Regexp, from int) (int, bool) {
	return searchForward(lines, re, from)
}

// SearchFrom finds the next (or previous) line matching re, wrapping around.
func SearchFrom(lines []string, re *regexp.Regexp, from int, backwards bool) (int, bool) {
	return searchFrom(lines, re, from, backwards)
}

// DecodeCSI classifies a terminal escape/control sequence.
func DecodeCSI(seq string) KeyKind { return decodeCSI(seq).kind }

// MongoBinary mirrors the BSON binary value used in MongoDB wire messages.
type MongoBinary = mongoBinary

// BsonEncode encodes v as a BSON document (used by tests to build replies).
func BsonEncode(v any) []byte { return bsonEncode(v) }

// BsonDecodeDoc decodes a BSON document at the start of p.
func BsonDecodeDoc(p []byte) (map[string]any, int) { return bsonDecodeDoc(p, 0) }
