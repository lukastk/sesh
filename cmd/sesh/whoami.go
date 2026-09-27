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
//	exit 0   stdout is the full uuid, and the identity is VERIFIED — one of the
//	         two things a process living somewhere else cannot manufacture:
//	         the @sesh-thread-id marker on the tmux pane this process actually
//	         runs in, or (schema 50) the local daemon confirming that this
//	         process sits inside the process tree it created for that thread's
//	         headless turn. The second is how a scheduled/headless WORKER
//	         identifies itself: it has no pane, and before it existed the one
//	         process on the machine whose identity the daemon knew for certain
//	         was the only one that could not prove it.
//	exit 1   stderr says why the identity cannot be trusted, and stdout is
//	         EMPTY. Three distinct cases, each with its own text: the env id is
//	         contradicted by the calling directory; the env id is uncontradicted
//	         but still unverified (no pane, and no turn this daemon launched); or
//	         nothing identifies this process at all.
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
	"strings"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
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
		// A refusal that is only a refusal sent a real agent looking in the wrong
		// place: the daemon knew which thread it was, and the message gave it
		// nothing to ask. So say what the daemon DOES know about this caller —
		// as a lead to confirm, never as an answer.
		cwd, _ := os.Getwd()
		if lead := whoamiLead(c, cwd, paneLiveness(cfg)); lead != "" {
			return fmt.Errorf("%w\n\n%s", gateErr, lead)
		}
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
		var hm *harnessMismatchError
		if errors.As(err, &hm) {
			home, _ := os.UserHomeDir()
			return fmt.Errorf("NOT a verified identity: the agent session this process is serving is recorded "+
				"against thread %s (%q), whose cwd (%s) is unrelated to this directory (%s). Either the "+
				"harness is reporting a session that belongs to other work, or you are not the agent you "+
				"appear to be — do not sign, attach or act as that thread. Name the thread explicitly in "+
				"whatever you were about to run",
				short8(hm.ThreadID), hm.ThreadName,
				config.TildeRelative(hm.ThreadCwd, home), config.TildeRelative(hm.CallerCwd, home))
		}
		var none *noIdentityError
		if errors.As(err, &none) {
			// The wording here used to say flatly "you have NO sesh thread
			// identity", and on 2026-09-27 an agent believed it: it was thread
			// 1a26989d the whole time — pane, marker and cwd all registered — but
			// reparented away from its pane and carrying another project's
			// $SESH_THREAD_ID, so nothing in its environment could say so. It
			// concluded it was not a thread at all. Not being ABLE TO RESOLVE an
			// identity here is not the same as not HAVING one, and a refusal that
			// conflates the two sends an agent off to sign as something it is not.
			return errors.New("could not establish a verified identity for this process: no thread-marked " +
				"tmux pane, no turn this machine's daemon launched, no recognised agent session, and no " +
				"valid $" + agents.EnvThreadID + ". NOTE that this means UNRESOLVED, not 'you are not a " +
				"thread' — a reparented agent whose environment carries another project's id looks exactly " +
				"like this while the daemon knows perfectly well which thread it is. If a thread is named " +
				"below, confirm it before using it; if none is, ask the daemon what it knows about this " +
				"directory rather than assuming, and otherwise identify yourself by what you actually are " +
				"(the tool or job and its working directory) and never borrow a thread id")
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
		"is no tmux pane here to confirm it, the local daemon does not recognise this process as one of "+
		"the turns it launched, and a detached or background process inherits that variable from whatever "+
		"started it. The calling directory does not contradict it, but it does not confirm it either. Do "+
		"not sign, attach or act as this thread on the strength of it; name the thread explicitly in "+
		"whatever you were about to run, or pass --allow-unverified to accept $%s here anyway",
		short8(id), nameOf(id), agents.EnvThreadID, agents.EnvThreadID)
}

// maxLeadProbes bounds how many liveness probes a refusal will spend. A lead is
// worth one or two round trips on an error path; it is not worth walking a box
// where 174 threads share a directory.
const maxLeadProbes = 8

// whoamiLead is the "what the daemon does know about you" paragraph appended to a
// refusal. It answers the complaint that produced it — "nothing about my
// situation was unknowable, I just could not get it from the environment and did
// not think to ask the daemon" — without letting the gate pass a guess.
//
// THE EVIDENCE IT USES AND THE EVIDENCE IT REFUSES TO USE. Matching on cwd is a
// LEAD, never an identity, and the fleet says why: measured 2026-09-27 across
// 2,311 threads, 937 of them (41 %) sit on a cwd shared with another thread —
// ~/mysetup/sesh has 38 and one box has 174. So a cwd match cannot certify
// anything, and the bug report that asked for cwd+pid matching as the MECHANISM
// was asking for a coin flip in the common case. What it can do is narrow: when
// exactly ONE live thread sits in this exact directory, saying so turns a dead
// end into something checkable. More than one, and the honest move is to say how
// many and name none.
//
// Exact directory only, not containment: containment would drag a 174-thread box
// in as "candidates", which is noise dressed as help.
func whoamiLead(c threadListClient, cwd string, paneLive func(string) bool) string {
	here := canonicalDir(cwd)
	if here == "" || paneLive == nil {
		return ""
	}
	threads, err := listAllThreads(c)
	if err != nil {
		return ""
	}
	var inHere []api.Thread
	for _, th := range threads {
		if th.Archived || th.Cwd == "" {
			continue
		}
		if canonicalDir(th.Cwd) == here {
			inHere = append(inHere, th)
		}
	}
	if len(inHere) == 0 {
		return ""
	}
	// Probe liveness, bounded. A dead thread is not a lead: nothing is running in
	// it, so it cannot be the conversation this process is serving.
	var live []api.Thread
	for i, th := range inHere {
		if i >= maxLeadProbes {
			break
		}
		if paneLive(th.ID) {
			live = append(live, th)
		}
	}
	switch len(live) {
	case 0:
		return ""
	case 1:
		th := live[0]
		return fmt.Sprintf("WHAT THE DAEMON DOES KNOW: exactly one live thread is registered in this exact "+
			"directory — %s (%q), agent %s. That is a LEAD, NOT your identity, and you must not act as it "+
			"on the strength of this line. Confirm it first: `sesh info %s` describes it, and capturing its "+
			"pane shows whether the conversation on screen is the one you are having. If it is you, name "+
			"that thread explicitly in whatever you were about to run.",
			short8(th.ID), th.Name, th.AgentKind, th.ID)
	default:
		names := make([]string, 0, len(live))
		for _, th := range live {
			names = append(names, short8(th.ID)+" ("+th.Name+")")
		}
		return fmt.Sprintf("WHAT THE DAEMON DOES KNOW: %d live threads are registered in this exact directory "+
			"— %s. Several candidates is not a lead: do not pick one. If one of them is you, confirm which "+
			"by capturing their panes, then name it explicitly.",
			len(live), strings.Join(names, ", "))
	}
}
