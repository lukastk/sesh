package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// The [schedules] table in <SESH_HOME>/config.toml — the machine-wide policy for
// `sesh schedule` (_dev/SCHEDULING.md §9.2–9.3).
//
//	[schedules]
//	enabled                = true   # the kill switch: false = this daemon fires nothing
//	disable_after_failures = 10     # a schedule failing this many times running is disabled, loudly
//
// The schedules themselves are records in the store (they are mutable at
// runtime through the API); only this policy is config, read at daemon start.

// DefaultScheduleFailureBreaker is the built-in consecutive-failure limit.
const DefaultScheduleFailureBreaker = 10

// SchedulesConfig is the resolved [schedules] table.
type SchedulesConfig struct {
	Enabled bool
	// DisableAfterFailures disables a schedule whose fail streak reaches it;
	// 0 = never disable automatically.
	DisableAfterFailures int
}

type schedulesFileTable struct {
	Enabled              *bool `toml:"enabled"`
	DisableAfterFailures *int  `toml:"disable_after_failures"`
}

type schedulesConfigFile struct {
	Schedules *schedulesFileTable `toml:"schedules"`
}

// LoadSchedules reads the [schedules] table. Missing = enabled with the
// built-in breaker; a broken value is a loud error.
func LoadSchedules(home string) (SchedulesConfig, error) {
	out := SchedulesConfig{Enabled: true, DisableAfterFailures: DefaultScheduleFailureBreaker}
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f schedulesConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return out, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	if f.Schedules == nil {
		return out, nil
	}
	if f.Schedules.Enabled != nil {
		out.Enabled = *f.Schedules.Enabled
	}
	if f.Schedules.DisableAfterFailures != nil {
		if *f.Schedules.DisableAfterFailures < 0 {
			return out, fmt.Errorf("config: [schedules] disable_after_failures %d must not be negative (0 = never)", *f.Schedules.DisableAfterFailures)
		}
		out.DisableAfterFailures = *f.Schedules.DisableAfterFailures
	}
	return out, nil
}
