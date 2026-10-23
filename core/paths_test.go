package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolatePaths points the user table at an empty file and clears the switches,
// so a keyword list or a reminder in a test never depends on the real
// %USERPROFILE%\.winux\paths.tsv of the machine running the suite.
func isolatePaths(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "paths.tsv")
	writeHostFixture(t, p, []byte("# empty\n"))
	t.Setenv("WINUX_PATHS_FILE", p)
	t.Setenv("WINUX_PATH_HINTS", "")
}

func TestBuiltinPathEntriesAreWellFormed(t *testing.T) {
	seen := map[string]string{}
	for _, e := range builtinPathEntries {
		if len(e.Keys) == 0 {
			t.Fatalf("entry %q has no keyword", e.Path)
		}
		name := e.Keys[0]
		if e.Kind != "file" && e.Kind != "dir" {
			t.Errorf("%s: kind %q must be file or dir", name, e.Kind)
		}
		if strings.TrimSpace(e.Desc) == "" {
			t.Errorf("%s: empty description", name)
		}
		if _, ok := expandPathTemplate(e.Path); !ok {
			t.Errorf("%s: %q does not expand on this machine", name, e.Path)
		}
		for _, k := range e.Keys {
			if k != strings.ToLower(k) {
				t.Errorf("%s: keyword %q must be lower case", name, k)
			}
			if strings.ContainsAny(k, " \t/\\") {
				t.Errorf("%s: keyword %q must be a plain word", name, k)
			}
			if prev, dup := seen[k]; dup {
				t.Errorf("keyword %q is claimed by both %s and %s", k, prev, name)
			}
			seen[k] = name
		}
	}
	// the keyword the feature was asked for must stay reachable
	if _, _, ok := lookupPath("hosts"); !ok {
		t.Fatal("the built-in table lost the hosts keyword")
	}
}

func TestExpandPathTemplate(t *testing.T) {
	t.Setenv("WINUX_TEST_DIR", `D:\x`)
	got, ok := expandPathTemplate(`%WINUX_TEST_DIR%\etc\hosts`)
	if !ok || got != `D:\x\etc\hosts` {
		t.Errorf("expansion = (%q, %v), want D:\\x\\etc\\hosts", got, ok)
	}
	if _, ok := expandPathTemplate(`%WINUX_NO_SUCH_VAR_XYZ%\hosts`); ok {
		t.Error("a missing placeholder must make the entry unavailable, not expand to an empty path")
	}
	// %ProgramFiles(x86)% style names contain parentheses: they must survive
	if v := os.Getenv("ProgramFiles(x86)"); v != "" {
		got, ok := expandPathTemplate(`%ProgramFiles(x86)%`)
		if !ok || got != filepath.Clean(v) {
			t.Errorf("parenthesised name expanded to (%q, %v), want %q", got, ok, filepath.Clean(v))
		}
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		got, ok := expandPathTemplate(`~\.ssh`)
		if !ok || got != filepath.Join(home, ".ssh") {
			t.Errorf("~ expansion = (%q, %v), want %q", got, ok, filepath.Join(home, ".ssh"))
		}
	}
}

func TestLookupPathMatchesKeywordsAndAliases(t *testing.T) {
	isolatePaths(t)
	for _, key := range []string{"hosts", "HOSTS", " Hosts "} {
		e, p, ok := lookupPath(key)
		if !ok || e.Keys[0] != "hosts" || !strings.HasSuffix(p, `drivers\etc\hosts`) {
			t.Errorf("lookupPath(%q) = (%v, %q, %v)", key, e, p, ok)
		}
	}
	// aliases resolve to the canonical entry
	for key, want := range map[string]string{"home": "userprofile", "tmp": "temp", "protocol": "protocols"} {
		e, _, ok := lookupPath(key)
		if !ok || e.Keys[0] != want {
			t.Errorf("lookupPath(%q) canonical = %v, want %s", key, e, want)
		}
	}
	if _, _, ok := lookupPath("no-such-keyword"); ok {
		t.Error("an unknown keyword must not resolve")
	}
	if _, _, ok := lookupPath(""); ok {
		t.Error("an empty keyword must not resolve")
	}
}

func TestPathHintRendersPathStateAndCommands(t *testing.T) {
	isolatePaths(t)
	text, matched := pathHint("hosts", nil)
	if !matched {
		t.Fatal("hosts must produce a reminder")
	}
	for _, want := range []string{`hosts → `, `drivers\etc\hosts`, "接着做:", `winux cat "`, `winux sudo vi "`, "winux paths"} {
		if !strings.Contains(text, want) {
			t.Errorf("reminder is missing %q:\n%s", want, text)
		}
	}
	// extra arguments are reported, not silently dropped
	text, _ = pathHint("hosts", []string{"srv1"})
	if !strings.Contains(text, "额外的参数") {
		t.Errorf("extra arguments were not mentioned:\n%s", text)
	}
	// a directory entry suggests ls instead of cat
	text, matched = pathHint("temp", nil)
	if !matched || !strings.Contains(text, "winux ls ") {
		t.Errorf("directory reminder should suggest ls:\n%s", text)
	}
}

func TestPathHintCanBeDisabled(t *testing.T) {
	isolatePaths(t)
	t.Setenv("WINUX_PATH_HINTS", "off")
	if _, matched := pathHint("hosts", nil); matched {
		t.Error("WINUX_PATH_HINTS=off must disable the reminder")
	}
	if code, _, _ := Execute("hosts", nil, nil); code != 127 {
		t.Errorf("with the reminder off, an unknown command must still exit 127, got %d", code)
	}
	if got := pathSuggestion("host"); got != "" {
		t.Errorf("the suggestion must be disabled too, got %q", got)
	}
}

