package main

// Current-thread inference (PARITY_ROADMAP F1, v1's current.go re-built on
// v2's birth-stamps). Single-thread verbs accept an OPTIONAL --id; when empty
// the thread is resolved, in order:
//
//  1. an explicit value — a full uuid, or a UNIQUE id prefix (resolved against
//     the daemon's list, archived included; ambiguous/unknown = loud);
//  2. the calling tmux pane's @sesh-thread-id marker ($TMUX + $TMUX_PANE — v2
//     needs no process walker; the stamp survives nesting because $TMUX_PANE is
//     inherited by every process in the pane). The marker is the GROUND TRUTH
//     for "which thread owns this pane right now": it is re-stamped on
//     adopt/reparent, so it tracks the live ownership of the pane;
//  3. $SESH_THREAD_ID — injected into every spawned pane and headless turn
//     process. This is FROZEN into the process env at launch and can drift
//     stale (an adopted/reparented agent still carries its old, possibly still
//     -valid, id), so it ranks BELOW the live pane marker. It is the source of
//     truth only when there is no pane (headless turns inject the env and have
//     no tmux pane);
//
// When the env and a valid pane marker DISAGREE, the pane wins and a one-line
// drift note is emitted to stderr (loud, not silent). If nothing resolves it is
// a LOUD error. `delete` deliberately does NOT infer (destructive + ambient
// context is a footgun — always explicit).
//
// Inherent limitation: a detached background job (no $TMUX_PANE) cannot read a
// pane marker, so it can only ever self-resolve via the env and cannot detect
// drift — unavoidable; this targets in-pane agents (the common case).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/tmux"
)

type threadListClient interface {
	ThreadList(context.Context, bool, bool) (api.ThreadListResponse, error)
}

type meshThreadClient interface {
	threadListClient
	Mesh(context.Context) (api.MeshSnapshot, error)
}

// The empty-selector footgun: resolveThreadID treats an empty selector as "infer
// the current thread" (the F1 convenience). An OMITTED --id is fine — that IS the
// convenience — but an EXPLICITLY-passed empty value (`sesh thread archive --id
// "$X"` where $X is unset, or `sesh info ""`) is indistinguishable from omission,
// so a verb silently acts on the WRONG (current) thread. That is exactly how an
// archive once hid the running session. The guards below make an explicit empty
// selector a LOUD error (mirroring "loud errors over silent failures"); inference
// on a truly-omitted selector is untouched. The DESTRUCTIVE verbs (stop, delete)
// go further still — they never infer at all (an omitted --id is also an error).

// guardEmptyIDFlag rejects an --id that was passed on the command line but is empty
// (or whitespace). Call it right after fs.Parse in any command that resolves the
// acted-on thread from --id. Omitting --id entirely is not flagged (inference).
func guardEmptyIDFlag(fs *flag.FlagSet) error { return guardEmptyFlag(fs, "id") }

// guardEmptyFlag is guardEmptyIDFlag for an arbitrarily-named selector flag (e.g.
// `--thread`). It uses fs.Visit, which reports only flags actually present on the
// command line, so an omitted flag is never rejected.
func guardEmptyFlag(fs *flag.FlagSet, name string) error {
	passedEmpty := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name && strings.TrimSpace(f.Value.String()) == "" {
			passedEmpty = true
		}
	})
	if passedEmpty {
		return fmt.Errorf("--%s was passed but is empty — give a thread id, or omit --%s to use the current thread", name, name)
	}
	return nil
}

// guardEmptyPositionalRef rejects a single positional thread id that was supplied
// but empty (`sesh info ""`), the positional twin of guardEmptyIDFlag. `supplied`
// is whether a positional id argument was actually given; an omitted positional is
// not flagged (inference).
func guardEmptyPositionalRef(supplied bool, ref string) error {
	if supplied && strings.TrimSpace(ref) == "" {
		return errors.New("the thread id argument was given but is empty — give a thread id, or omit it to use the current thread")
	}
	return nil
}

// resolveIDFlag resolves the acted-on thread from the command's --id flag, after
// rejecting an explicit empty value (guardEmptyIDFlag). It replaces the bare
// `resolveThreadID(cfg, *id)` at every command that selects a single thread purely
// by --id, folding in the footgun guard so no call site can forget it. `id` is the
// command's bound --id flag pointer (its flagset is `fs`).
func resolveIDFlag(cfg config.Config, fs *flag.FlagSet, id *string) (string, error) {
	if err := guardEmptyIDFlag(fs); err != nil {
		return "", err
	}
	return resolveThreadID(cfg, *id)
}

