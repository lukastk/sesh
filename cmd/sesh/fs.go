package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/lukastk/sesh/internal/config"
)

// runFs implements `sesh fs <list>` — a generic, policy-free filesystem primitive the
// daemon serves over its API. `fs list --path ~/dev` returns the immediate
// SUBDIRECTORIES of an allow-listed (home-rooted) path on the daemon's host;
// `--machine` routes it to a peer. The Obsidian new-thread modal uses this to fill the
// box (~/dev) and mysetup (~/mysetup) cwd pickers on mobile, where the plugin has no
// local filesystem access.
func runFs(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return printGroupHelp("fs")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return fsList(cfg, rest)
	default:
		return fmt.Errorf("unknown fs subcommand %q", sub)
	}
}

func fsList(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	path := fs.String("path", "", "the home-rooted path to list (e.g. ~/dev or ~/mysetup)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("fs list: --path is required")
	}
	resp, err := daemonClient(cfg).FsList(context.Background(), *path)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	for _, e := range resp.Entries {
		fmt.Printf("%s\t%s\n", e.Name, e.Path)
	}
	return nil
}
