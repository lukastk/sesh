package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The COMMAND REGISTRY: every action the normal-mode grid can perform, as a named
// command. It is the single source of truth for three surfaces that used to be
// maintained by hand and could drift apart:
//
//   - the KEYMAP — which key runs what (defaults here, overridable per command in
//     config via [[tui.key]]; see ResolveKeymap),
//   - the COMMAND PALETTE (`p`) — fuzzy-search every command by description and run
//     it, so a rarely-used action needs no key at all,
//   - the `?` help popup — rendered FROM the registry, so it cannot lie about what
//     a key does after a rebind.
//
// Keys are deliberately scarce: only the frequent commands carry one by default
// (the set Lukas fixed in 2026-08), everything else is palette-only. Adding a
// command here is all it takes to make it reachable, searchable and bindable.
//
// The OWNER GATE is keyed by command ID, NOT by key string — a rebound key must
// keep its offline-owner refusal (see requiresReachableOwner).

// Command is one invocable normal-mode action.
type Command struct {
	// ID is the stable identifier used in config ([[tui.key]] command = "...")
	// and in the dispatch switch. It never changes with a rebind.
	ID string
	// Desc is the human description — what the `?` popup lists and what the
	// palette fuzzy-searches. Written as an imperative phrase.
	Desc string
	// Keys are the DEFAULT bindings (bubbletea key strings, e.g. "f", "ctrl+f",
	// "up"). Empty = palette-only by default. Config replaces this list per
	// command; see ResolveKeymap.
	Keys []string
}

// commands is the registry, in DISPLAY order — the order of the `?` popup and of
// the palette's unfiltered list. It mirrors the old hand-maintained keymap order
// so the help reads the same as it always did.
var commands = []Command{
	// --- motion (the grid's cursor / viewport / layout) ---
	{ID: "cursor-up", Desc: "move the selection up", Keys: []string{"up", "k"}},
	{ID: "cursor-down", Desc: "move the selection down", Keys: []string{"down", "j"}},
	{ID: "scroll-up", Desc: "scroll the viewport up", Keys: []string{"ctrl+k"}},
	{ID: "scroll-down", Desc: "scroll the viewport down", Keys: []string{"ctrl+j"}},
	{ID: "fold", Desc: "collapse the tree fold", Keys: []string{"left"}},
	{ID: "unfold", Desc: "expand the tree fold", Keys: []string{"right"}},
	{ID: "pan-left", Desc: "pan columns left", Keys: []string{"ctrl+h"}},
	{ID: "pan-right", Desc: "pan columns right", Keys: []string{"ctrl+l"}},

	// --- modes / navigation ---
	{ID: "enter", Desc: "enter the thread (revives a dead one)", Keys: []string{"enter"}},
	{ID: "filter", Desc: "filter mode (fuzzy)", Keys: []string{"/"}},
	{ID: "view-picker", Desc: "view picker", Keys: []string{"tab"}},
	{ID: "palette", Desc: "command palette", Keys: []string{"p"}},

	// --- thread actions ---
	{ID: "flag", Desc: "toggle the needs-attention flag ⚑", Keys: []string{"f"}},
	{ID: "flag-gate", Desc: "toggle auto-flagging (⌁ when disabled)", Keys: []string{"ctrl+f"}},
	{ID: "hold", Desc: "hold until tomorrow / release", Keys: []string{"h"}},
	{ID: "hold-until", Desc: "hold until a date (prompt)"},
	{ID: "rename", Desc: "rename", Keys: []string{"r"}},
	{ID: "tag-add", Desc: "add a tag"},
	{ID: "tag-remove", Desc: "remove a tag"},
	{ID: "set-parent", Desc: "set the parent thread (pick from a list)"},
	{ID: "set-parent-uuid", Desc: "set the parent thread by UUID"},
	{ID: "new-virtual", Desc: "new virtual group"},
	{ID: "pin", Desc: "pin to the top block"},
	{ID: "unpin", Desc: "unpin", Keys: []string{"u"}},
	{ID: "move-mode", Desc: "move mode (reorder pinned rows)", Keys: []string{"m"}},
	{ID: "new-divider", Desc: "new divider"},
	{ID: "fork", Desc: "fork the thread (headless copy)"},
	{ID: "stop", Desc: "stop the runtime (keep the record)", Keys: []string{"x"}},
	{ID: "archive", Desc: "archive / unarchive (instant)", Keys: []string{"a"}},
	{ID: "undo-archive", Desc: "undo the last archive", Keys: []string{"U"}},
	{ID: "delete", Desc: "delete (y/n confirm)"},

	// --- inspect / display toggles ---
	{ID: "tickets", Desc: "tickets view", Keys: []string{"K"}},
	{ID: "details", Desc: "thread details", Keys: []string{"I"}},
	{ID: "toggle-id", Desc: "toggle the ID column", Keys: []string{"i"}},
	{ID: "toggle-width-cap", Desc: "toggle the column-width cap", Keys: []string{"w"}},
	{ID: "uuid", Desc: "show the full UUID (c copies)", Keys: []string{"y"}},
	{ID: "notify", Desc: "toggle notifications", Keys: []string{"n"}},
	{ID: "toggle-offline", Desc: "show / hide offline machines"},
	{ID: "refresh", Desc: "force refresh", Keys: []string{"R"}},
	{ID: "help", Desc: "keymap help", Keys: []string{"?"}},
	{ID: "dismiss", Desc: "dismiss the message line", Keys: []string{"esc", "q"}},
	{ID: "quit", Desc: "quit"},
}

