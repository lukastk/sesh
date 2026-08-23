package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// The DEFAULT KEYMAP is a spec, not an accident: in 2026-08 Lukas cut the grid's
// keys down to a fixed list and moved everything else into the command palette.
// This test pins that list EXACTLY — both halves. A key that quietly comes back
// (or goes missing) fails here, which is the whole point: the surviving set is
// the contract, and "which keys does the TUI have" must not drift by accretion.
func TestDefaultKeymapIsTheSurvivingSet(t *testing.T) {
	want := map[string]string{
		// motion
		"up": "cursor-up", "k": "cursor-up",
		"down": "cursor-down", "j": "cursor-down",
		"ctrl+k": "scroll-up", "ctrl+j": "scroll-down",
		"left": "fold", "right": "unfold",
		"ctrl+h": "pan-left", "ctrl+l": "pan-right",
		// modes
		"enter": "enter", "/": "filter", "tab": "view-picker", "p": "palette",
		// actions + toggles that kept a key
		"f": "flag", "ctrl+f": "flag-gate", "h": "hold", "r": "rename",
		"u": "unpin", "m": "move-mode", "K": "tickets", "I": "details",
		"i": "toggle-id", "w": "toggle-width-cap", "y": "uuid", "n": "notify",
		"x": "stop", "a": "archive", "U": "undo-archive", "R": "refresh",
		"?": "help",
		// esc/q KEEP quitting (Lukas asked for them back after the keymap cut). In
		// SIDEBAR mode they resolve to `dismiss` instead — see TestSidebarKeymapSwapsQuit.
		"esc": "quit", "q": "quit",
	}
	km := DefaultKeymap()
	for key, id := range want {
		if got := km.Command(key); got != id {
			t.Errorf("default key %q runs %q, want %q", key, got, id)
		}
	}
	for key, id := range km.byKey {
		if _, ok := want[key]; !ok {
			t.Errorf("key %q is bound to %q but is not in the surviving set — either it should be palette-only, or this test needs updating deliberately", key, id)
		}
	}

	// The commands whose keys were deliberately REMOVED are palette-only: their old
	// key must not run them, and must not run anything else either.
	for _, c := range []struct{ id, oldKey string }{
		{"hold-until", "H"}, {"tag-add", "t"}, {"tag-remove", "T"},
		{"set-parent-uuid", "P"}, {"new-virtual", "v"}, {"pin", "p"},
		{"new-divider", "D"}, {"fork", "F"}, {"delete", "d"},
		{"toggle-offline", "o"}, {"dismiss", ""},
	} {
		if c.oldKey == "" && len(km.KeysFor(c.id)) != 0 {
			t.Errorf("command %q should carry no default key", c.id)
		}
		if keys := km.KeysFor(c.id); len(keys) != 0 {
			t.Errorf("command %q should be palette-only, but carries %v", c.id, keys)
		}
		if got := km.Command(c.oldKey); got == c.id {
			t.Errorf("old key %q still runs %q — it was removed from the surviving set", c.oldKey, c.id)
		}
	}
}

// Every registry command must be unique, described, and (if it has keys) free of
// collisions. mustDefaultKeymap panics on a collision at init, so this also names
// the offender rather than leaving a bare panic.
func TestRegistryWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Commands() {
		if c.ID == "" || c.Desc == "" {
			t.Errorf("command %+v: id and desc are both required", c)
		}
		if seen[c.ID] {
			t.Errorf("duplicate command id %q", c.ID)
		}
		seen[c.ID] = true
		for _, k := range c.Keys {
			if err := validateKeyName(k); err != nil {
				t.Errorf("command %q default key: %v", c.ID, err)
			}
		}
	}
	if _, err := ResolveKeymap(nil); err != nil {
		t.Fatalf("default keymap must be conflict-free: %v", err)
	}
}

// ResolveKeymap's override rules: the first entry for a command REPLACES its
// defaults, further entries ADD, an empty key UNBINDS, and a configured key is
// taken away from whatever command held it by default (so the rendered keymap
// always matches what the key actually does).
func TestResolveKeymapOverrides(t *testing.T) {
	km, err := ResolveKeymap([]KeySpec{
		{Command: "fork", Key: "F"},      // give a palette-only command a key
		{Command: "flag", Key: "g"},      // MOVE flag off f...
		{Command: "flag", Key: "ctrl+g"}, // ...and give it a second key
		{Command: "archive", Key: ""},    // unbind: palette-only
		{Command: "delete", Key: "a"},    // steal `a` from archive's default
	})
	if err != nil {
		t.Fatalf("ResolveKeymap: %v", err)
	}
	if got := km.Command("F"); got != "fork" {
		t.Errorf("F should run fork, got %q", got)
	}
	if got := km.Command("g"); got != "flag" {
		t.Errorf("g should run flag, got %q", got)
	}
	if got := km.Command("ctrl+g"); got != "flag" {
		t.Errorf("ctrl+g should also run flag, got %q", got)
	}
	if got := km.Command("f"); got == "flag" {
		t.Errorf("f must no longer run flag — the first entry replaces the defaults")
	}
	if keys := km.KeysFor("archive"); len(keys) != 0 {
		t.Errorf("archive was unbound, but carries %v", keys)
	}
	// `a` was archive's default AND is now delete's: delete wins, and archive must
	// not still advertise it (that is the "keymap can never lie" rule).
	if got := km.Command("a"); got != "delete" {
		t.Errorf("a should run delete, got %q", got)
	}
	// Untouched commands keep their defaults.
	if got := km.Command("x"); got != "stop" {
		t.Errorf("an untouched command lost its default key: x runs %q", got)
	}
}

