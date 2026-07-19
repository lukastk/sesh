package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// The [mesh] table in <SESH_HOME>/config.toml — cadence policy for the background
// mesh sync (issue #1: the fixed 1 Hz full-snapshot poll burned ~450 MB/hr of
// mobile data on the termux leaf).
//
//	[mesh]
//	idle_interval = "60s"
//
// The sync runs at full cadence (1 Hz) while the mesh view is in demand — a
// GET /v1/mesh read or an all-machines fan-out within the active window — or
// when [[hooks]] are configured (the eventer is a standing consumer of remote
// state). Otherwise it backs off to one round per idle_interval. "0s" disables
// idling entirely (always full cadence).

// DefaultMeshIdleInterval is the built-in idle cadence when [mesh] idle_interval
// is unset.
const DefaultMeshIdleInterval = 60 * time.Second

// MeshConfig is the resolved [mesh] table.
type MeshConfig struct {
	// IdleInterval is the sync cadence while nothing consumes the mesh view.
	// 0 = never idle (always full cadence).
	IdleInterval time.Duration
}

type meshFileTable struct {
	IdleInterval *string `toml:"idle_interval"`
}

type meshConfigFile struct {
	Mesh *meshFileTable `toml:"mesh"`
}

// LoadMesh reads the [mesh] table. Missing file/table/key = the built-in default.
// A present-but-broken value is a LOUD error (parity with the other loaders) —
// a mistyped interval must refuse the daemon, not silently poll at the default.
func LoadMesh(home string) (MeshConfig, error) {
	out := MeshConfig{IdleInterval: DefaultMeshIdleInterval}
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f meshConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return out, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	if f.Mesh == nil || f.Mesh.IdleInterval == nil {
		return out, nil
	}
	d, err := time.ParseDuration(*f.Mesh.IdleInterval)
	if err != nil {
		return out, fmt.Errorf("config: [mesh] idle_interval %q: %w", *f.Mesh.IdleInterval, err)
	}
	if d < 0 {
		return out, fmt.Errorf("config: [mesh] idle_interval %q must not be negative", *f.Mesh.IdleInterval)
	}
	out.IdleInterval = d
	return out, nil
}