// Commands exposes the registry (help rendering, tests, docs).
func Commands() []Command { return commands }

// commandByID looks a command up by its stable id.
func commandByID(id string) (Command, bool) {
	for _, c := range commands {
		if c.ID == id {
			return c, true
		}
	}
	return Command{}, false
}

// hardQuitKey ALWAYS quits and is deliberately NOT in the registry, so it cannot be
// rebound or unbound: a config that unbinds `quit` (or binds every key to something
// else) must never leave the TUI with no way out. Documented as the escape hatch.
const hardQuitKey = "ctrl+c"

// Keymap is the resolved bindings: which key runs which command, and — for the
// help/palette — which keys each command carries. Build it with DefaultKeymap or
// ResolveKeymap; the zero value is unusable (use Model.km(), which falls back to
// the defaults so a struct-literal Model behaves like the shipped TUI).
type Keymap struct {
	byKey map[string]string   // key string -> command id
	byCmd map[string][]string // command id -> its keys, in display order
}

// Command returns the command id bound to key, or "" when nothing is.
func (k *Keymap) Command(key string) string {
	if k == nil {
		return ""
	}
	return k.byKey[key]
}

// KeysFor returns the keys bound to a command, in display order (nil = none, i.e.
// the command is reachable only from the palette).
func (k *Keymap) KeysFor(id string) []string {
	if k == nil {
		return nil
	}
	return k.byCmd[id]
}

// KeyLabel renders a command's keys the way the help and palette show them:
// "↑/k", "ctrl+f", or "" when the command has no key. Arrow names are shown as
// arrows — they are what the key caps say.
func (k *Keymap) KeyLabel(id string) string {
	keys := k.KeysFor(id)
	if len(keys) == 0 {
		return ""
	}
	out := make([]string, 0, len(keys))
	for _, s := range keys {
		out = append(out, prettyKey(s))
	}
	return strings.Join(out, "/")
}

// prettyKey renders one key string for display (arrows as glyphs, space as ␣).
func prettyKey(s string) string {
	switch s {
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	case " ":
		return "␣"
	}
	return s
}

// defaultKeymap is the registry's own bindings, built once.
var defaultKeymap = mustDefaultKeymap()

func mustDefaultKeymap() *Keymap {
	km, err := ResolveKeymap(nil)
	if err != nil {
		// The registry is a compile-time constant; a conflict in it is a bug, and
		// TestDefaultKeymapHasNoConflicts pins it. Failing loudly at init beats
		// shipping a TUI whose keys silently shadow each other.
		panic("tui: default keymap is inconsistent: " + err.Error())
	}
	return km
}

// DefaultKeymap is the built-in binding set (no config applied).
func DefaultKeymap() *Keymap { return defaultKeymap }

// KeySpec is one [[tui.key]] config entry: bind Key to Command. An empty Key
// UNBINDS the command (it stays reachable from the palette) — the same
// empty-clears convention [[tui.column_color]] / [[tui.glyph_color]] use.
type KeySpec struct {
	Command string
	Key     string
}

