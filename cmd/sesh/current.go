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
//     process. This is FROZEN into the process env at launch and INHERITED by
//     every descendant, so it can name a thread that is not this one at all
//     (an adopted/reparented agent carries its old id; a detached background
//     job carries whatever started it). It ranks BELOW the live pane marker,
//     and when it is the ONLY source the answer is UNVERIFIED — see the
//     provenance block further down: it is corroborated against the calling
//     directory and REFUSED when contradicted, and always announced as
//     unverified on stderr.
//
// When the env and a valid pane marker DISAGREE, the pane wins and a one-line
// drift note is emitted to stderr (loud, not silent). If nothing resolves it is
// a LOUD error. `delete` deliberately does NOT infer (destructive + ambient
// context is a footgun — always explicit).
//
// Residual limitation: a detached job whose inherited id happens to name a
// thread rooted in the SAME directory tree is still indistinguishable from the
// real thing — cwd is corroboration, not proof. Passing --id remains the only
// certainty available outside a pane.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
// uuid. Callers that care HOW the id was arrived at use resolveCurrentThread.
func resolveThreadID(cfg config.Config, explicit string) (string, error) {
	id, _, err := resolveCurrentThread(cfg, explicit)
	return id, err
}

// resolveThreadIDFor is resolveThreadID for a command whose "say which thread"
// flag is NOT --id: subscribe/unsubscribe take --from, `ticket list --current`
// and `hooks test` take --thread. A refusal has to name a flag the caller can
// actually pass, or the remedy it offers fails to parse.
func resolveThreadIDFor(cfg config.Config, explicit, idFlag string) (string, error) {
	id, _, err := resolveCurrentThreadWith(cfg, explicit, idFlag)
	return id, err
}

// resolveCurrentThread is resolveThreadID plus the PROVENANCE of the answer
// (see the block below). It reads the process state — env, pane, cwd — and
// emits any notes to stderr.
func resolveCurrentThread(cfg config.Config, explicit string) (string, idSource, error) {
	return resolveCurrentThreadWith(cfg, explicit, "")
}

// resolveCurrentThreadWith is resolveCurrentThread with the calling command's
// explicit-thread flag name, used in any refusal it raises.
func resolveCurrentThreadWith(cfg config.Config, explicit, idFlag string) (string, idSource, error) {
	c := daemonClient(cfg)
	if explicit != "" {
		id, err := resolveIDPrefix(c, explicit)
		return id, srcExplicit, err
	}
	paneID, _ := paneThreadID() // ("", err) is a legitimate not-here; treat as unmarked
	cwd, _ := os.Getwd()
	id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{
		env:             os.Getenv(agents.EnvThreadID),
		paneID:          paneID,
		cwd:             cwd,
		allowUnverified: allowUnverifiedCurrent,
		idFlag:          idFlag,
	})
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "sesh: "+n)
	}
	return id, src, err
}

// ---------------------------------------------------------------------------
// PROVENANCE: how much the resolved "current thread" can be trusted.
//
// Inference has two sources and they are NOT equally trustworthy:
//
//   - the live pane marker is VERIFIED — it is read from the pane this process
//     actually runs in, so it cannot be inherited by a process living somewhere
//     else;
//   - $SESH_THREAD_ID is UNVERIFIED — it is frozen into the process env at
//     launch and is inherited by every descendant, including descendants that
//     are no longer that thread. A claude BACKGROUND job/agent is hosted by a
//     machine-global `claude daemon run` that froze whichever pane's env
//     happened to start it, so its shells carry a VALID id belonging to an
//     UNRELATED thread (H82). From sesh's side the id looks perfectly good.
//
// That is not a hypothetical: on 2026-08-25 an agent in the boxyard-go thread
// asked `sesh info` who it was, was told (confidently) that it was the
// "mysetup - sesh" thread, and its self-compact runner then compacted that
// thread and injected a foreign handover prompt into it. The answer was wrong
// and nothing said so.
//
// So an env-derived answer now (a) always says it is unverified, and (b) is
// CORROBORATED against the calling directory: if the named thread's cwd and the
// caller's cwd are unrelated, the id is contradicted and inference REFUSES
// rather than guessing. `--allow-unverified` overrides it.
//
// The corroboration follows H82's ONE-DIRECTIONAL EVIDENCE rule: only a
// POSITIVE contradiction may refuse. Missing cwd, an unreadable path, or
// containment in either direction all read as "no contradiction" and still
// resolve. A false positive costs one loud, actionable error; a false negative
// costs someone else's session.

