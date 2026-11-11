package main

// `sesh idle-stop` (schema 66, _dev/IDLE_STOP.md): each daemon stops its own
// headful agent threads once they have been idle for longer than their limit —
// [idle_stop] after, or the thread's own override set here.

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

func runIdleStop(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return printGroupHelp("idle-stop")
	}
	switch args[0] {
	case "set":
		return idleStopSet(cfg, args[1:])
	case "preview":
		return idleStopPreview(cfg, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q (want set | preview)", args[0])
	}
}

func idleStopSet(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("idle-stop set", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	never := fs.Bool("never", false, "never stop this thread for being idle")
	after := fs.String("after", "", "stop this thread after this long idle instead of the machine's limit (e.g. 48h)")
	inherit := fs.Bool("inherit", false, "drop the override: use the machine's [idle_stop] after")
	if err := fs.Parse(args); err != nil {
		return err
	}
	n := 0
	value := ""
	if *never {
		n++
		value = api.IdleStopNever
	}
	if *after != "" {
		n++
		value = *after
	}
	if *inherit {
		n++
	}
	if n != 1 {
		return errors.New("idle-stop set: give exactly one of --never, --after <duration>, --inherit")
	}
	if _, _, err := api.ParseIdleStopOverride(value); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	resp, err := daemonClient(cfg).SetIdleStop(context.Background(), rid, value)
	if err != nil {
		return err
	}
	shown := resp.Thread.IdleStop
	if shown == "" {
		shown = "inherit (the machine's [idle_stop] after)"
	}
	fmt.Printf("%s idle-stop: %s\n", rid, shown)
	return nil
}

func idleStopPreview(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("idle-stop preview", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("idle-stop preview: unexpected arguments %v", fs.Args())
	}
	p, err := daemonClient(cfg).IdleStopPreview(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(p)
	}
	after := p.After
	if after == "" {
		after = "none"
	}
	fmt.Printf("%s: machine limit %s; %d live thread(s)\n", p.Machine, after, len(p.Threads))
	for _, c := range p.Threads {
		verdict := "keep"
		if c.Stop {
			verdict = "STOP"
		}
		short := c.ID
		if len(short) > 8 {
			short = short[:8]
		}
		fmt.Printf("%-4s %s %-24s %s\n", verdict, short, c.Name, c.Reason)
	}
	return nil
}
