package main

// Current-thread inference (PARITY_ROADMAP F1, v1's current.go re-built on
// v2's birth-stamps). Single-thread verbs accept an OPTIONAL --id; when empty
// the thread is resolved, in order:
//
//  1. an explicit value — a full uuid, or a UNIQUE id prefix (resolved against
//     the daemon's list, archived included; ambiguous/unknown = loud);
//  2. $SESH_THREAD_ID — injected into every spawned pane and headless turn
//     process; validated against the daemon, a stale value falls through;
//  3. the calling tmux pane's @sesh-thread-id birth-stamp ($TMUX + $TMUX_PANE
//     — v2 needs no process walker; the stamp survives nesting because
//     $TMUX_PANE is inherited by every process in the pane);
//
// else a LOUD error. `delete` deliberately does NOT infer (destructive +
// ambient context is a footgun — always explicit).

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/tmux"
)

// resolveThreadID resolves the thread a verb acts on (see the package comment
// above for the precedence). The returned id is always a full, daemon-known
// uuid.
func resolveThreadID(cfg config.Config, explicit string) (string, error) {
	c := daemonClient(cfg)
	if explicit != "" {
		return resolveIDPrefix(c, explicit)
	}
	if env := os.Getenv(agents.EnvThreadID); env != "" {
		if _, ok := lookupThread(c, env); ok {
			return env, nil
		}
		// Stale (e.g. the thread was deleted, or the env leaked across homes):
		// fall through to the pane, per v1.
	}
	if id, err := paneThreadID(); err == nil && id != "" {
		if _, ok := lookupThread(c, id); ok {
			return id, nil
		}
	}
	return "", fmt.Errorf("not inside a sesh thread: no --id, no valid $%s, and no thread-marked tmux pane — pass --id", agents.EnvThreadID)
}

// resolveIDPrefix resolves a full uuid or unique id prefix against the
// daemon's thread list (archived included). Unknown or ambiguous is loud.
func resolveIDPrefix(c *client.Client, ref string) (string, error) {
	threads, err := listAllThreads(c)
	if err != nil {
		return "", err
	}
	var hits []string
	for _, th := range threads {
		if th.ID == ref {
			return ref, nil
		}
		if strings.HasPrefix(th.ID, ref) {
			hits = append(hits, th.ID)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no thread with id (or id prefix) %q", ref)
	case 1:
		return hits[0], nil
	default:
		return "", fmt.Errorf("id prefix %q is ambiguous (%d threads: %s …)", ref, len(hits), shortJoin(hits, 3))
	}
}

func lookupThread(c *client.Client, id string) (api.Thread, bool) {
	threads, err := listAllThreads(c)
	if err != nil {
		return api.Thread{}, false
	}
	for _, th := range threads {
		if th.ID == id {
			return th, true
		}
	}
	return api.Thread{}, false
}

func listAllThreads(c *client.Client) ([]api.Thread, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := c.ThreadList(ctx, true, false) // archived included: inference must see parked threads too
	if err != nil {
		return nil, err
	}
	return resp.Threads, nil
}

// paneThreadID reads the calling pane's @sesh-thread-id birth-stamp via the
// caller's own $TMUX socket path. ("", nil) when not in tmux or the pane is
// unmarked — a legitimate not-here, the caller falls through to its loud error.
func paneThreadID() (string, error) {
	tmuxEnv, pane := os.Getenv("TMUX"), os.Getenv("TMUX_PANE")
	if tmuxEnv == "" || pane == "" {
		return "", nil
	}
	socketPath := tmuxEnv
	if i := strings.IndexByte(tmuxEnv, ','); i >= 0 {
		socketPath = tmuxEnv[:i]
	}
	return tmux.ThreadIDOfPaneAtPath(socketPath, pane)
}

func shortJoin(ids []string, n int) string {
	out := make([]string, 0, n)
	for i, id := range ids {
		if i == n {
			break
		}
		out = append(out, id[:8])
	}
	return strings.Join(out, ", ")
}

// resolveMeshThreadID is resolveThreadID with MESH-wide prefix resolution: an
// explicit ref may name a thread on any machine (await/watchers work across
// the mesh by id); inference (env/pane) stays local as always.
func resolveMeshThreadID(c *client.Client, cfg config.Config, explicit string) (string, error) {
	if explicit == "" {
		return resolveThreadID(cfg, "")
	}
	// LOCAL list first: it sees just-created threads immediately (the mesh
	// snapshot lags one maintainer publish) and archived ones; the mesh pass
	// then covers other machines.
	if id, err := resolveIDPrefix(c, explicit); err == nil {
		return id, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mesh, err := c.Mesh(ctx)
	if err != nil {
		return "", err
	}
	var hits []string
	for _, mv := range mesh.Machines {
		for _, th := range mv.Threads {
			if th.ID == explicit {
				return explicit, nil
			}
			if strings.HasPrefix(th.ID, explicit) {
				hits = append(hits, th.ID)
			}
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no thread on the mesh with id (or prefix) %q", explicit)
	case 1:
		return hits[0], nil
	default:
		return "", fmt.Errorf("id prefix %q is ambiguous (%d threads: %s …)", explicit, len(hits), shortJoin(hits, 3))
	}
}
