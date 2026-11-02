package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// runModel is `sesh model`: the per-harness DEFAULT models ([spawn.<agent>]
// model in the target daemon's config.toml). Routes per --machine like every
// daemon call, so `--machine X` reads/writes X's config.
func runModel(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return printGroupHelp("model")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "defaults":
		return modelDefaults(cfg, rest)
	case "set-default":
		return modelSetDefault(cfg, rest)
	case "unset-default":
		return modelUnsetDefault(cfg, rest)
	default:
		return fmt.Errorf("unknown model subcommand %q", sub)
	}
}

func modelDefaults(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("model defaults", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("model defaults: unexpected argument %q", fs.Arg(0))
	}
	out, err := daemonClient(cfg).ModelDefaults(context.Background())
	if err != nil {
		return err
	}
	return printModelDefaults(out, *asJSON)
}

func modelSetDefault(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("model set-default", flag.ContinueOnError)
	agent := fs.String("agent", "", "claude | codex | pi | mypi (required)")
	model := fs.String("model", "", "the model, in the harness's own spelling (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("model set-default: unexpected argument %q", fs.Arg(0))
	}
	if *agent == "" || *model == "" {
		return errors.New("model set-default: --agent and --model are required (use `model unset-default` to remove a default)")
	}
	out, err := daemonClient(cfg).SetModelDefault(context.Background(), *agent, *model)
	if err != nil {
		return err
	}
	if *asJSON {
		return printModelDefaults(out, true)
	}
	fmt.Printf("%s default model = %s (%s)\n", *agent, *model, out.WrotePath)
	return nil
}

func modelUnsetDefault(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("model unset-default", flag.ContinueOnError)
	agent := fs.String("agent", "", "claude | codex | pi | mypi (required)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("model unset-default: unexpected argument %q", fs.Arg(0))
	}
	if *agent == "" {
		return errors.New("model unset-default: --agent is required")
	}
	out, err := daemonClient(cfg).SetModelDefault(context.Background(), *agent, "")
	if err != nil {
		return err
	}
	if *asJSON {
		return printModelDefaults(out, true)
	}
	fmt.Printf("removed the %s default model (%s)\n", *agent, out.WrotePath)
	return nil
}

func printModelDefaults(out api.ModelDefaultsResponse, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	for _, a := range config.SpawnModelAgents {
		v := out.Defaults[a]
		if v == "" {
			v = "-  (the harness's own default)"
		}
		fmt.Printf("%-7s %s\n", a, v)
	}
	fmt.Printf("\n%s: %s\n", out.Machine, strings.TrimSpace(out.ConfigPath))
	return nil
}
