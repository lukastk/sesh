package main

// `sesh env` (schema 62): a thread's environment variables. show/set/unset/clear/
// edit act on the owner's env files (--machine routes), and report how the change
// reaches the running agent. `hook` is what claude's hooks run (see
// agents/threadenv.go) and is not meant to be typed.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"strings"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// maskedEnvValue stands for a value in output that was not asked to reveal it, so
// a listing is safe in a transcript, a shared terminal or a log.
const maskedEnvValue = "••••••"

func runEnv(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return printGroupHelp("env")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "show":
		return envShow(cfg, rest)
	case "set":
		return envSet(cfg, rest)
	case "unset":
		return envUnset(cfg, rest)
	case "clear":
		return envClear(cfg, rest)
	case "edit":
		return envEdit(cfg, rest)
	case "hook":
		return envHook()
	default:
		return fmt.Errorf("unknown env subcommand %q (want show|set|unset|clear|edit)", sub)
	}
}

func envShow(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("env show", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	reveal := fs.Bool("reveal", false, "print the values")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("env show: unexpected argument %q", fs.Arg(0))
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	out, err := daemonClient(cfg).ThreadEnv(context.Background(), rid)
	if err != nil {
		return err
	}
	if !*reveal {
		for k := range out.Vars {
			out.Vars[k] = maskedEnvValue
		}
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if len(out.Vars) == 0 {
		fmt.Fprintf(os.Stderr, "%s has no environment variables\n", rid)
		return nil
	}
	fmt.Print(agents.FormatEnvLines(out.Vars))
	return nil
}

func envSet(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	file := fs.String("file", "", "read KEY=VALUE lines from this file; - reads stdin")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	vars, err := envFromFlags(fs.Args(), *file)
	if err != nil {
		return err
	}
	if len(vars) == 0 {
		return errors.New("env set: nothing to set (pass KEY=VALUE, or --file)")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	out, err := daemonClient(cfg).SetThreadEnv(context.Background(), api.SetThreadEnvRequest{ID: rid, Set: vars})
	if err != nil {
		return err
	}
	return printEnvSet(out, *asJSON)
}

func envUnset(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("env unset", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("env unset: name at least one variable")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	out, err := daemonClient(cfg).SetThreadEnv(context.Background(), api.SetThreadEnvRequest{ID: rid, Unset: fs.Args()})
	if err != nil {
		return err
	}
	return printEnvSet(out, *asJSON)
}

func envClear(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("env clear", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("env clear: unexpected argument %q", fs.Arg(0))
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	out, err := daemonClient(cfg).SetThreadEnv(context.Background(), api.SetThreadEnvRequest{ID: rid, Replace: true})
	if err != nil {
		return err
	}
	return printEnvSet(out, *asJSON)
}

// envEdit opens the variables as KEY=VALUE lines in the editor and saves them when
// it exits. The save replaces the set, conditional on the version that was opened.
// The temp file holds the values, so it is removed whatever happens, except when a
// save fails, when it holds the user's edit and the error names it.
func envEdit(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("env edit", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("env edit: unexpected argument %q", fs.Arg(0))
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		return errors.New("env edit: set $VISUAL or $EDITOR (or use `env set`)")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	cur, err := c.ThreadEnv(context.Background(), rid)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "sesh-env-*.env") // 0600
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.WriteString(agents.FormatEnvLines(cur.Vars))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(name)
		return werr
	}
	parts := strings.Fields(editor)
	cmd := exec.Command(parts[0], append(parts[1:], name)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("env edit: editor: %w (your edit is in %s)", err, name)
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	next, err := agents.ParseEnvLines(string(raw))
	if err != nil {
		return fmt.Errorf("%w (your edit is in %s)", err, name)
	}
	if maps.Equal(next, cur.Vars) {
		os.Remove(name)
		fmt.Printf("%s: environment unchanged\n", rid)
		return nil
	}
	expect := cur.Hash
	if expect == "" {
		expect = "none"
	}
	out, err := c.SetThreadEnv(context.Background(), api.SetThreadEnvRequest{ID: rid, Set: next, Replace: true, ExpectHash: expect})
	if err != nil {
		return fmt.Errorf("%w (your edit is in %s)", err, name)
	}
	os.Remove(name)
	return printEnvSet(out, false)
}

func printEnvSet(out api.SetThreadEnvResponse, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if !out.Changed {
		fmt.Printf("%s: environment unchanged\n", out.Thread.ID)
		return nil
	}
	fmt.Printf("%s: %d variable(s) [%s] — %s\n", out.Thread.ID, out.Thread.EnvCount, out.Outcome, out.Detail)
	return nil
}

// envFromFlags collects variables from a KEY=VALUE file ("" = none, - = stdin) and
// then KEY=VALUE arguments, which win. The value after the first = is literal.
func envFromFlags(pairs []string, file string) (map[string]string, error) {
	vars := map[string]string{}
	if file != "" {
		var raw []byte
		var err error
		if file == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(file)
		}
		if err != nil {
			return nil, fmt.Errorf("env: %w", err)
		}
		if vars, err = agents.ParseEnvLines(string(raw)); err != nil {
			return nil, err
		}
	}
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("env: expected KEY=VALUE, got %q (flags go before the variables)", pair)
		}
		vars[k] = v
	}
	return vars, nil
}

// envHook is claude's SessionStart and FileChanged hook. It runs in the agent's
// environment: $SESH_THREAD_ENV_FILE is set only in a pane sesh launched, and
// $CLAUDE_ENV_FILE is the file claude offers these two events. Writing it is what
// refreshes claude's cached shell environment; see agents/threadenv.go.
func envHook() error {
	script := os.Getenv(agents.EnvThreadEnvFile)
	if script == "" {
		return nil
	}
	var in struct {
		Event string `json:"hook_event_name"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("env hook: read the hook input: %w", err)
	}
	if file := os.Getenv("CLAUDE_ENV_FILE"); file != "" {
		if err := os.WriteFile(file, []byte(agents.EnvHookLine), 0o600); err != nil {
			return fmt.Errorf("env hook: %w", err)
		}
	}
	if in.Event != "SessionStart" {
		return nil
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{"hookEventName": in.Event, "watchPaths": []string{script}},
	})
}

// threadRestart is `thread restart`: destructive to whatever the agent is doing, so
// like `switch-model` it never infers the thread.
func threadRestart(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (required)")
	force := fs.Bool("force", false, "restart even though the thread is mid-turn (kills the turn)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("thread restart: unexpected argument %q", fs.Arg(0))
	}
	if *id == "" {
		return errors.New("thread restart: --id is required")
	}
	c := daemonClient(cfg)
	rid, err := resolveIDPrefix(c, *id)
	if err != nil {
		return err
	}
	out, err := c.RestartThread(context.Background(), api.RestartThreadRequest{ID: rid, Force: *force})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	fmt.Printf("%s: %s\n", rid, out.Detail)
	return nil
}
