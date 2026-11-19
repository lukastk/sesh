package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// The [idle_stop] table (_dev/IDLE_STOP.md): the owning daemon stops a headful
// agent thread that has been idle for longer than `after`.
//
//	[idle_stop]
//	after = "48h"
//	flagged_after = "168h"   # optional: the limit for FLAGGED threads
//
// Absent = no machine-wide limit (only threads with their own `idle_stop`
// duration are ever stopped). flagged_after applies to a flagged thread that has
// no per-thread override; without it a flagged thread uses `after` like any other.
// The shared config.toml carries the fleet value; a machine overrides it in its
// config.local.toml overlay.

// IdleStopConfig is the resolved [idle_stop] table.
type IdleStopConfig struct {
	// After is the machine-wide idle limit; 0 = none.
	After time.Duration
	// FlaggedAfter is the idle limit for a flagged thread without a per-thread
	// override; 0 = flagged threads use After.
	FlaggedAfter time.Duration
}

type idleStopFileTable struct {
	After        *string `toml:"after"`
	FlaggedAfter *string `toml:"flagged_after"`
}

type idleStopConfigFile struct {
	IdleStop *idleStopFileTable `toml:"idle_stop"`
}

// LoadIdleStop reads [idle_stop]. Missing file/table/key = no limit. A present
// value must be a positive duration: "0s" would stop every idle thread on the
// next sweep, which is never what a typo meant.
func LoadIdleStop(home string) (IdleStopConfig, error) {
	var out IdleStopConfig
	raw, err := readConfig(home)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	var f idleStopConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return out, fmt.Errorf("config: parse %s: %w", ConfigDesc(home), err)
	}
	if f.IdleStop == nil {
		return out, nil
	}
	if f.IdleStop.After != nil {
		d, err := parseIdleLimit("after", *f.IdleStop.After)
		if err != nil {
			return out, err
		}
		out.After = d
	}
	if f.IdleStop.FlaggedAfter != nil {
		d, err := parseIdleLimit("flagged_after", *f.IdleStop.FlaggedAfter)
		if err != nil {
			return out, err
		}
		out.FlaggedAfter = d
	}
	return out, nil
}

// parseIdleLimit reads one [idle_stop] duration, which must be positive.
func parseIdleLimit(key, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("config: [idle_stop] %s %q: %w", key, value, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("config: [idle_stop] %s %q must be positive (remove the key to turn it off)", key, value)
	}
	return d, nil
}
