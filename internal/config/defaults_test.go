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

// mypi is a configurable harness: a default agent, its own [spawn.mypi] table,
// and the same loud sandbox refusal pi has (neither has a permission system).
func TestMyPiIsConfigurableLikePi(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[defaults]\nagent = 'mypi'\n\n[spawn]\nmode = 'default'\n\n[spawn.mypi]\nmode = 'yolo'\nargs = ['--thinking', 'high']\n\n[flags]\nheuristic_agents = ['mypi']\n")

	defaults, err := LoadDefaults(home)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Agent != "mypi" {
		t.Fatalf("[defaults] agent = %q", defaults.Agent)
	}
	spawn, err := LoadSpawn(home)
	if err != nil {
		t.Fatal(err)
	}
	if spawn.ModeFor("mypi") != "yolo" || spawn.ModeFor("pi") != "default" {
		t.Fatalf("modes: mypi=%q pi=%q (the tables are independent)", spawn.ModeFor("mypi"), spawn.ModeFor("pi"))
	}
	args := spawn.ArgsFor("mypi")
	if len(args) != 2 || args[0] != "--thinking" || args[1] != "high" {
		t.Fatalf("[spawn.mypi] args = %#v", args)
	}
	if len(spawn.ArgsFor("pi")) != 0 {
		t.Fatalf("[spawn.mypi] args leaked onto pi: %#v", spawn.ArgsFor("pi"))
	}
	flags, err := LoadFlags(home)
	if err != nil {
		t.Fatal(err)
	}
	if !flags.HeuristicAgents["mypi"] {
		t.Fatalf("[flags] heuristic_agents = %#v", flags.HeuristicAgents)
	}
}

func TestMyPiSandboxIsLoud(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "[spawn.mypi]\nmode = 'sandbox'\n")
	if _, err := LoadSpawn(home); err == nil {
		t.Fatal("a sandboxed mypi was accepted")
	} else if !strings.Contains(err.Error(), "mypi") {
		t.Fatalf("sandbox refusal does not name mypi: %v", err)
	}
}
