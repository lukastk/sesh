package main

// Finding a thread that lives on another machine (lukastk/sesh: cross-machine id
// lookup).
//
// A thread is owned by the machine that runs it, and a verb that takes --id acts on
// the owner. Before this, `thread info|transcript|send … --id X` ran against the
// LOCAL daemon, so a thread on another machine answered "vanished mid-lookup" or 404
// unless the caller already knew to add --machine. Every daemon caches every peer's
// thread list (GET /v1/mesh, read locally, no network), so the id can be resolved
// from that cache without asking the peer first.
//
// Two rules keep this safe:
//   - It never overrides a thread this machine owns: a local match always wins.
//   - It acts only when the id names exactly one thread on exactly one OTHER machine.
//     Zero matches fall through to the usual local error. Several matches are
//     refused loudly, naming each machine, because routing to a guess could send a
//     message to the wrong conversation.
//
// It applies to `sesh thread …` verbs that take --id. A positional id (`thread
// resume <id>`) is not routed automatically: pass --machine for those.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/config"
)

// meshThreadMatch is one thread the mesh cache holds for a reference.
type meshThreadMatch struct {
	Machine string
	ID      string
	Name    string
	Self    bool
}

// matchThreadsAcrossMesh returns every thread, on any machine the local daemon
// knows, whose id is ref or starts with ref.
func matchThreadsAcrossMesh(cfg config.Config, ref string) ([]meshThreadMatch, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mesh, err := daemonClient(cfg).Mesh(ctx)
	if err != nil {
		return nil, err
	}
	var out []meshThreadMatch
	for _, mv := range mesh.Machines {
		for _, th := range mv.Threads {
			if th.ID == ref || strings.HasPrefix(th.ID, ref) {
				out = append(out, meshThreadMatch{Machine: mv.Machine, ID: th.ID, Name: th.Name, Self: mv.Self})
			}
		}
	}
	return out, nil
}

// idFlagValue returns the value of --id in args (both `--id X` and `--id=X`), and
// whether the flag was present at all.
func idFlagValue(args []string) (string, bool) {
	for i, a := range args {
		switch {
		case a == "--id" || a == "-id":
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		case strings.HasPrefix(a, "--id="):
			return strings.TrimPrefix(a, "--id="), true
		case strings.HasPrefix(a, "-id="):
			return strings.TrimPrefix(a, "-id="), true
		}
	}
	return "", false
}

// remoteMachineForThreadID decides where a `sesh thread …` command with --id
// should run when no --machine was given. It returns "" to run here. args is the
// command line after the program name (subcommand first).
func remoteMachineForThreadID(cfg config.Config, args []string) (string, error) {
	if len(args) == 0 || args[0] != "thread" || cfg.RouteMachine != "" || cfg.RemoteAddr != "" {
		return "", nil
	}
	ref, ok := idFlagValue(args[1:])
	if !ok || ref == "" {
		return "", nil
	}
	matches, err := matchThreadsAcrossMesh(cfg, ref)
	if err != nil {
		// No local daemon to consult: keep the local behaviour, which says so itself.
		return "", nil
	}
	for _, m := range matches {
		if m.Self {
			return "", nil
		}
	}
	machines := map[string]bool{}
	for _, m := range matches {
		machines[m.Machine] = true
	}
	switch len(machines) {
	case 0:
		return "", nil
	case 1:
		return matches[0].Machine, nil
	default:
		return "", fmt.Errorf("thread id %q matches threads on several machines (%s); give the full id or pass --machine", ref, machineList(matches))
	}
}

func machineList(ms []meshThreadMatch) string {
	seen := map[string]bool{}
	var names []string
	for _, m := range ms {
		if !seen[m.Machine] {
			seen[m.Machine] = true
			names = append(names, m.Machine+" ("+m.ID[:8]+")")
		}
	}
	return strings.Join(names, ", ")
}

// threadFind implements `sesh thread find <id-or-prefix>`: list every thread the
// mesh holds with that id, and where it lives.
func threadFind(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return errors.New("thread find: pass one thread id or id prefix")
	}
	ref := fs.Arg(0)
	matches, err := matchThreadsAcrossMesh(cfg, ref)
	if err != nil {
		return fmt.Errorf("thread find: the local daemon is needed to read the mesh: %w", err)
	}
	if *asJSON {
		type row struct {
			Machine string `json:"machine"`
			ID      string `json:"id"`
			Name    string `json:"name"`
			Local   bool   `json:"local"`
		}
		out := []row{}
		for _, m := range matches {
			out = append(out, row{m.Machine, m.ID, m.Name, m.Self})
		}
		return emitJSON(out)
	}
	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "thread find: no thread with id or prefix %q on any machine the mesh knows\n", ref)
		return errors.New("not found")
	}
	for _, m := range matches {
		where := m.Machine
		if m.Self {
			where += " (this machine)"
		}
		fmt.Printf("%s  %s  %s\n", m.ID, where, m.Name)
	}
	return nil
}
