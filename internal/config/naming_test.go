package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNamingMissingFileIsNoRules(t *testing.T) {
	n, err := LoadNaming(t.TempDir())
	if err != nil || n != nil {
		t.Fatalf("missing config: want (nil,nil), got (%v,%v)", n, err)
	}
	// nil Naming applies no rule.
	name, matched, err := n.SessionNameFor("/x", "id", "nm", "/home/u")
	if err != nil || matched || name != "" {
		t.Fatalf("nil naming should not match: %q %v %v", name, matched, err)
	}
}

func TestNamingLoudOnBadConfig(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[[session_name]]\nmatch = '(' \nname = 'x'\n")
	if _, err := LoadNaming(home); err == nil {
		t.Fatalf("bad regex must be a loud load error")
	}
	writeConfig(t, home, "[[session_name]]\nmatch = '.*'\n")
	if _, err := LoadNaming(home); err == nil {
		t.Fatalf("missing name must be a loud load error")
	}
}

func TestNamingRules(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `
[[session_name]]
match = '^~/dev/[0-9]{8}_(?P<boxid>[a-z0-9]+)__(?P<boxname>[^/]+)$'
name  = '{boxname} <{boxid}> ({tid8})'

[[session_name]]
match = '^~/dev/[0-9]{8}_(?P<boxid>[a-z0-9]+)__(?P<boxname>[^/]+)/(?P<rel>.+)$'
name  = '{boxname}/{rel} <{boxid}> ({tid8})'

[[session_name]]
match = '^(?P<path>.+)$'
name  = '{path} ({tid8})'
`)
	n, err := LoadNaming(home)
	if err != nil {
		t.Fatal(err)
	}
	const uh = "/home/u"
	tid := "a1b2c3d4-ffff-eeee-dddd-000000000000"

	cases := []struct{ cwd, want string }{
		{"/home/u/dev/20260608_ipz5bm__sesh-v2", "sesh-v2 <ipz5bm> (a1b2c3d4)"},
		{"/home/u/dev/20260608_ipz5bm__sesh-v2/internal/tui", "sesh-v2/internal/tui <ipz5bm> (a1b2c3d4)"},
		{"/home/u/other", "~/other (a1b2c3d4)"},
		{"/srv/data", "/srv/data (a1b2c3d4)"},
	}
	for _, c := range cases {
		got, matched, err := n.SessionNameFor(c.cwd, tid, "nm", uh)
		if err != nil || !matched {
			t.Fatalf("cwd %s: err=%v matched=%v", c.cwd, err, matched)
		}
		if got != c.want {
			t.Errorf("cwd %s: got %q want %q", c.cwd, got, c.want)
		}
	}
}

func TestNamingSanitizesTmuxForbiddenChars(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[[session_name]]\nmatch = '^(?P<p>.+)$'\nname = '{p} ({tid8})'\n")
	n, err := LoadNaming(home)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := n.SessionNameFor("/a/b.c:d", "12345678", "nm", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got, ".:") {
		t.Errorf("tmux-forbidden chars survived: %q", got)
	}
}

func TestNamingUnknownPlaceholderIsLoud(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[[session_name]]\nmatch = '^.*$'\nname = '{nope}'\n")
	n, err := LoadNaming(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := n.SessionNameFor("/x", "id", "nm", ""); err == nil {
		t.Fatalf("unknown placeholder must be a loud error")
	}
}

func TestNamingNoMatchFallsThrough(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[[session_name]]\nmatch = '^/only/this$'\nname = 'x ({tid8})'\n")
	n, err := LoadNaming(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, matched, err := n.SessionNameFor("/other", "id", "nm", ""); err != nil || matched {
		t.Fatalf("non-matching cwd must fall through to the default, got matched=%v err=%v", matched, err)
	}
}
