package main

// `sesh whoami` — the DEFAULT-SAFE identity primitive.
//
// It exists because `sesh info` is a DIAGNOSTIC verb and whoami is a GATE, and
// the two want opposite defaults. `info` answers "tell me about this thread":
// when the only evidence is an inherited $SESH_THREAD_ID it says so (source:
// env, verified: false, a note on stderr) and still exits 0, because refusing
// to describe a thread is exactly the wrong move when you are diagnosing. That
// leaves the safe reading of it optional — `sesh info --json | jq -r
// 'select(.source == "pane") | .thread.id'` — and an optional ritual is one
// nobody performs. The skills that were supposed to carry it did not: the
// self-compact runner had to be patched for this in H92, and on 2026-09-25 a
// claude background job in a mosaic-v3 course box reported that its inherited
// $SESH_THREAD_ID named `adi-requests`, a live headful thread in an unrelated
// box, and that the standard advice ("sign with $SESH_THREAD_ID") produces
// exactly that misattribution silently.
//
// So whoami answers a different question — "may I act as this thread?" — and
// the answer is the EXIT CODE:
//
//	exit 0   stdout is the full uuid, and the identity is VERIFIED: it was read
//	         from the @sesh-thread-id marker on the tmux pane this process
//	         actually runs in, which a process living somewhere else cannot
//	         inherit.
//	exit 1   stderr says why the identity cannot be trusted, and stdout is
//	         EMPTY. Three distinct cases, each with its own text: the env id is
//	         contradicted by the calling directory; the env id is uncontradicted
//	         but still unverified (no pane); or nothing identifies this process
//	         at all.
//
// `TID=$(sesh whoami) || exit 1` is therefore safe by construction, which the
// jq form never was. --allow-unverified (the pseudo-global) downgrades the gate
// to info's tolerance for the callers that genuinely want it.
//
// whoami takes NO thread selector, deliberately: naming a thread would make the
// answer "explicit", i.e. trivially verified, which answers a question nobody
// asked. To describe some OTHER thread, that is `sesh info --id <thread>`.

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/config"
)

func runWhoami(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	// --machine is declared only to REFUSE it. whoami is excluded from
	// routableSubcommand, so the flag reaches this flagset instead of being
	// stripped by the router; without the declaration the caller would get
	// flag's bare "flag provided but not defined: -machine", which does not
	// say why routing is meaningless here.
	machine := fs.String("machine", "", "not routable (see the refusal)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *machine != "" {
		return fmt.Errorf("whoami reports who THIS process is; it cannot be routed to %q — "+
			"the peer would read its OWN pane and environment, not yours, and answer confidently about "+
			"a different machine. Run it where the caller lives; to describe a thread elsewhere use "+
			"`sesh info --id <thread> --machine %s`", *machine, *machine)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("whoami takes no thread argument (it answers who THIS process is) — "+
			"to describe thread %q use `sesh info %s`", fs.Arg(0), fs.Arg(0))
	}

	c := daemonClient(cfg)
	// Best-effort name for the refusal texts. A refusal must never itself fail
	// because a lookup did, so an unknown/unreachable thread renders as "?" —
	// the id is the load-bearing part of the message.
	nameOf := func(id string) string {
		if th, ok := lookupThread(c, id); ok && th.Name != "" {
			return th.Name
		}
		return "?"
	}

	id, src, err := resolveCurrentThread(cfg, "")
	if gateErr := whoamiGate(id, src, err, allowUnverifiedCurrent, nameOf); gateErr != nil {
		return gateErr
	}

	if *asJSON {
		th, ok := lookupThread(c, id)
		if !ok {
			return fmt.Errorf("thread %s vanished mid-lookup", id)
		}
		return emitJSON(map[string]any{
			"id":       th.ID,
			"short":    short8(th.ID),
			"name":     th.Name,
			"machine":  th.Machine,
			"cwd":      th.Cwd,
			"agent":    th.AgentKind,
			"source":   string(src),
			"verified": src.verified(),
		})
	}
	// The bare uuid and nothing else: `TID=$(sesh whoami)` is the point.
	fmt.Println(id)
	return nil
}

// whoamiGate is the whole decision, pure and injectable: given what inference
// arrived at, may this process claim to BE that thread? nil means yes. It is
// separate from runWhoami so the truth table is unit-testable with no daemon,
// no tmux pane and no rewritten process env.
//
// It re-phrases the resolver's refusals rather than passing them through,
// because the resolver's text offers `--id` (or whichever flag the calling verb
// takes) and whoami takes none — a remedy the caller cannot type is only half a
// loud error, which is the H95 lesson.
func whoamiGate(id string, src idSource, err error, allowUnverified bool, nameOf func(string) string) error {
	if err != nil {
		var unver *unverifiedError
		if errors.As(err, &unver) {
			home, _ := os.UserHomeDir()
			return fmt.Errorf("NOT a verified identity: $%s=%s names %q, whose cwd (%s) is unrelated to "+
				"this directory (%s). There is no tmux pane here to check the id against, and a detached "+
				"or background process inherits that variable from whatever started it — so this is very "+
				"likely another thread's id, not yours. Do not sign, attach or act as it. "+
				"Pass --allow-unverified to accept $%s here anyway",
				agents.EnvThreadID, short8(unver.ThreadID), nameOf(unver.ThreadID),
				config.TildeRelative(unver.ThreadCwd, home), config.TildeRelative(unver.CallerCwd, home),
				agents.EnvThreadID)
		}
		var none *noIdentityError
		if errors.As(err, &none) {
			return errors.New("this process has NO sesh thread identity: there is no thread-marked tmux " +
				"pane here and no valid $" + agents.EnvThreadID + ". That is an answer, not a failure — " +
				"identify yourself by what you actually are (the tool or job and its working directory), " +
				"and never borrow a thread id to sign with")
		}
		return err
	}
	if src.verified() {
		return nil
	}
	if allowUnverified {
		return nil
	}
	// The UNCONTRADICTED env case — the residual the cwd corroboration cannot
	// reach. The calling directory neither confirms nor denies the inherited id
	// (a detached job started from a pane whose thread is rooted in the same
	// tree looks identical to the real agent), so corroboration stays silent
	// and `sesh info` exits 0. For a GATE that is not good enough: absence of
	// contradiction is not evidence.
	return fmt.Errorf("NOT a verified identity: the current thread %s (%q) rests on $%s alone — there "+
		"is no tmux pane here to confirm it, and a detached or background process inherits that variable "+
		"from whatever started it. The calling directory does not contradict it, but it does not confirm "+
		"it either. Do not sign, attach or act as this thread on the strength of it; name the thread "+
		"explicitly in whatever you were about to run, or pass --allow-unverified to accept $%s here anyway",
		short8(id), nameOf(id), agents.EnvThreadID, agents.EnvThreadID)
}
