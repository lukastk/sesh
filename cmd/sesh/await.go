package main

// `sesh await [id|prefix]` (PARITY_ROADMAP C1, v1's await on the v2 mesh):
// block until the thread's turn completes (busy → idle), then print the state.
// It polls the LOCAL daemon's merged mesh view — turn state propagates over
// the mesh, so a thread on ANY machine awaits with no forwarding (v1's
// design). Already-idle returns immediately; an unknown busy value keeps
// waiting (not-yet-idle); a vanished thread is a loud error; --timeout 0 = no
// limit, expiry = loud non-zero (the scripting contract).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

func runAwait(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("await", flag.ContinueOnError)
	id := fs.String("id", "", "thread id/prefix (default: the current thread)")
	timeout := fs.Duration("timeout", 0, "give up after this long (0 = no limit)")
	poll := fs.Duration("poll", 500*time.Millisecond, "poll interval")
	asJSON := fs.Bool("json", false, "emit JSON on completion")
	// Accept the v1 positional form (`sesh await <id> --timeout …`): Go's flag
	// package stops at the first positional, so pop a leading ref first.
	ref0 := ""
	ref0Supplied := false
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		ref0, args = args[0], args[1:]
		ref0Supplied = true
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *timeout < 0 {
		return fmt.Errorf("await: --timeout must be >= 0 (got %s); 0 means no limit", *timeout)
	}
	if *poll <= 0 {
		return errors.New("await: --poll must be > 0")
	}
	if err := guardEmptyIDFlag(fs); err != nil {
		return err
	}
	if err := guardEmptyPositionalRef(ref0Supplied, ref0); err != nil {
		return err
	}
	ref := *id
	if ref == "" {
		ref = ref0
	}
	// Resolution against the MESH (not just local threads): a peer's thread is
	// awaitable by id from anywhere. Prefix resolution needs the same scope.
	c := daemonClient(cfg)
	rid, err := resolveMeshThreadID(c, cfg, ref)
	if err != nil {
		return err
	}

	start := time.Now()
	var deadline time.Time
	if *timeout > 0 {
		deadline = start.Add(*timeout)
	}
	for {
		snap, found, err := meshThread(c, rid)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("await: thread %s vanished while waiting", rid)
		}
		if snap.Busy == api.BusyIdle {
			if *asJSON {
				return emitJSON(map[string]any{
					"schema": api.SchemaVersion, "id": rid,
					"head": snap.Head, "busy": snap.Busy,
					"waited_ms": time.Since(start).Milliseconds(),
				})
			}
			fmt.Printf("idle (%s, waited %s)\n", snap.Head, time.Since(start).Round(time.Millisecond))
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("await: thread %s still %s after %s", rid, snap.Busy, *timeout)
		}
		time.Sleep(*poll)
	}
}

// meshThread reads one thread's snapshot from the local merged mesh view.
func meshThread(c interface {
	Mesh(ctx context.Context) (api.MeshSnapshot, error)
}, id string) (api.ThreadSnapshot, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mesh, err := c.Mesh(ctx)
	if err != nil {
		return api.ThreadSnapshot{}, false, err
	}
	for _, mv := range mesh.Machines {
		for _, th := range mv.Threads {
			if th.ID == id {
				return th, true, nil
			}
		}
	}
	return api.ThreadSnapshot{}, false, nil
}
