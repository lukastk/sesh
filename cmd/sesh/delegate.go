package main

// `sesh delegate --agent <a> <task>` (PARITY_ROADMAP C2, v1's delegate as a
// composition of v2's green primitives): spawn a headless worker, give it the
// task, await the reply, print it, DELETE the worker. Ephemeral by contract —
// the thread disappears once it answers (--keep retains it; then `sesh new
// --headless` semantics apply and the id is printed to stderr for follow-ups).
// Cross-machine: `--machine X` routes the WHOLE verb (the worker spawns, runs
// and dies on X). Failure paths never leak a worker unless --keep.

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
	keep := fs.Bool("keep", false, "retain the worker thread instead of deleting it after the reply")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up after this long (the worker is still deleted unless --keep)")
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
	// The ephemeral contract: no worker outlives a failure unless --keep.
	cleanup := func() {
		if *keep {
			return
		}
		dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dcancel()
		if derr := c.ThreadDelete(dctx, id, false); derr != nil {
			fmt.Fprintf(os.Stderr, "delegate: WARNING: worker %s not deleted: %v\n", id, derr)
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
			return fmt.Errorf("delegate: no reply within %s (worker %s deleted unless --keep)", *timeout, id)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
