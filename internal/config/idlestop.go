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
//	after = "24h"
//
// Absent = no machine-wide limit (only threads with their own `idle_stop`
// duration are ever stopped). The shared config.toml carries the fleet value; a
// machine overrides it in its config.local.toml overlay.

// IdleStopConfig is the resolved [idle_stop] table.
type IdleStopConfig struct {
	// After is the machine-wide idle limit; 0 = none.
	After time.Duration
}

type idleStopFileTable struct {
	After *string `toml:"after"`
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
	if f.IdleStop == nil || f.IdleStop.After == nil {
		return out, nil
	}
	d, err := time.ParseDuration(*f.IdleStop.After)
	if err != nil {
		return out, fmt.Errorf("config: [idle_stop] after %q: %w", *f.IdleStop.After, err)
	}
	if d <= 0 {
		return out, fmt.Errorf("config: [idle_stop] after %q must be positive (remove the key to turn idle-stop off)", *f.IdleStop.After)
	}
	out.After = d
	return out, nil
}
