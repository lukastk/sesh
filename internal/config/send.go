package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// The [send] table in <SESH_HOME>/config.toml — the typing guard on every delivery
// into a live pane (_dev/SCHEDULING.md §6.5.1).
//
//	[send]
//	respect_typing          = "60s"   # "0s" disables the guard machine-wide
//	respect_typing_deadline = "10m"   # how long a deferred delivery waits before it fails + flags
//
// A pane paste is `paste-buffer` then Enter, so text delivered while a human is
// mid-line in that pane is appended to their half-typed prompt and SUBMITTED with
// it. The guard reads the session's newest tmux client_activity (last INPUT from a
// viewer, H48) and holds the delivery until the pane has been quiet for
// respect_typing; a delivery still held at respect_typing_deadline fails loudly and
// auto-flags the target thread instead of pasting anyway. Per-call flags override
// both values; the daemon reads this table at start like every other section.

// DefaultRespectTyping is the built-in quiet window when [send] respect_typing is
// unset. Nonzero by default: a wait is benign, a submitted half-typed line is not.
const DefaultRespectTyping = 60 * time.Second

// DefaultRespectTypingDeadline bounds a deferred delivery when [send]
// respect_typing_deadline is unset.
const DefaultRespectTypingDeadline = 10 * time.Minute

// SendConfig is the resolved [send] table.
type SendConfig struct {
	// RespectTyping is how long a pane must be free of viewer input before a
	// delivery is pasted. 0 = the guard is off.
	RespectTyping time.Duration
	// RespectTypingDeadline is how long a held delivery waits for that quiet
	// window before failing loudly (and flagging the thread).
	RespectTypingDeadline time.Duration
}

type sendFileTable struct {
	RespectTyping         *string `toml:"respect_typing"`
	RespectTypingDeadline *string `toml:"respect_typing_deadline"`
}

type sendConfigFile struct {
	Send *sendFileTable `toml:"send"`
}

// LoadSend reads the [send] table. Missing file/table/key = the built-in defaults.
// A present-but-broken value is a LOUD error (parity with the other loaders).
func LoadSend(home string) (SendConfig, error) {
	out := SendConfig{RespectTyping: DefaultRespectTyping, RespectTypingDeadline: DefaultRespectTypingDeadline}
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f sendConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return out, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	if f.Send == nil {
		return out, nil
	}
	if f.Send.RespectTyping != nil {
		d, err := parseNonNegativeDuration("[send] respect_typing", *f.Send.RespectTyping)
		if err != nil {
			return out, err
		}
		out.RespectTyping = d
	}
	if f.Send.RespectTypingDeadline != nil {
		d, err := parseNonNegativeDuration("[send] respect_typing_deadline", *f.Send.RespectTypingDeadline)
		if err != nil {
			return out, err
		}
		if d <= 0 {
			return out, fmt.Errorf("config: [send] respect_typing_deadline %q must be positive (a held delivery needs a bound)", *f.Send.RespectTypingDeadline)
		}
		out.RespectTypingDeadline = d
	}
	return out, nil
}

// parseNonNegativeDuration parses a config duration, refusing negatives loudly.
func parseNonNegativeDuration(field, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", field, raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("config: %s %q must not be negative", field, raw)
	}
	return d, nil
}
