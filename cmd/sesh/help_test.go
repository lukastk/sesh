package main

import (
	"strings"
	"testing"
)

// subcommandSets mirrors each parent command's dispatch switch. The meta-test asserts
// every declared subcommand has a help entry — the "no silent gap" guard for help.
var subcommandSets = map[string][]string{
	"daemon": {"run", "start", "stop", "restart", "status"},
	"tmux":   {"current", "info", "create-session", "create-pane", "send-text", "stage-file", "nav", "master-current"},
	"thread": {"new", "list", "stop", "pane", "capture", "status", "send", "send-headless",
		"headless-reply", "rename", "info", "adopt", "transcript", "notify", "reparent",
		"tag", "archive", "delete", "resume", "headful", "grid", "snapshot"},
	"ticket": {"create", "list", "set-status", "needs-input", "send-prompt"},
	"master": {"up", "window", "attach", "down", "ensure", "watchers"},
	"peer":   {"add", "list", "remove"},
	"hooks":  {"list", "enable", "disable", "test"},
	"meta":   {"set", "get", "unset", "list"},
	"matrix": {"grid", "skips"},
}

func TestHelpCoversEveryTopLevelCommand(t *testing.T) {
	for _, cmd := range topLevelCommands {
		if _, ok := helpRegistry[cmd]; !ok {
			t.Errorf("dispatched top-level command %q has NO help entry", cmd)
		}
	}
}

func TestHelpCoversEverySubcommand(t *testing.T) {
	for parent, subs := range subcommandSets {
		for _, sub := range subs {
			key := parent + " " + sub
			if _, ok := helpRegistry[key]; !ok {
				t.Errorf("dispatched subcommand %q has NO help entry", key)
			}
		}
	}
}

func TestHelpEntriesWellFormed(t *testing.T) {
	for key, h := range helpRegistry {
		if strings.TrimSpace(h.summary) == "" {
			t.Errorf("%q: empty summary", key)
		}
		if !strings.HasPrefix(h.usage, "sesh "+strings.SplitN(key, " ", 2)[0]) {
			t.Errorf("%q: usage %q should start with the command", key, h.usage)
		}
		if len(h.examples) == 0 {
			t.Errorf("%q: no examples (the bar is agent-usable from --help alone)", key)
		}
	}
}

func TestRenderHelpNamesTheCommand(t *testing.T) {
	for key := range helpRegistry {
		out := renderHelp(strings.Split(key, " "))
		if !strings.Contains(out, "sesh "+key) {
			t.Errorf("help for %q does not name the command:\n%s", key, out)
		}
		if !strings.Contains(out, "usage:") {
			t.Errorf("help for %q has no usage line", key)
		}
	}
	// The root overview lists the commands.
	root := renderHelp(nil)
	if !strings.Contains(root, "commands:") || !strings.Contains(root, "thread") {
		t.Errorf("root help missing the command list:\n%s", root)
	}
}

func TestResolveHelpRequest(t *testing.T) {
	cases := []struct {
		args     []string
		wantHelp bool
		wantPath []string
	}{
		{[]string{"--help"}, true, nil},
		{[]string{"-h"}, true, nil},
		{[]string{"help"}, true, nil},
		{[]string{"help", "thread", "new"}, true, []string{"thread", "new"}},
		{[]string{"thread", "new", "--help"}, true, []string{"thread", "new"}},
		{[]string{"thread", "--help"}, true, []string{"thread"}},
		{[]string{"thread", "capture", "--machine", "x", "--help"}, true, []string{"thread", "capture"}},
		{[]string{"thread", "list", "--json"}, false, nil},
		{[]string{"tui"}, false, nil},
	}
	for _, c := range cases {
		path, ok := resolveHelpRequest(c.args)
		if ok != c.wantHelp {
			t.Errorf("resolveHelpRequest(%v) help=%v, want %v", c.args, ok, c.wantHelp)
			continue
		}
		if ok && strings.Join(path, " ") != strings.Join(c.wantPath, " ") {
			t.Errorf("resolveHelpRequest(%v) path=%v, want %v", c.args, path, c.wantPath)
		}
	}
}
