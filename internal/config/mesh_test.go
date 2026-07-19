package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeMeshConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return home
}

// TestLoadMesh covers the [mesh] idle_interval knob: default when absent, an
// explicit value, "0s" = never idle, and loud refusal of broken values (a typo
// must not silently poll at the default).
func TestLoadMesh(t *testing.T) {
	if c, err := LoadMesh(t.TempDir()); err != nil || c.IdleInterval != DefaultMeshIdleInterval {
		t.Fatalf("missing file: (%v, %v), want default %v", c.IdleInterval, err, DefaultMeshIdleInterval)
	}
	if c, err := LoadMesh(writeMeshConfig(t, "[ticket]\nsend_prepend = true\n")); err != nil || c.IdleInterval != DefaultMeshIdleInterval {
		t.Fatalf("missing table: (%v, %v), want default", c.IdleInterval, err)
	}
	if c, err := LoadMesh(writeMeshConfig(t, "[mesh]\nidle_interval = \"5m\"\n")); err != nil || c.IdleInterval != 5*time.Minute {
		t.Fatalf("explicit 5m: (%v, %v)", c.IdleInterval, err)
	}
	if c, err := LoadMesh(writeMeshConfig(t, "[mesh]\nidle_interval = \"0s\"\n")); err != nil || c.IdleInterval != 0 {
		t.Fatalf("0s (never idle): (%v, %v)", c.IdleInterval, err)
	}
	if _, err := LoadMesh(writeMeshConfig(t, "[mesh]\nidle_interval = \"sixty\"\n")); err == nil {
		t.Fatalf("unparseable idle_interval must be a loud error")
	}
	if _, err := LoadMesh(writeMeshConfig(t, "[mesh]\nidle_interval = \"-10s\"\n")); err == nil {
		t.Fatalf("negative idle_interval must be a loud error")
	}
}