// A configured key displaces a DEFAULT holder that was not otherwise reconfigured.
func TestResolveKeymapConfiguredKeyDisplacesDefault(t *testing.T) {
	km, err := ResolveKeymap([]KeySpec{{Command: "fork", Key: "r"}})
	if err != nil {
		t.Fatalf("ResolveKeymap: %v", err)
	}
	if got := km.Command("r"); got != "fork" {
		t.Fatalf("r should run fork, got %q", got)
	}
	if keys := km.KeysFor("rename"); len(keys) != 0 {
		t.Errorf("rename kept %v after r was taken — the help would then advertise a key that runs fork", keys)
	}
}

// Misconfiguration is LOUD: nothing here may degrade into a key that silently
// never fires or a binding that silently loses.
func TestResolveKeymapLoudErrors(t *testing.T) {
	cases := []struct {
		name  string
		specs []KeySpec
		want  string
	}{
		{"unknown command", []KeySpec{{Command: "flagg", Key: "g"}}, "unknown command"},
		{"empty command", []KeySpec{{Command: "", Key: "g"}}, "command is required"},
		{"typo'd key", []KeySpec{{Command: "flag", Key: "ctlr+f"}}, "not a key this TUI can receive"},
		{"two commands, one key", []KeySpec{{Command: "fork", Key: "z"}, {Command: "delete", Key: "z"}}, "bound to both"},
		{"rebinding ctrl+c", []KeySpec{{Command: "flag", Key: "ctrl+c"}}, "cannot be rebound"},
		{"empty after alt+", []KeySpec{{Command: "flag", Key: "alt+"}}, "nothing after the alt+ prefix"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ResolveKeymap(c.specs)
			if err == nil {
				t.Fatalf("expected a loud error, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err, c.want)
			}
		})
	}
}

// validateKeyName accepts what bubbletea can actually deliver and nothing else.
func TestValidateKeyName(t *testing.T) {
	for _, ok := range []string{"f", "?", "F", "enter", "esc", "tab", "up", "ctrl+f", "backspace", "f12", "alt+f", "alt+enter", " ", "shift+tab"} {
		if err := validateKeyName(ok); err != nil {
			t.Errorf("validateKeyName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"ctlr+f", "Enter", "ctrl-f", "meta+x", "alt+"} {
		if err := validateKeyName(bad); err == nil {
			t.Errorf("validateKeyName(%q) = nil, want an error", bad)
		}
	}
}

// A struct-literal Model (how nearly every unit test builds one) must use the
// SHIPPED bindings — a nil keymap falling back to an empty map would leave those
// models with no keys at all, testing nothing like the real TUI (the H80 lesson).
func TestZeroValueModelUsesDefaultKeymap(t *testing.T) {
	var m Model
	if got := m.km().Command("f"); got != "flag" {
		t.Errorf("zero-value Model resolved f to %q, want flag", got)
	}
}

// The keys removed from the surviving set are INERT: pressing one changes nothing
// and runs nothing. This is what the old key-driven tests would no longer notice.
func TestRemovedKeysAreInert(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", Name: "one", Machine: "mymain", AgentKind: "pi", Tags: []string{"x"}}}
	for _, k := range []string{"H", "t", "T", "P", "v", "D", "F", "d", "o"} {
		m := Model{machine: "mymain", rows: []api.ThreadRow{row}, machines: selfMachines(), hideOffline: true}
		nm, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		got := nm.(Model)
		if cmd != nil {
			t.Errorf("removed key %q returned a command", k)
		}
		if got.prompting != promptNone || got.confirming != confirmNone || got.tagPopup ||
			got.parentPick || got.palette || !got.hideOffline {
			t.Errorf("removed key %q still did something (prompt=%v confirm=%v tag=%v pick=%v palette=%v hideOffline=%v)",
				k, got.prompting, got.confirming, got.tagPopup, got.parentPick, got.palette, got.hideOffline)
		}
	}
}

// `p` opens the palette (it was pin); q/esc dismiss the message lines instead of
// quitting; ctrl+c still quits and cannot be taken away.
func TestSurvivingKeyReassignments(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", Name: "one", Machine: "mymain", AgentKind: "pi"}}
	base := Model{machine: "mymain", rows: []api.ThreadRow{row}, machines: selfMachines()}

	nm, _ := base.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	got := nm.(Model)
	if !got.PaletteOpen() {
		t.Errorf("p should open the command palette")
	}
	if got.pendingFor("t1") != nil {
		t.Errorf("p must no longer pin the selected thread")
	}

	// esc/q still QUIT in the normal grid (restored after the keymap cut).
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyRunes, Runes: []rune("q")}} {
		_, cmd := base.handleKey(k)
		if cmd == nil {
			t.Fatalf("%v produced no command (want quit)", k)
		}
		if _, quit := cmd().(tea.QuitMsg); !quit {
			t.Errorf("%v must still quit the normal grid", k)
		}
	}

	_, cmd := base.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("ctrl+c produced no command")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Errorf("ctrl+c must always quit")
	}
}

