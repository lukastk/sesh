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
	case "list":
		return modelList(cfg, rest)
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

// modelList prints the models the target machine's harness reports it can use.
func modelList(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("model list", flag.ContinueOnError)
	agent := fs.String("agent", "", "claude | codex | pi | mypi (required)")
	refresh := fs.Bool("refresh", false, "ask the harness again instead of reusing the daemon's recent answer")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("model list: unexpected argument %q", fs.Arg(0))
	}
	if *agent == "" {
		return errors.New("model list: --agent is required")
	}
	out, err := daemonClient(cfg).ModelList(context.Background(), *agent, *refresh)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	w := 0
	for _, m := range out.Models {
		if len(m.ID) > w {
			w = len(m.ID)
		}
	}
	for _, m := range out.Models {
		detail := strings.TrimSpace(strings.Join(nonEmpty(m.Label, m.Description), " · "))
		if m.Resolved != "" {
			detail = strings.TrimSpace(detail + " (= " + m.Resolved + ")")
		}
		fmt.Printf("%-*s  %s\n", w, m.ID, detail)
	}
	if out.HarnessDefault != "" {
		fmt.Printf("\nwith no --model, %s runs %s\n", out.Agent, out.HarnessDefault)
	}
	return nil
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// threadSetModel is `thread set-model`: change the thread's REGISTERED model
// only (what sesh launches it with from now on). A live agent keeps running its
// current model.
func threadSetModel(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("set-model", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	model := fs.String("model", "", "the model, in the harness's own spelling")
	clearModel := fs.Bool("clear", false, "remove the registered model (the harness's own default from then on)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("thread set-model: unexpected argument %q", fs.Arg(0))
	}
	if (*model == "") == !*clearModel {
		return errors.New("thread set-model: exactly one of --model or --clear is required")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	out, err := daemonClient(cfg).ThreadModel(context.Background(), api.SetThreadModelRequest{ID: rid, Model: *model, Clear: *clearModel})
	if err != nil {
		return err
	}
	return printThreadModel(out, *asJSON)
}

// threadSwitchModel is `thread switch-model`: change the registered model AND
// the live agent's. Destructive for claude (its pane restarts), so like `stop`
// it never infers the thread — --id is required.
func threadSwitchModel(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("switch-model", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (required)")
	model := fs.String("model", "", "the model, in the harness's own spelling (required)")
	force := fs.Bool("force", false, "switch even though the thread is mid-turn (for claude this kills the turn)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("thread switch-model: unexpected argument %q", fs.Arg(0))
	}
	if *id == "" || *model == "" {
		return errors.New("thread switch-model: --id and --model are required")
	}
	c := daemonClient(cfg)
	rid, err := resolveIDPrefix(c, *id)
	if err != nil {
		return err
	}
	out, err := c.ThreadModel(context.Background(), api.SetThreadModelRequest{ID: rid, Model: *model, Apply: true, Force: *force})
	if err != nil {
		return err
	}
	return printThreadModel(out, *asJSON)
}

func printThreadModel(out api.SetThreadModelResponse, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	model := out.Thread.Model
	if model == "" {
		model = "- (harness default)"
	}
	fmt.Printf("%s: model %s [%s] — %s\n", out.Thread.ID, model, out.Outcome, out.Detail)
	return nil
}
