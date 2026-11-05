package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateEnvKey(t *testing.T) {
	for _, ok := range []string{"A", "_x", "API_URL2"} {
		if err := ValidateEnvKey(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "1A", "A-B", "A B", "A=B", "a;b", "$(x)", "SESH_THREAD_ID", "SESH_ANYTHING", "TMUX", "TMUX_PANE", "CLAUDE_ENV_FILE"} {
		if err := ValidateEnvKey(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// A special name that slips through aborts the whole source (see zshSpecialNames),
// so the list is checked against the zsh on this machine.
func TestZshSpecialNamesAreRefused(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	out, err := exec.Command("zsh", "-f", "-c", `for k v in "${(@kv)parameters}"; do [[ $v == *special* && ($v == integer* || $v == *readonly* || $v == array* || $v == association*) ]] && print -r -- $k; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range strings.Fields(string(out)) {
		if envKeyPattern.MatchString(name) && ValidateEnvKey(name) == nil {
			t.Errorf("zsh's special parameter %s is accepted", name)
		}
	}
}

// env.sh goes through a real shell: hostile values must come out byte-identical,
// and a removed name must actually be unset.
func TestRenderThreadEnvRoundTripsThroughShell(t *testing.T) {
	vars := map[string]string{
		"QUOTES": `it's "quoted"`,
		"SUBST":  "$(touch pwned) `touch pwned` $HOME",
		"MULTI":  "one\ntwo",
		"EMPTY":  "",
	}
	home := t.TempDir()
	if err := WriteThreadEnv(home, "t1", ThreadEnv{Vars: vars, Removed: []string{"GONE"}}); err != nil {
		t.Fatal(err)
	}
	script, _ := ThreadEnvScript(home, "t1")
	for _, shell := range []string{"sh", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		cmd := exec.Command(shell, "-c", `. "$1"; printf '%s\0' "$QUOTES" "$SUBST" "$MULTI" "$EMPTY" "${GONE-unset}"`, shell, script)
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "GONE=still-here")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		want := []string{vars["QUOTES"], vars["SUBST"], vars["MULTI"], "", "unset"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: got %q, want %q", shell, got, want)
		}
		if _, err := os.Stat(filepath.Join(cmd.Dir, "pwned")); err == nil {
			t.Errorf("%s: a value was executed", shell)
		}
	}
}

func TestThreadEnvFilesArePrivate(t *testing.T) {
	home := t.TempDir()
	if err := WriteThreadEnv(home, "t1", ThreadEnv{Vars: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	dir, _ := ThreadEnvDir(home, "t1")
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "env.json"): 0o600, filepath.Join(dir, "env.sh"): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s is %o, want %o", path, fi.Mode().Perm(), want)
		}
	}
}

func TestReadThreadEnvMissingAndCorrupt(t *testing.T) {
	home := t.TempDir()
	e, err := ReadThreadEnv(home, "never")
	if err != nil || e.Vars == nil || len(e.Vars) != 0 {
		t.Fatalf("missing: %+v %v", e, err)
	}
	dir, _ := ThreadEnvDir(home, "bad")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "env.json"), []byte("{"), 0o600)
	if _, err := ReadThreadEnv(home, "bad"); err == nil {
		t.Fatal("a corrupt env.json read as empty")
	}
}

func TestRemoveThreadEnvRefusesPathLikeIDs(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"", ".", "..", "a/b", `a\b`} {
		if err := RemoveThreadEnv(home, id); err == nil {
			t.Errorf("%q was accepted", id)
		}
	}
	if err := RemoveThreadEnv(home, "never-had-one"); err != nil {
		t.Fatal(err)
	}
}

func TestParseFormatEnvLinesRoundTrip(t *testing.T) {
	vars := map[string]string{"A": "1", "URL": "https://x/?a=b#c", "SP": " padded ", "NL": "a\nb", "Q": `say "hi"`, "E": "", "S": "it's"}
	got, err := ParseEnvLines(FormatEnvLines(vars))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(vars) {
		t.Fatalf("got %q", got)
	}
	for k, v := range vars {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}

func TestParseEnvLinesForms(t *testing.T) {
	got, err := ParseEnvLines("# comment\n\nexport A=1\nB='two words'\nC=\"x\\ny\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got["A"] != "1" || got["B"] != "two words" || got["C"] != "x\ny" {
		t.Fatalf("%q", got)
	}
	for _, bad := range []string{"NOEQUALS", "A=1\nA=2", `A="\q"`} {
		if _, err := ParseEnvLines(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestApplyThreadEnvOverrides(t *testing.T) {
	got := ApplyThreadEnv([]string{"A=old", "B=keep"}, map[string]string{"A": "new", "C": "add"})
	if strings.Join(got, ",") != "B=keep,A=new,C=add" {
		t.Fatalf("%q", got)
	}
}

func TestThreadEnvHashIsUnambiguous(t *testing.T) {
	if ThreadEnvHash(nil) != "" {
		t.Fatal("no variables must have no hash")
	}
	if ThreadEnvHash(map[string]string{"A": "1B"}) == ThreadEnvHash(map[string]string{"A": "1", "B": ""}) {
		t.Fatal("different sets share a hash")
	}
}

// The launch line names the env file itself, because a shell acts on the variable and
// `tmux -e` at new-session would hand it to every pane of the session. A thread with
// no variables has nothing to source.
func TestWithThreadEnvLine(t *testing.T) {
	bare := WithThreadEnv("/h o/env.sh", false, "claude --x")
	if bare != `export SESH_THREAD_ENV_FILE='/h o/env.sh'; exec claude --x` {
		t.Fatalf("%q", bare)
	}
	withVars := WithThreadEnv("/h o/env.sh", true, "claude --x")
	if withVars != `export SESH_THREAD_ENV_FILE='/h o/env.sh'; . '/h o/env.sh'; exec claude --x` {
		t.Fatalf("%q", withVars)
	}
}

// A turn runs through a shell that acts on this variable, so one inherited by a
// hand-started daemon must not survive into a turn.
func TestTurnEnvDropsAnInheritedEnvFile(t *testing.T) {
	for _, e := range TurnEnv([]string{EnvThreadEnvFile + "=/other/thread/env.sh", "A=1"}, "t", "", "") {
		if strings.HasPrefix(e, EnvThreadEnvFile+"=") {
			t.Fatalf("kept %q", e)
		}
	}
}
