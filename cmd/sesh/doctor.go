package main

// `sesh doctor` (PARITY_ROADMAP E2): diagnose a sesh install. CLIENT-side
// checks run here (the binary, config.toml parsing, SESH_MACHINE); the
// DAEMON-side checks (agents on the daemon's $SHELL PATH, tmux, peers — the
// deploy-env failure class) come from the daemon's /v1/doctor. Each line is
// ok / warn / fail; a fail sets a non-zero exit (scriptable).

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

func runDoctor(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var checks []api.DoctorCheck
	add := func(name, status, detail string) {
		checks = append(checks, api.DoctorCheck{Name: name, Status: status, Detail: detail})
	}

	// Client-side: identity + config policies load (a broken one is loud here
	// the same way it refuses the daemon).
	if cfg.MachineExplicit {
		add("SESH_MACHINE", "ok", cfg.Machine)
	} else {
		add("SESH_MACHINE", "warn", "unset (client falls back to hostname "+cfg.Machine+"; the daemon REFUSES this)")
	}
	checkConfig(cfg.Home, add)

	// Daemon-side checks.
	c := daemonClient(cfg)
	if resp, err := c.Doctor(context.Background()); err != nil {
		add("daemon", "fail", "not reachable: "+err.Error())
	} else {
		add("daemon", "ok", "reachable")
		checks = append(checks, resp.Checks...)
	}

	failed := false
	for _, ch := range checks {
		if ch.Status == "fail" {
			failed = true
		}
	}
	if *asJSON {
		if err := emitJSON(api.DoctorResponse{Schema: api.SchemaVersion, Checks: checks}); err != nil {
			return err
		}
		if failed {
			return fmt.Errorf("doctor: one or more checks FAILED")
		}
		return nil
	}
	for _, ch := range checks {
		glyph := "✓"
		switch ch.Status {
		case "warn":
			glyph = "!"
		case "fail":
			glyph = "✗"
		}
		fmt.Printf("%s %-18s %s\n", glyph, ch.Name, ch.Detail)
	}
	if failed {
		return fmt.Errorf("doctor: one or more checks FAILED")
	}
	return nil
}

// checkConfig reports whether each [config] policy parses (loud-load parity).
func checkConfig(home string, add func(name, status, detail string)) {
	if _, err := config.LoadNaming(home); err != nil {
		add("config:session_name", "fail", err.Error())
	}
	if _, err := config.LoadCwdLabels(home); err != nil {
		add("config:cwd_label", "fail", err.Error())
	}
	if _, err := config.LoadHooks(home); err != nil {
		add("config:hooks", "fail", err.Error())
	}
	if _, err := config.LoadSpawn(home); err != nil {
		add("config:spawn", "fail", err.Error())
	}
	tcfg, err := config.LoadTUI(home)
	if err != nil {
		add("config:tui", "fail", err.Error())
	} else if tcfg != nil {
		var specs []config.TUIView
		_ = specs
		add("config:tui", "ok", fmt.Sprintf("%d view(s)", len(tcfg.Views)))
	}
	if _, err := os.Stat(config.ConfigPath(home)); err == nil {
		add("config", "ok", config.ConfigPath(home))
	}
}