// idSource is the provenance of a resolved thread id.
type idSource string

const (
	srcExplicit idSource = "explicit" // the caller passed an id/prefix
	srcPane     idSource = "pane"     // the calling pane's @sesh-thread-id marker
	srcEnv      idSource = "env"      // $SESH_THREAD_ID, with no pane to check it against
)

// verified reports whether the id came from something the calling process could
// not have merely INHERITED. Only an unverified id is corroborated/refusable.
func (s idSource) verified() bool { return s == srcExplicit || s == srcPane }

// allowUnverifiedCurrent is the pseudo-global `--allow-unverified` escape hatch,
// stripped from os.Args before dispatch (see extractAllowUnverifiedFlag). It is
// process-wide because current-thread inference is reached from ~20 verbs; the
// resolver itself takes it as an argument so it stays testable.
var allowUnverifiedCurrent bool

// unverifiedError is the refusal raised when an env-derived id is positively
// contradicted by the calling directory. It is a distinct type so callers that
// infer OPTIONALLY (thread new's parent) can tell "you are not in a thread"
// (fine, carry on with no parent) apart from "the id here is not to be trusted"
// (loud — the mis-parent this prevents was invisible for ten hours, 6ea1f6eb).
type unverifiedError struct {
	ThreadID  string
	ThreadCwd string
	CallerCwd string
	// Flag is the flag THIS command accepts for naming a thread explicitly.
	// Empty means the usual --id. It exists because the remedy in a loud error
	// has to be a remedy the caller can actually type: `sesh subscribe` takes
	// --from and has no --id at all, so telling its caller to "pass --id" sends
	// them to `flag provided but not defined: -id`. That is not hypothetical —
	// a supervisor thread hit exactly this on 2026-08-27 and its subscriptions
	// silently never existed for an hour.
	Flag string
}

// idFlagOr returns the flag name to suggest, defaulting to --id.
func idFlagOr(flag string) string {
	if flag == "" {
		return "--id"
	}
	return flag
}

func (e *unverifiedError) Error() string {
	home, _ := os.UserHomeDir()
	return fmt.Sprintf("$%s=%s names a thread whose cwd (%s) is unrelated to the calling directory (%s) — "+
		"refusing to guess which thread this is. There is no tmux pane here to check the id against, and a "+
		"detached/background process inherits a stale $%s from whatever started it. "+
		"Pass %s <thread> to say which thread you mean, or --allow-unverified to use $%s anyway",
		agents.EnvThreadID, short8(e.ThreadID),
		config.TildeRelative(e.ThreadCwd, home), config.TildeRelative(e.CallerCwd, home),
		agents.EnvThreadID, idFlagOr(e.Flag), agents.EnvThreadID)
}

// currentInputs is the process state inference reads. Injected rather than read
// from the environment inside the resolver so the whole truth table is unit
// testable without a tmux pane or a rewritten process env.
type currentInputs struct {
	env             string // $SESH_THREAD_ID
	paneID          string // the calling pane's @sesh-thread-id marker ("" = no pane, or unmarked)
	cwd             string // the calling process's working directory
	allowUnverified bool
	idFlag          string // the flag THIS command takes for an explicit thread ("" = --id)
}

