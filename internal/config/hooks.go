package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Event hooks (PARITY_ROADMAP B1, v1's [[hooks]] re-built on the mesh cache):
// `[[hooks]]` entries in <SESH_HOME>/config.toml run a user command when the
// daemon OBSERVES a thread event. Observation is mesh-wide by design (v1's
// observer-bound rule): the daemon diffs its merged view — its own threads
// plus every peer's replicated snapshots — so a hook enabled on the machine
// you sit at fires for a thread going idle ANYWHERE, with no cross-machine
// plumbing in the hook itself.
//
//	[[hooks]]
//	name    = "notify-idle"
//	event   = "busy_changed"
//	to      = "idle"
//	command = "sesh-notify"
//
// The command runs through $SHELL -c (deploy-env parity: the same env panes
// get) with SESH_EVENT_* variables describing the event. A broken hook
// definition refuses the daemon loudly.

// Hook is one [[hooks]] entry.
type Hook struct {
	Name    string `toml:"name"`    // identifies the hook for `sesh hooks enable/disable/test`
	Event   string `toml:"event"`   // event type (see ValidHookEvents)
	From    string `toml:"from"`    // busy_changed/head_changed edge filter (optional)
	To      string `toml:"to"`      // busy_changed/head_changed edge filter (optional)
	Agent   string `toml:"agent"`   // thread matcher: claude|codex|pi (optional)
	Machine string `toml:"machine"` // thread matcher: owner machine (optional)
	Tag     string `toml:"tag"`     // thread matcher: has this tag (optional)
	Command string `toml:"command"` // shell command ($SHELL -c)
}

// ValidHookEvents are the event types hooks may subscribe to (stable strings;
// they reach commands verbatim via SESH_EVENT).
var ValidHookEvents = []string{
	"busy_changed", "head_changed",
	"thread_created", "thread_deleted",
	"thread_archived", "thread_unarchived", "thread_renamed",
}

type hooksFile struct {
	Hooks []Hook `toml:"hooks"`
}

// LoadHooks reads and validates the [[hooks]] entries from <home>/config.toml.
// Missing file or no hooks = (nil, nil). A broken entry (missing fields,
// unknown event, duplicate name) is a LOUD error — the daemon refuses to start
// on a misconfigured hook rather than silently never firing it.
func LoadHooks(home string) ([]Hook, error) {
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f hooksFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	seen := map[string]bool{}
	for i, h := range f.Hooks {
		if h.Name == "" || h.Event == "" || h.Command == "" {
			return nil, fmt.Errorf("config: hooks entry %d: name, event and command are all required", i+1)
		}
		if seen[h.Name] {
			return nil, fmt.Errorf("config: hooks entry %d: duplicate name %q", i+1, h.Name)
		}
		seen[h.Name] = true
		valid := false
		for _, ev := range ValidHookEvents {
			if h.Event == ev {
				valid = true
			}
		}
		if !valid {
			return nil, fmt.Errorf("config: hook %q: unknown event %q (valid: %v)", h.Name, h.Event, ValidHookEvents)
		}
	}
	return f.Hooks, nil
}