// ResolveKeymap overlays [[tui.key]] entries on the built-in defaults.
//
// Semantics, chosen so the rendered keymap can never lie:
//
//   - The FIRST entry naming a command replaces that command's default keys;
//     further entries for the same command ADD keys (so several keys can run one
//     command). `key = ""` leaves it unbound.
//   - A configured key WINS over a default: binding `f` to `delete` takes `f` away
//     from `flag`, and `flag` then renders (and behaves) as keyless rather than
//     claiming a key that runs something else.
//   - Two CONFIG entries fighting over one key is a LOUD error — there is no
//     defensible way to pick a winner, and silently dropping one would be exactly
//     the plausible-but-wrong behaviour this project forbids.
//   - An unknown command id or a malformed key string is a LOUD error.
func ResolveKeymap(specs []KeySpec) (*Keymap, error) {
	// Start from the registry's defaults.
	keys := make(map[string][]string, len(commands))
	for _, c := range commands {
		keys[c.ID] = append([]string(nil), c.Keys...)
	}

	cleared := make(map[string]bool, len(specs))    // command ids whose defaults are gone
	explicit := make(map[string]string, len(specs)) // key -> command id, from config only
	for i, sp := range specs {
		id := strings.TrimSpace(sp.Command)
		if id == "" {
			return nil, fmt.Errorf("[[tui.key]] entry %d: command is required (the command id, e.g. command = \"flag\")", i+1)
		}
		if _, ok := commandByID(id); !ok {
			return nil, fmt.Errorf("[[tui.key]] %q: unknown command — valid ids: %s", id, strings.Join(commandIDs(), ", "))
		}
		// First mention wipes the defaults, so `command = "flag", key = "F"` MOVES
		// flag rather than leaving it on both f and F.
		if !cleared[id] {
			keys[id], cleared[id] = nil, true
		}
		key := sp.Key
		if strings.TrimSpace(key) == "" {
			continue // deliberate unbind — palette-only
		}
		if err := validateKeyName(key); err != nil {
			return nil, fmt.Errorf("[[tui.key]] %q: %w", id, err)
		}
		if key == hardQuitKey {
			return nil, fmt.Errorf("[[tui.key]] %q: %q cannot be rebound — it is the always-available quit", id, hardQuitKey)
		}
		if prev, dup := explicit[key]; dup && prev != id {
			return nil, fmt.Errorf("[[tui.key]] key %q is bound to both %q and %q — a key can only run one command", key, prev, id)
		}
		explicit[key] = id
		keys[id] = append(keys[id], key)
	}

	// A configured key wins: take it off whatever command held it by default, so
	// the displaced command renders as keyless instead of advertising a key that
	// now runs something else.
	for key, owner := range explicit {
		for id, list := range keys {
			if id == owner {
				continue
			}
			keys[id] = removeStr(list, key)
		}
	}

	km := &Keymap{byKey: make(map[string]string), byCmd: make(map[string][]string)}
	for _, c := range commands { // registry order => deterministic errors
		for _, key := range keys[c.ID] {
			if prev, dup := km.byKey[key]; dup {
				return nil, fmt.Errorf("[[tui.key]] key %q is bound to both %q and %q — a key can only run one command", key, prev, c.ID)
			}
			km.byKey[key] = c.ID
		}
		if len(keys[c.ID]) > 0 {
			km.byCmd[c.ID] = keys[c.ID]
		}
	}
	return km, nil
}

// commandIDs lists every registered command id (for the unknown-command error).
func commandIDs() []string {
	out := make([]string, 0, len(commands))
	for _, c := range commands {
		out = append(out, c.ID)
	}
	return out
}

// validKeyNames is every NAMED key bubbletea can report (enter, esc, tab, up,
// ctrl+a…, f1…, backspace, …), derived from bubbletea itself rather than
// hand-listed, so it cannot drift from what KeyMsg.String() actually produces.
// The KeyType range covers bubbletea v1's special keys (negative) and the
// control/ASCII codes (0..127).
var validKeyNames = func() map[string]bool {
	out := make(map[string]bool, 96)
	for t := -100; t <= 200; t++ {
		if s := (tea.Key{Type: tea.KeyType(t)}).String(); s != "" {
			out[s] = true
		}
	}
	return out
}()

// validateKeyName checks a configured key string is one the TUI can ever receive:
// a single printable rune ("f", "?"), a named key ("enter", "ctrl+f", "up"), or
// either with an `alt+` prefix. A typo like "ctlr+f" would otherwise bind a key
// that can never fire — a silent misconfiguration, which this project forbids.
func validateKeyName(key string) error {
	name := strings.TrimPrefix(key, "alt+")
	if name == "" {
		return fmt.Errorf("key %q: nothing after the alt+ prefix", key)
	}
	if validKeyNames[name] {
		return nil
	}
	if r := []rune(name); len(r) == 1 {
		return nil
	}
	return fmt.Errorf("key %q is not a key this TUI can receive — use a single character (\"f\"), or a named key such as %s, optionally prefixed with alt+",
		key, strings.Join(sampleKeyNames(), ", "))
}

// sampleKeyNames is a short, stable sample of valid named keys for error text.
func sampleKeyNames() []string {
	want := []string{"enter", "esc", "tab", "up", "down", "left", "right", "ctrl+f", "backspace", "f1"}
	out := make([]string, 0, len(want))
	for _, s := range want {
		if validKeyNames[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// km resolves this model's keymap, falling back to the built-in defaults when none
// was injected. The fallback is load-bearing: nearly every unit test builds a
// struct-literal Model, and without it those models would have NO keys at all and
// so would exercise nothing like the shipped TUI (the H80 zero-value lesson).
func (m Model) km() *Keymap {
	if m.keymap != nil {
		return m.keymap
	}
	return defaultKeymap
}

// WithKeymap injects the resolved [[tui.key]] bindings (cmd/sesh/tui.go).
func (m Model) WithKeymap(km *Keymap) Model {
	m.keymap = km
	return m
}
