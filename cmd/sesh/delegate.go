package main

// `sesh delegate --agent <a> <task>` (PARITY_ROADMAP C2, v1's delegate as a
// composition of v2's green primitives): spawn a headless worker, give it the
// task, await the reply, print it, then ARCHIVE the worker. Ephemeral by
// contract — the thread disappears from the default view once it answers, but
// the record + transcript are retained (archived, so resumable / auditable);
// --keep leaves it un-archived (active). Either way the id is printed to stderr
// for follow-ups under --keep. Cross-machine: `--machine X` routes the WHOLE
// verb (the worker spawns, runs and is archived on X). Failure paths archive
// the worker (never leave it active) unless --keep.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

func runDelegate(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("delegate", flag.ContinueOnError)
	agent := fs.String("agent", "", "agent kind: pi|claude|codex (required)")
	cwd := fs.String("cwd", "", "working directory (default: $PWD)")
	name := fs.String("name", "", "thread name (mostly for --keep; default delegate-<ts>)")
	keep := fs.Bool("keep", false, "leave the worker un-archived (active) instead of archiving it after the reply")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up after this long (the worker is still archived unless --keep)")
	sandbox := fs.Bool("sandbox", false, "restrict the worker (codex read-only; claude default-deny; pi: refused)")
	yolo := fs.Bool("yolo", false, "bypass the worker's permissions (overrides [spawn] config)")
	asJSON := fs.Bool("json", false, "emit {reply, id} as JSON")
	// The task may be positional-first (v1's form): pop it before Parse.
	task := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		task, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if task == "" && fs.NArg() == 1 {
		task = fs.Arg(0)
	}
	if *agent == "" || task == "" {
		return errors.New("delegate: --agent and a task are required (sesh delegate --agent pi \"question\")")
	}
	if *sandbox && *yolo {
		return errors.New("delegate: --sandbox and --yolo are mutually exclusive")
	}
	mode := ""
	if *sandbox {
		mode = "sandbox"
	}
	if *yolo {
		mode = "yolo"
	}
	if *cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		*cwd = wd
	}
	// A relative --cwd expands against the invocation dir (the daemon needs absolute).
	if abs, err := absCwd(*cwd); err != nil {
		return fmt.Errorf("delegate: --cwd: %w", err)
	} else {
		*cwd = abs
	}
	if *name == "" {
		*name = fmt.Sprintf("delegate-%d", time.Now().Unix())
	}

	c := daemonClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	resp, err := c.ThreadNew(ctx, api.NewThreadRequest{Agent: *agent, Name: *name, Cwd: *cwd, Headless: true})
	if err != nil {
		return fmt.Errorf("delegate: spawn worker: %w", err)
	}
	id := resp.Thread.ID
	// The ephemeral contract: a worker is archived (not active) after it answers
	// or fails — its record + transcript are retained, just hidden from the
	// default view. --keep leaves it active.
	cleanup := func() {
		if *keep {
			return
		}
		dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dcancel()
		if derr := c.ThreadArchive(dctx, id, true); derr != nil {
			fmt.Fprintf(os.Stderr, "delegate: WARNING: worker %s not archived: %v\n", id, derr)
		}
	}

	if err := c.ThreadSendHeadlessMode(ctx, id, task, mode); err != nil {
		cleanup()
		return fmt.Errorf("delegate: start turn: %w", err)
	}
	for {
		reply, rerr := c.ThreadHeadlessReply(ctx, id)
		if rerr != nil {
			cleanup()
			return fmt.Errorf("delegate: poll reply: %w", rerr)
		}
		if !reply.Working && reply.HaveReply {
			// Turn errors surface as an "ERROR: …" reply (the daemon's headless
			// registry convention) — loud, with the worker cleaned up.
			if strings.HasPrefix(reply.Reply, "ERROR: ") {
				cleanup()
				return fmt.Errorf("delegate: worker turn failed: %s", strings.TrimPrefix(reply.Reply, "ERROR: "))
			}
			if *asJSON {
				out := map[string]any{"schema": api.SchemaVersion, "reply": reply.Reply, "id": id, "kept": *keep}
				cleanup()
				return emitJSON(out)
			}
			fmt.Println(reply.Reply)
			if *keep {
				fmt.Fprintf(os.Stderr, "worker kept: %s\n", id)
			}
			cleanup()
			return nil
		}
		select {
		case <-ctx.Done():
			cleanup()
			return fmt.Errorf("delegate: no reply within %s (worker %s archived unless --keep)", *timeout, id)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