// resolveIDOrPositional is resolveIDFlag for verbs that ALSO accept the thread id
// as a single positional argument (`sesh resume <id>`): it rejects an explicit
// empty --id OR an explicit empty positional, prefers --id, falls back to the
// positional, and infers the current thread only when BOTH were omitted.
func resolveIDOrPositional(cfg config.Config, fs *flag.FlagSet) (string, error) {
	if err := guardEmptyIDFlag(fs); err != nil {
		return "", err
	}
	if err := guardEmptyPositionalRef(fs.NArg() == 1, fs.Arg(0)); err != nil {
		return "", err
	}
	ref := ""
	if f := fs.Lookup("id"); f != nil {
		ref = f.Value.String()
	}
	if ref == "" && fs.NArg() == 1 {
		ref = fs.Arg(0)
	}
	return resolveThreadID(cfg, ref)
}

// resolveThreadID resolves the thread a verb acts on (see the package comment
// above for the precedence). The returned id is always a full, daemon-known
// uuid.
func resolveThreadID(cfg config.Config, explicit string) (string, error) {
	c := daemonClient(cfg)
	if explicit != "" {
		return resolveIDPrefix(c, explicit)
	}
	env := os.Getenv(agents.EnvThreadID)
	// The live pane marker is ground truth and outranks the env: it is
	// re-stamped on adopt/reparent, whereas the env is frozen at launch and may
	// carry a stale-but-still-valid id (the SESH_THREAD_ID drift bug).
	if id, err := paneThreadID(); err == nil && id != "" {
		if _, ok := lookupThread(c, id); ok {
			if env != "" && env != id {
				fmt.Fprintf(os.Stderr, "sesh: $%s=%s is stale; using live pane marker %s (pane adopted/reparented since launch)\n", agents.EnvThreadID, short8(env), short8(id))
			}
			return id, nil
		}
	}
	// No pane (e.g. a headless turn — env injected, no tmux pane), or the pane
	// carries no valid marker: the env is the source of truth. A stale env
	// (deleted thread, leaked across homes) falls through to the loud error.
	if env != "" {
		if _, ok := lookupThread(c, env); ok {
			return env, nil
		}
	}
	return "", fmt.Errorf("not inside a sesh thread: no --id, no valid $%s, and no thread-marked tmux pane — pass --id", agents.EnvThreadID)
}

// resolveIDPrefix resolves a full uuid or unique id prefix against the
// daemon's thread list (archived included). Unknown or ambiguous is loud.
//
// A FULL well-formed uuid needs no prefix expansion, so it skips the list
// fetch entirely. That fetch is the expensive part of a ROUTED verb (`--machine
// X` points this process at the peer's API, so resolving pulls the peer's whole
// thread list — archived included — before the actual verb; the TUI always
// passes full row IDs, so every routed TUI action paid it). Nothing is lost:
// an unknown full uuid still fails LOUDLY, just at the verb itself (the
// daemon's 404) instead of here.
func resolveIDPrefix(c threadListClient, ref string) (string, error) {
	if isFullUUID(ref) {
		return ref, nil
	}
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

// isFullUUID reports a canonical full uuid: 36 chars, dashes at 8/13/18/23,
// lowercase hex elsewhere — the exact form sesh mints ids in. Deliberately
// conservative (uppercase or nonstandard forms fall through to prefix
// resolution, which behaves exactly as before).
func isFullUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func lookupThread(c threadListClient, id string) (api.Thread, bool) {
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

func listAllThreads(c threadListClient) ([]api.Thread, error) {
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

// short8 is the leading 8 chars of an id (the conventional display prefix),
// safe for shorter strings.
func short8(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
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
func resolveMeshThreadID(c meshThreadClient, cfg config.Config, explicit string) (string, error) {
	if explicit == "" {
		return resolveThreadID(cfg, "")
	}
	// LOCAL list first: it sees just-created threads immediately (the mesh
	// snapshot lags one maintainer publish) and archived ones; the mesh pass
	// then covers other machines. resolveIDPrefix's full-UUID fast path skips
	// existence checks for ordinary verbs (their owner returns the eventual
	// 404), but a mesh-read verb has no later owner request: it must actually
	// observe an exact id here, or a not-yet-replicated remote thread would be
	// misreported as having "vanished" on the first await poll.
	if isFullUUID(explicit) {
		if _, ok := lookupThread(c, explicit); ok {
			return explicit, nil
		}
	} else if id, err := resolveIDPrefix(c, explicit); err == nil {
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