// The `?` popup and the bottom hint are rendered FROM the live keymap, so a rebind
// can't leave them advertising a key that now runs something else.
func TestHelpAndHintFollowTheKeymap(t *testing.T) {
	km, err := ResolveKeymap([]KeySpec{
		{Command: "flag", Key: "g"},
		{Command: "help", Key: "f1"},
		{Command: "fork", Key: "F"},
	})
	if err != nil {
		t.Fatalf("ResolveKeymap: %v", err)
	}
	m := Model{keymap: km}
	strip := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	out := strip.ReplaceAllString(m.helpView(), "")
	if !strings.Contains(out, "g ") || !strings.Contains(out, "toggle the needs-attention flag") {
		t.Errorf("help should show flag on its rebound key g:\n%s", out)
	}
	if !strings.Contains(out, "F ") || !strings.Contains(out, "fork the thread") {
		t.Errorf("help should show fork on its newly-bound key F:\n%s", out)
	}
	// A palette-only command still LISTS (with an empty key column) — hiding it
	// would make the palette's contents undiscoverable.
	if !strings.Contains(out, "add a tag") {
		t.Errorf("help should still list keyless commands:\n%s", out)
	}
	if hint := strip.ReplaceAllString(m.legendHint(), ""); !strings.Contains(hint, "f1 keys") || !strings.Contains(hint, "p commands") {
		t.Errorf("legend hint should follow the keymap, got %q", hint)
	}
}

var errTest = &testErr{}

type testErr struct{}

func (*testErr) Error() string { return "test" }

// SIDEBAR mode swaps the keys that would QUIT over to `dismiss`: a persistent
// cockpit pane must not die to a stray keystroke (its pane would vanish and take
// the traveling slot with it), while the message lines it accumulates would
// otherwise sit there forever. ctrl+c stays the deliberate kill, and choosing
// `quit` explicitly from the palette is still honoured.
func TestSidebarKeymapSwapsQuit(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", Name: "one", Machine: "mymain", AgentKind: "pi"}}
	sb := Model{machine: "mymain", rows: []api.ThreadRow{row}, machines: selfMachines(), sidebar: true}
	sb.note, sb.actionErr = "hi", errTest

	for _, k := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyRunes, Runes: []rune("q")}} {
		nm, cmd := sb.handleKey(k)
		got := nm.(Model)
		if cmd != nil {
			if _, quit := cmd().(tea.QuitMsg); quit {
				t.Errorf("%v must not quit a SIDEBAR", k)
			}
		}
		if got.note != "" || got.actionErr != nil {
			t.Errorf("%v should dismiss the sidebar's message lines", k)
		}
	}
	// The rendered keymap must say so too — a sidebar's `?` popup that still
	// advertised esc/q as quit would be lying about what they do there.
	if got := sb.km().Command("esc"); got != "dismiss" {
		t.Errorf("sidebar keymap resolves esc to %q, want dismiss", got)
	}
	if keys := sb.km().KeysFor("quit"); len(keys) != 0 {
		t.Errorf("the sidebar keymap should show quit as keyless, got %v", keys)
	}
	if lbl := sb.km().KeyLabel("dismiss"); lbl != "esc/q" {
		t.Errorf("sidebar dismiss should carry esc/q, got %q", lbl)
	}
	// ctrl+c is outside the registry and still kills the sidebar.
	_, cmd := sb.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("ctrl+c produced no command in a sidebar")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Errorf("ctrl+c must still quit a sidebar (the deliberate kill)")
	}
	// Explicitly running `quit` from the palette IS honoured — a deliberate act,
	// not a stray keystroke.
	_, cmd = sb.runCommand("quit")
	if cmd == nil {
		t.Fatalf("palette quit produced no command in a sidebar")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Errorf("an explicit palette `quit` should quit even a sidebar")
	}
}

// A REBOUND quit follows the sidebar rule too — the swap is "the keys that would
// quit", not "esc and q specifically".
func TestSidebarSwapFollowsRebind(t *testing.T) {
	km, err := ResolveKeymap([]KeySpec{{Command: "quit", Key: "Q"}})
	if err != nil {
		t.Fatalf("ResolveKeymap: %v", err)
	}
	grid := Model{keymap: km}
	if got := grid.km().Command("Q"); got != "quit" {
		t.Errorf("grid: Q should quit, got %q", got)
	}
	side := Model{keymap: km, sidebar: true}
	if got := side.km().Command("Q"); got != "dismiss" {
		t.Errorf("sidebar: the rebound quit key Q should dismiss, got %q", got)
	}
	if got := side.km().Command("esc"); got == "dismiss" {
		t.Errorf("esc was moved off quit by config, so it should not dismiss either")
	}
}
