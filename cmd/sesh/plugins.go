package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/lukastk/sesh/internal/config"
)

// runPlugins implements `sesh plugins <list|run>` — the daemon command-provider
// substrate. `plugins list` shows the manifests at <SESH_HOME>/plugins/*.toml on the
// (possibly remote) daemon; `plugins run <plugin> <capability> [--field k=v …]` runs a
// list or action capability ON that machine. `--machine` routes either to a peer, so
// the app (and the CLI) can drive any machine's plugins.
func runPlugins(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return printGroupHelp("plugins")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return pluginsList(cfg, rest)
	case "run":
		return pluginsRun(cfg, rest)
	default:
		return fmt.Errorf("unknown plugins subcommand %q", sub)
	}
}

func pluginsList(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resp, err := daemonClient(cfg).PluginsList(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	for _, p := range resp.Plugins {
		fmt.Printf("%s\t%s\n", p.Name, p.Description)
		for _, c := range p.Capabilities {
			fmt.Printf("  %s\t%s\n", c.Kind, c.Name)
		}
	}
	return nil
}

// fieldFlag collects repeated --field k=v pairs.
type fieldFlag map[string]string

func (f fieldFlag) String() string { return "" }
func (f fieldFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("--field must be key=value, got %q", v)
	}
	f[k] = val
	return nil
}

func pluginsRun(cfg config.Config, args []string) error {
	// <plugin> and <capability> are the first two positionals; flags follow them (Go's
	// flag package stops at the first non-flag token, so they must come first).
	if len(args) < 2 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		return errors.New("plugins run <plugin> <capability> [--field k=v …]")
	}
	plugin, capability := args[0], args[1]
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fields := fieldFlag{}
	fs.Var(fields, "field", "an action field value, key=value (repeatable)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	resp, err := daemonClient(cfg).PluginRun(context.Background(), plugin, capability, fields)
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	if resp.Kind == "list" {
		for _, it := range resp.Items {
			fmt.Printf("%s\t%s\t%s\t%s\n", it.ID, it.Label, strings.Join(it.Groups, ","), it.Path)
		}
		return nil
	}
	if out := strings.TrimRight(resp.Output, "\n"); out != "" {
		fmt.Println(out)
	}
	return nil
}
