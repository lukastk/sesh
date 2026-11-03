package main

// `sesh context` (schema 60): a thread's custom CONTEXT — text its agent receives
// as standing instructions, attached to the thread rather than to its working
// directory. show/set/clear/edit act on the owner's record (--machine routes);
// `hook` is what the agents' own hooks run (see agents/contexthook.go and the pi
// sesh-agent-state extension) and is not meant to be typed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/config"
)

func runContext(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return printGroupHelp("context")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "show":
		return contextShow(cfg, rest)
	case "set":
		return contextSet(cfg, rest)
	case "clear":
		return contextClear(cfg, rest)
	case "edit":
		return contextEdit(cfg, rest)
	case "hook":
		return contextHook(cfg, rest)
	default:
		return fmt.Errorf("unknown context subcommand %q (want show|set|clear|edit)", sub)
	}
}

func noArgs(fs *flag.FlagSet, verb string) error {
	if fs.NArg() > 0 {
		return fmt.Errorf("context %s: unexpected argument %q", verb, fs.Arg(0))
	}
	return nil
}

func contextShow(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("context show", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, "show"); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	out, err := daemonClient(cfg).ThreadContext(context.Background(), rid)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if out.Text == "" {
		fmt.Fprintf(os.Stderr, "%s has no context\n", rid)
		return nil
	}
	fmt.Print(out.Text)
	if !strings.HasSuffix(out.Text, "\n") {
		fmt.Println()
	}
	return nil
}

func contextSet(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("context set", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	text := fs.String("text", "", "the context text")
	file := fs.String("file", "", "read the context from this file (- = stdin)")
	expect := fs.String("expect-hash", "", "refuse unless the current context hash is this (none = no context)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, "set"); err != nil {
		return err
	}
	if (*text == "") == (*file == "") {
		return errors.New("context set: exactly one of --text or --file is required (use `context clear` to remove)")
	}
	body := *text
	if *file != "" {
		var raw []byte
		var err error
		if *file == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(*file)
		}
		if err != nil {
			return fmt.Errorf("context set: read %s: %w", *file, err)
		}
		body = string(raw)
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	out, err := daemonClient(cfg).SetThreadContext(context.Background(), api.SetThreadContextRequest{ID: rid, Text: body, ExpectHash: *expect})
	if err != nil {
		return err
	}
	return printContextSet(out, *asJSON)
}

func contextClear(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("context clear", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	expect := fs.String("expect-hash", "", "refuse unless the current context hash is this")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, "clear"); err != nil {
		return err
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	out, err := daemonClient(cfg).SetThreadContext(context.Background(), api.SetThreadContextRequest{ID: rid, Clear: true, ExpectHash: *expect})
	if err != nil {
		return err
	}
	return printContextSet(out, *asJSON)
}

func printContextSet(out api.SetThreadContextResponse, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	fmt.Printf("%s: context %s\n", out.Thread.ID, out.Detail)
	return nil
}

// contextEdit opens the context in $VISUAL/$EDITOR and saves it when the editor
// exits. Saving an empty file removes the context. The save is conditional on
// the version that was opened, so a context changed meanwhile is never silently
// overwritten (the edited text is kept in a file the error names).
func contextEdit(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("context edit", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, "edit"); err != nil {
		return err
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		return errors.New("context edit: set $VISUAL or $EDITOR (or use `context set --file`)")
	}
	rid, err := resolveIDFlag(cfg, fs, id)
	if err != nil {
		return err
	}
	c := daemonClient(cfg)
	cur, err := c.ThreadContext(context.Background(), rid)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "sesh-context-*.md")
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.WriteString(cur.Text)
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
		return fmt.Errorf("context edit: editor: %w (your text is in %s)", err, name)
	}
	out, saved, err := saveEditedContext(c, rid, cur, name)
	if err != nil {
		return err
	}
	os.Remove(name)
	if !saved {
		fmt.Printf("%s: context unchanged\n", rid)
		return nil
	}
	return printContextSet(out, false)
}

// saveEditedContext saves the edited file as rid's context, conditional on the
// version `was`. saved=false = nothing changed. On failure the file is kept and
// the error names it.
func saveEditedContext(c *client.Client, rid string, was api.ThreadContextResponse, file string) (api.SetThreadContextResponse, bool, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return api.SetThreadContextResponse{}, false, fmt.Errorf("context edit: read %s: %w", file, err)
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == strings.TrimRight(was.Text, "\n") {
		return api.SetThreadContextResponse{}, false, nil
	}
	expect := was.Hash
	if expect == "" {
		expect = "none"
	}
	req := api.SetThreadContextRequest{ID: rid, Text: text, ExpectHash: expect}
	if strings.TrimSpace(text) == "" {
		req = api.SetThreadContextRequest{ID: rid, Clear: true, ExpectHash: expect}
	}
	out, err := c.SetThreadContext(context.Background(), req)
	if err != nil {
		return out, false, fmt.Errorf("context edit: %w (your text is in %s)", err, file)
	}
	return out, true, nil
}