// The reminder belongs to command position only: as an argument of a real
// command the keyword must behave exactly like any other string.
func TestKeywordAsArgumentIsUntouched(t *testing.T) {
	isolatePaths(t)
	if code, out, _ := Execute("hosts", nil, nil); code != 0 || !strings.Contains(out, `drivers\etc\hosts`) {
		t.Errorf("Execute(hosts) = %d, %q", code, out)
	}
	if code, out, errOut := Execute("echo", []string{"hosts"}, nil); code != 0 || strings.TrimSpace(out) != "hosts" || errOut != "" {
		t.Errorf("echo hosts = %d, %q, %q (must print just the argument)", code, out, errOut)
	}
	// cat hosts looks for a real file called hosts in the working directory
	dir := t.TempDir()
	code, out, errOut := Execute("cat", []string{filepath.Join(dir, "hosts")}, nil)
	if code == 0 || strings.Contains(out, "drivers") || strings.Contains(errOut, "drivers") {
		t.Errorf("cat hosts = %d, %q, %q (no reminder, no substitution)", code, out, errOut)
	}
}

func TestPathSuggestionForNearMiss(t *testing.T) {
	isolatePaths(t)
	got := pathSuggestion("host")
	if !strings.Contains(got, "hosts") || !strings.Contains(got, "drivers") {
		t.Errorf("suggestion for 'host' = %q", got)
	}
	if pathSuggestion("zzzznotapath") != "" {
		t.Error("no suggestion should be made for an unrelated name")
	}
	if pathSuggestion("h") != "" {
		t.Error("one-letter names should not produce suggestions")
	}
	// a suggestion keeps the unknown-command failure
	code, out, errOut := Execute("host", nil, nil)
	if code != 127 || out != "" || !strings.Contains(errOut, "unknown command") || !strings.Contains(errOut, "hosts") {
		t.Errorf("Execute(host) = %d, %q, %q", code, out, errOut)
	}
}

func TestUserPathTableExtendsAndShadows(t *testing.T) {
	p := filepath.Join(t.TempDir(), "paths.tsv")
	body := "# comment\n" +
		"myapp\tD:\\apps\\my.conf\tfile\t我的应用配置\n" +
		"hosts\tD:\\alt\\hosts\tfile\t我自己的 hosts\tadmin\n" +
		"aliased|alias2\tD:\\alt\\dir\tdir\t带别名的目录\n" +
		"no tabs here\n"
	writeHostFixture(t, p, []byte(body))
	t.Setenv("WINUX_PATHS_FILE", p)

	e, path, ok := lookupPath("myapp")
	if !ok || path != `D:\apps\my.conf` || !e.User || e.Desc != "我的应用配置" {
		t.Errorf("user entry = (%+v, %q, %v)", e, path, ok)
	}
	// the user table wins over the built-in entry of the same keyword
	if _, path, ok := lookupPath("hosts"); !ok || path != `D:\alt\hosts` {
		t.Errorf("shadowed hosts = %q, %v", path, ok)
	}
	if e, _, ok := lookupPath("hosts"); !ok || !e.Admin {
		t.Errorf("the admin flag from the user table was lost: %+v", e)
	}
	if _, _, ok := lookupPath("alias2"); !ok {
		t.Error("alias in the user table did not resolve")
	}
	// the built-in table is still there behind the user entries
	if _, path, ok := lookupPath("temp"); !ok || path == "" {
		t.Errorf("built-in temp disappeared: %q, %v", path, ok)
	}
}

func TestPathsCommandListsQuietlyAndAsJSON(t *testing.T) {
	isolatePaths(t)

	code, out, errOut := Execute("paths", []string{"-q", "hosts"}, nil)
	want, _ := expandPathTemplate(`%SystemRoot%\System32\drivers\etc\hosts`)
	if code != 0 || strings.TrimSpace(out) != want || errOut != "" {
		t.Errorf("paths -q hosts = %d, %q, %q (want %q)", code, out, errOut, want)
	}

	code, out, _ = Execute("paths", []string{"--json"}, nil)
	if code != 0 {
		t.Fatalf("paths --json exited %d", code)
	}
	var rows []struct {
		Keyword string `json:"keyword"`
		Path    string `json:"path"`
		Exists  bool   `json:"exists"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("paths --json produced invalid JSON: %v", err)
	}
	if len(rows) < 20 {
		t.Errorf("only %d entries in the table", len(rows))
	}
	found := false
	for _, r := range rows {
		if r.Keyword == "hosts" && strings.HasSuffix(r.Path, `drivers\etc\hosts`) {
			found = true
		}
	}
	if !found {
		t.Error("paths --json does not contain the hosts entry")
	}

	// substring search over keyword, path and description
	code, out, _ = Execute("paths", []string{"-s", "ssh", "-q"}, nil)
	if code != 0 || strings.Count(out, "\n") < 2 || !strings.Contains(out, `.ssh`) {
		t.Errorf("paths -s ssh = %d, %q", code, out)
	}

	// an unknown keyword is an error with a readable message
	code, _, errOut = Execute("paths", []string{"nosuchkeyword"}, nil)
	if code != 1 || !strings.Contains(errOut, "unknown keyword") {
		t.Errorf("paths nosuchkeyword = %d, %q", code, errOut)
	}

	// the plain listing prints keywords, paths and the usage footer
	code, out, _ = Execute("paths", []string{"hosts"}, nil)
	if code != 0 || !strings.Contains(out, "hosts → ") {
		t.Errorf("paths hosts = %d, %q", code, out)
	}
}
