package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestHelpFlagsCoverUsageExactly is the no-silent-gap guard for per-flag help: every
// flag in a command's usage line must have a flagDoc, and every flagDoc must be a real
// usage flag (no orphans, no dups, no empty descriptions). So a new/renamed flag can't
// land in the usage line without an explanation.
func TestHelpFlagsCoverUsageExactly(t *testing.T) {
	// Flags are dash-prefixed tokens (single- or double-dash) at a token boundary
	// (start, space, '[', '(', or '|') in the usage line.
	flagRe := regexp.MustCompile(`(?:^|[\s\[(|])(-{1,2}[a-z][a-z0-9-]*)`)
	for key := range flagDocs {
		if _, ok := helpRegistry[key]; !ok {
			t.Errorf("flagDocs has key %q with no help registry entry", key)
		}
	}
	for key, h := range helpRegistry {
		usage := map[string]bool{}
		for _, m := range flagRe.FindAllStringSubmatch(h.usage, -1) {
			usage[m[1]] = true
		}
		doc := map[string]bool{}
		for _, fd := range flagDocs[key] {
			if doc[fd.name] {
				t.Errorf("%q: flag %q documented twice", key, fd.name)
			}
			doc[fd.name] = true
			if strings.TrimSpace(fd.desc) == "" {
				t.Errorf("%q: flag %q has an empty description", key, fd.name)
			}
			if !usage[fd.name] {
				t.Errorf("%q: documented flag %q is not in the usage line %q", key, fd.name, h.usage)
			}
		}
		for f := range usage {
			if !doc[f] {
				t.Errorf("%q: usage flag %q has no flag doc (cmd/sesh/help_flags.go)", key, f)
			}
		}
	}
}

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

func TestHelpTreeCoversEverything(t *testing.T) {
	tree := renderHelpTree()
	// Every top-level command appears...
	for _, cmd := range topLevelCommands {
		if !strings.Contains(tree, cmd) {
			t.Errorf("help-tree omits top-level command %q", cmd)
		}
	}
	// ...and every declared subcommand's leaf name appears under it.
	for parent, subs := range subcommandSets {
		for _, sub := range subs {
			if !strings.Contains(tree, sub) {
				t.Errorf("help-tree omits subcommand %q of %q", sub, parent)
			}
		}
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
