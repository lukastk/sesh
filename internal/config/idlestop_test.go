package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadIdleStop(t *testing.T) {
	for _, tc := range []struct {
		name, toml string
		want       time.Duration
		errIn      string
	}{
		{"absent", "", 0, ""},
		{"set", "[idle_stop]\nafter = \"24h\"\n", 24 * time.Hour, ""},
		{"zero refused", "[idle_stop]\nafter = \"0s\"\n", 0, "must be positive"},
		{"garbage refused", "[idle_stop]\nafter = \"a day\"\n", 0, "[idle_stop] after"},
		{"flagged_after alone", "[idle_stop]\nflagged_after = \"168h\"\n", 0, ""},
		{"flagged_after zero refused", "[idle_stop]\nafter = \"48h\"\nflagged_after = \"0s\"\n", 0, "flagged_after"},
	} {
		home := t.TempDir()
		if tc.toml != "" {
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(tc.toml), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := LoadIdleStop(home)
		if tc.errIn != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errIn) {
				t.Errorf("%s: want error containing %q, got %v", tc.name, tc.errIn, err)
			}
			continue
		}
		if err != nil || got.After != tc.want {
			t.Errorf("%s: got %v, %v; want %s", tc.name, got.After, err, tc.want)
		}
		if tc.name == "flagged_after alone" && (err != nil || got.FlaggedAfter != 168*time.Hour || got.After != 0) {
			t.Errorf("flagged_after alone: got %+v, %v", got, err)
		}
	}
}
