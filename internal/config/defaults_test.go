package config

import (
	"strings"
	"testing"
)

func TestLoadDefaultsAgent(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[defaults]\nagent = 'pi'\nnotifications = false\n")

	got, err := LoadDefaults(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "pi" {
		t.Errorf("agent = %q, want pi", got.Agent)
	}
	if got.Notifications == nil || *got.Notifications {
		t.Errorf("notifications = %v, want explicit false", got.Notifications)
	}
}

func TestLoadDefaultsAgentUnset(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[defaults]\nnotifications = true\n")

	got, err := LoadDefaults(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "" {
		t.Errorf("unset agent = %q, want empty (no implicit harness)", got.Agent)
	}
}

func TestLoadDefaultsAgentInvalidIsLoud(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[defaults]\nagent = 'gemini'\n")

	_, err := LoadDefaults(home)
	if err == nil {
		t.Fatal("invalid [defaults] agent must be a loud error")
	}
	if !strings.Contains(err.Error(), "[defaults] agent") || !strings.Contains(err.Error(), "gemini") {
		t.Fatalf("error does not name the field and bad value: %v", err)
	}
}
