package config

// [flags] — the auto-flagging policy knobs (api schema 44, ticket df4fb07a).
// Auto-flagging itself is always on for REPORTED turn ends (the in-agent
// harness hooks are exact); heuristic_agents opts specific agents into
// flagging on the content-diff busy→idle edge as a FALLBACK (default: none —
// the heuristic can mistake a user's own settle for a turn end, so it is
// opt-in per agent, per the ticket).

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type flagsConfigFile struct {
	Flags *struct {
		HeuristicAgents []string `toml:"heuristic_agents"`
	} `toml:"flags"`
}

// FlagsConfig is the resolved [flags] policy.
type FlagsConfig struct {
	// HeuristicAgents: agent kinds whose HEURISTIC busy→idle edges auto-flag
	// (reported edges always do). Unknown agent kinds refuse loudly.
	HeuristicAgents map[string]bool
}

// LoadFlags reads [flags] from <home>/config.toml. Missing file/section =
// the zero policy (no heuristic flagging).
func LoadFlags(home string) (FlagsConfig, error) {
	out := FlagsConfig{HeuristicAgents: map[string]bool{}}
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f flagsConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return out, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	if f.Flags == nil {
		return out, nil
	}
	for _, a := range f.Flags.HeuristicAgents {
		switch a {
		case "claude", "codex", "pi":
			out.HeuristicAgents[a] = true
		default:
			return out, fmt.Errorf("config: [flags] heuristic_agents: unknown agent %q (want claude|codex|pi)", a)
		}
	}
	return out, nil
}