// resolveCurrentThreadFrom is the inference truth table (see the package comment
// for the precedence and the block above for the trust model). Notes are
// returned rather than printed so tests can assert on them; the caller emits
// them to stderr.
func resolveCurrentThreadFrom(c threadListClient, in currentInputs) (id string, src idSource, notes []string, err error) {
	// The live pane marker is ground truth and outranks the env: it is
	// re-stamped on adopt/reparent, whereas the env is frozen at launch and may
	// carry a stale-but-still-valid id (the SESH_THREAD_ID drift bug).
	if in.paneID != "" {
		if _, ok := lookupThread(c, in.paneID); ok {
			if in.env != "" && in.env != in.paneID {
				notes = append(notes, fmt.Sprintf("$%s=%s is stale; using live pane marker %s (pane adopted/reparented since launch)",
					agents.EnvThreadID, short8(in.env), short8(in.paneID)))
			}
			return in.paneID, srcPane, notes, nil
		}
	}
	// No pane (a headless turn — env injected, no tmux pane), or the pane carries
	// no valid marker: the env is all there is, and it is UNVERIFIED. A stale env
	// (deleted thread, leaked across homes) falls through to the loud error.
	if in.env != "" {
		if th, ok := lookupThread(c, in.env); ok {
			if cwdContradicts(th.Cwd, in.cwd) {
				if !in.allowUnverified {
					return "", "", notes, &unverifiedError{ThreadID: in.env, ThreadCwd: th.Cwd, CallerCwd: in.cwd, Flag: in.idFlag}
				}
				notes = append(notes, fmt.Sprintf("--allow-unverified: using $%s=%s (%q) even though its cwd is unrelated to this directory",
					agents.EnvThreadID, short8(in.env), th.Name))
			}
			notes = append(notes, fmt.Sprintf("unverified current thread %s (%q) — from $%s; there is no tmux pane here to confirm it",
				short8(in.env), th.Name, agents.EnvThreadID))
			return in.env, srcEnv, notes, nil
		}
	}
	return "", "", notes, fmt.Errorf("not inside a sesh thread: no %s, no valid $%s, and no thread-marked tmux pane — pass %s",
		idFlagOr(in.idFlag), agents.EnvThreadID, idFlagOr(in.idFlag))
}

// cwdContradicts reports whether the calling directory POSITIVELY contradicts a
// thread's recorded cwd. Containment in EITHER direction reads as agreement (an
// agent working in a subdirectory of its thread's cwd is entirely normal, and a
// thread cwd nested under the caller's is ambiguous rather than contradictory);
// only two unrelated trees are a contradiction. Anything unknown or unreadable
// is "no contradiction" — see the one-directional evidence rule above.
func cwdContradicts(threadCwd, callerCwd string) bool {
	a, b := canonicalDir(threadCwd), canonicalDir(callerCwd)
	if a == "" || b == "" {
		return false // no evidence either way
	}
	return !withinDir(a, b) && !withinDir(b, a)
}

// canonicalDir renders a directory comparable: ~-expanded, absolute, cleaned and
// symlink-resolved (a box under ~/dev is routinely reached through a symlink, and
// an unresolved pair would read as two unrelated trees). A path that does not
// exist keeps its lexical form — it is still comparable, just not resolved.
func canonicalDir(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	if !filepath.IsAbs(p) {
		return "" // a relative thread cwd is not comparable to anything — no evidence
	}
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// withinDir reports whether child is dir itself or sits underneath it.
func withinDir(dir, child string) bool {
	return child == dir || strings.HasPrefix(child, strings.TrimRight(dir, "/")+string(filepath.Separator))
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
	return resolveMeshThreadIDFor(c, cfg, explicit, "")
}

// resolveMeshThreadIDFor is resolveMeshThreadID for a command whose
// explicit-thread flag is not --id (see resolveThreadIDFor).
func resolveMeshThreadIDFor(c meshThreadClient, cfg config.Config, explicit, idFlag string) (string, error) {
	if explicit == "" {
		return resolveThreadIDFor(cfg, "", idFlag)
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
