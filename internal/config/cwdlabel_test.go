package config

import (
	"strings"
	"testing"
)

const lukasRules = `
[[cwd_label]]
match = '^~/dev/[0-9]{8}_(?P<boxid>[a-z0-9]+)__(?P<boxname>[^/]+)$'
label = '{boxname} <{boxid}>'
[[cwd_label]]
match = '^~/dev/[0-9]{8}_(?P<boxid>[a-z0-9]+)__(?P<boxname>[^/]+)/(?P<rel>.+)$'
label = '{boxname}/{rel} <{boxid}>'
[[cwd_label]]
match = '^~/mysetup$'
label = 'mysetup'
[[cwd_label]]
match = '^~/mysetup/(?P<rel>.+)$'
label = 'mysetup/{rel}'
`

func TestCwdLabelRules(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, lukasRules)
	l, err := LoadCwdLabels(home)
	if err != nil {
		t.Fatal(err)
	}
	const uh = "/home/u"
	cases := []struct{ cwd, want string }{
		{"/home/u/dev/20260608_ipz5bm__sesh-v2", "sesh-v2 <ipz5bm>"},
		{"/home/u/dev/20260608_ipz5bm__sesh-v2/internal/tui", "sesh-v2/internal/tui <ipz5bm>"},
		{"/home/u/mysetup", "mysetup"},
		{"/home/u/mysetup/myrig/home", "mysetup/myrig/home"},
		{"/home/u/other/place", "~/other/place"}, // no rule -> ~-relative
		{"/srv/data", "/srv/data"},               // no rule, outside home -> raw
	}
	for _, c := range cases {
		got, err := l.LabelFor(c.cwd, uh)
		if err != nil {
			t.Fatalf("cwd %s: %v", c.cwd, err)
		}
		if got != c.want {
			t.Errorf("cwd %s: got %q want %q", c.cwd, got, c.want)
		}
	}
}

func TestCwdLabelNilLabelingIsTildeRelative(t *testing.T) {
	var l *Labeling
	got, err := l.LabelFor("/home/u/x", "/home/u")
	if err != nil || got != "~/x" {
		t.Fatalf("nil labeling: got %q, %v", got, err)
	}
}

func TestCwdLabelLoudOnBrokenConfig(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[[cwd_label]]\nmatch='('\nlabel='x'\n")
	if _, err := LoadCwdLabels(home); err == nil {
		t.Fatalf("bad regex must refuse the load")
	}
	writeConfig(t, home, "[[cwd_label]]\nmatch='^(?P<a>.+)$'\nlabel='{typo}'\n")
	if _, err := LoadCwdLabels(home); err == nil || !strings.Contains(err.Error(), "{typo}") {
		t.Fatalf("unknown placeholder must refuse the load STATICALLY, got %v", err)
	}
	writeConfig(t, home, "[[cwd_label]]\nmatch='^x$'\n")
	if _, err := LoadCwdLabels(home); err == nil {
		t.Fatalf("missing label must refuse the load")
	}
}
