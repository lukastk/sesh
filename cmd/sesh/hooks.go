package main

// `sesh hooks <list|enable|disable|test>` — manage the [[hooks]] event hooks
// (definitions live in config.toml; enable/disable is persisted runtime state;
// test runs a hook synchronously so the whole chain is verifiable end to end).

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/lukastk/sesh/internal/config"
)

func runHooks(args []string) error {
	if len(args) == 0 {
		return printGroupHelp("hooks")
	}
	cfg := config.Load()
	c := daemonClient(cfg)
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "emit JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		resp, err := c.HooksList(context.Background())
		if err != nil {
			return err
		}
		if *asJSON {
			return emitJSON(resp)
		}
		if len(resp.Hooks) == 0 {
			fmt.Println("(no hooks configured — add [[hooks]] to " + config.ConfigPath(cfg.Home) + ")")
			return nil
		}
		for _, h := range resp.Hooks {
			state := "enabled"
			if h.Muted {
				state = "DISABLED"
			}
			edge := ""
			if h.From != "" || h.To != "" {
				edge = " " + h.From + "→" + h.To
			}
			fmt.Printf("%-20s %-18s%s  [%s]  %s\n", h.Name, h.Event, edge, state, h.Command)
		}
		return nil
	case "enable", "disable":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		name := fs.String("name", "", "hook name (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("hooks " + args[0] + ": --name is required")
		}
		if err := c.HooksMute(context.Background(), *name, args[0] == "disable"); err != nil {
			return err
		}
		fmt.Printf("%s %sd\n", *name, args[0])
		return nil
	case "test":
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		name := fs.String("name", "", "hook name (required)")
		thread := fs.String("thread", "", "use this thread's real snapshot for the event (default: synthetic)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("hooks test: --name is required")
		}
		// An explicit empty --thread must not silently mean "synthetic" (the OMITTED
		// default) — reject it like the empty --id footgun.
		if err := guardEmptyFlag(fs, "thread"); err != nil {
			return err
		}
		tid := ""
		if *thread != "" {
			rid, err := resolveThreadIDFor(cfg, *thread, "--thread")
			if err != nil {
				return err
			}
			tid = rid
		}
		resp, err := c.HooksTest(context.Background(), *name, tid)
		if err != nil {
			return err
		}
		if resp.Output != "" {
			fmt.Print(resp.Output)
		}
		if !resp.OK {
			return fmt.Errorf("hook %s failed: %s", resp.Name, resp.Error)
		}
		fmt.Printf("hook %s ran ok\n", resp.Name)
		return nil
	default:
		return fmt.Errorf("unknown hooks subcommand %q", args[0])
	}
}
