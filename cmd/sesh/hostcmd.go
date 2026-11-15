package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// runHostCmd is `sesh host-cmd`: list and run the named commands a machine
// declares in <SESH_HOME>/host-commands/*.toml (routes per --machine).
func runHostCmd(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return printGroupHelp("host-cmd")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return hostCmdList(cfg, rest)
	case "run":
		return hostCmdRun(cfg, rest)
	default:
		return fmt.Errorf("unknown host-cmd subcommand %q", sub)
	}
}

func hostCmdList(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resp, err := daemonClient(cfg).HostCommands(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		return emitJSON(resp)
	}
	if len(resp.Providers) == 0 {
		fmt.Printf("no host commands on %s (none in its <SESH_HOME>/host-commands/)\n", resp.Machine)
		return nil
	}
	for _, p := range resp.Providers {
		for _, c := range p.Commands {
			var names []string
			for _, a := range c.Args {
				n := a.Name
				if !a.Required {
					n = "[" + n + "]"
				}
				names = append(names, n)
			}
			fmt.Printf("%s %s\t%s\t%s\n", p.Name, c.Name, strings.Join(names, " "), c.Description)
		}
	}
	return nil
}

// argFlags collects repeated --arg k=v.
type argFlags map[string]string

func (a argFlags) String() string { return "" }
func (a argFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("--arg %q: want name=value", v)
	}
	if _, dup := a[k]; dup {
		return fmt.Errorf("--arg %s given twice", k)
	}
	a[k] = val
	return nil
}

func hostCmdRun(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	provider := fs.String("provider", "", "the provider (the manifest's file stem)")
	command := fs.String("command", "", "the command name within the provider")
	values := argFlags{}
	fs.Var(values, "arg", "name=value for a declared arg (repeatable)")
	asJSON := fs.Bool("json", false, "emit the finished run as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *provider == "" || *command == "" {
		return errors.New("host-cmd run: --provider and --command are required")
	}
	c := daemonClient(cfg)
	ctx := context.Background()
	run, err := c.HostCommandStart(ctx, api.HostCommandRunRequest{
		Provider: *provider, Command: *command, Args: values, RequestID: uuid.NewString(),
	})
	if err != nil {
		return err
	}
	for run.State != api.HostCommandDone {
		if run, err = c.HostCommandRun(ctx, run.RunID, int((8 * time.Second).Milliseconds())); err != nil {
			return err
		}
	}
	if *asJSON {
		if err := emitJSON(run); err != nil {
			return err
		}
	} else {
		fmt.Fprint(os.Stdout, run.Stdout)
		fmt.Fprint(os.Stderr, run.Stderr)
		if run.StdoutTruncated || run.StderrTruncated {
			fmt.Fprintln(os.Stderr, "host-cmd: output was truncated at 1 MiB per stream")
		}
	}
	switch {
	case run.Error != "":
		return fmt.Errorf("%s/%s on %s: %s", run.Provider, run.Command, run.Machine, run.Error)
	case run.ExitCode != 0:
		return fmt.Errorf("%s/%s on %s exited %d", run.Provider, run.Command, run.Machine, run.ExitCode)
	}
	return nil
}