// contextHook is what an agent's hook runs (agents/contexthook.sh for claude and
// codex; the pi extension calls it directly with --session). It prints the
// harness's hook output and exits 0 whenever there is nothing to inject —
// including when the process is not verifiably a sesh thread's own agent, which
// is the legitimate state of every hook outside sesh. A real failure (a sesh
// thread whose daemon cannot answer) exits 1 so the harness shows a hook error.
func contextHook(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("context hook", flag.ContinueOnError)
	agent := fs.String("agent", "", "the harness running this hook: claude, codex or pi")
	session := fs.String("session", "", "pi: the agent session id (claude/codex read it from stdin)")
	contextFile := fs.Bool("context-file", false, "claude: this process was launched with its thread-context file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	kind, err := agents.ParseKind(*agent)
	if err != nil {
		return fmt.Errorf("context hook: %w", err)
	}
	family := agents.Family(kind)
	req := api.ThreadContextDeliverRequest{Agent: string(family), ContextFile: *contextFile}
	if family == agents.Pi {
		if *session == "" {
			return errors.New("context hook: --session is required for pi")
		}
		req.SessionID, req.Event = *session, api.ContextEventTurn
	} else {
		var in struct {
			SessionID string `json:"session_id"`
			Event     string `json:"hook_event_name"`
			Source    string `json:"source"`
			AgentID   string `json:"agent_id"`
		}
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("context hook: read hook input: %w", err)
		}
		if err := json.Unmarshal(bytes.TrimSpace(raw), &in); err != nil {
			return fmt.Errorf("context hook: hook input is not JSON: %w", err)
		}
		// A claude SUBAGENT's hooks carry agent_id; only the main conversation is
		// the thread. A claude BACKGROUND session inherited its pane env from
		// whatever started claude's daemon (see sesh-agent-state.sh) — not this thread.
		if in.AgentID != "" || os.Getenv("CLAUDE_CODE_SESSION_KIND") == "bg" {
			return nil
		}
		req.SessionID, req.Event, req.Source = in.SessionID, in.Event, in.Source
	}
	id := hookIdentity(cfg)
	if id == "" {
		return emitHookOutput(family, req.Event, api.ThreadContextDeliverResponse{})
	}
	req.ID = id
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The agent runs on its thread's owner, so the LOCAL daemon is the owner.
	out, err := client.New(cfg.SocketPath()).DeliverThreadContext(ctx, req)
	if errors.Is(err, client.ErrRouteMissing) {
		// A daemon older than thread context (schema 60) cannot hold one: nothing
		// to deliver, and no reason to show the agent a hook error mid-deploy.
		return emitHookOutput(family, req.Event, api.ThreadContextDeliverResponse{})
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "sesh: thread context for %s: %v\n", id, err)
		os.Exit(1)
	}
	return emitHookOutput(family, req.Event, out)
}

// hookIdentity is the thread a hook's agent VERIFIABLY belongs to, or "": the
// tmux marker of the pane the agent runs in (read live), else the headless turn
// the local daemon launched it for (confirmed by process ancestry). These are
// two of current-thread inference's verified sources (current.go), and the only
// two that can hold here — an agent's hooks run inside its pane or its turn — so
// the hook asks them directly rather than through resolveCurrentThread, which
// also lists every thread record to vet the answer (~1.5 MB on a large rig, on
// every prompt). $SESH_THREAD_ID alone never counts: it is inherited by anything
// started inside the thread. The daemon still checks the session (a nested
// agent run in the same pane is not the thread) and that the thread exists.
func hookIdentity(cfg config.Config) string {
	if id, err := paneThreadID(); err == nil && id != "" {
		return id
	}
	if id, err := localTurnIdentity(cfg)(); err == nil && id != "" {
		return id
	}
	return ""
}

// emitHookOutput prints the delivery in the harness's hook format.
func emitHookOutput(family agents.Kind, event string, out api.ThreadContextDeliverResponse) error {
	if family == agents.Pi {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"system": out.System, "message": out.Inject})
	}
	if out.Inject == "" {
		return nil
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": out.Inject},
	})
}
