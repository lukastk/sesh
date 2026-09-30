package daemon

// claudebackground.go is sesh's side of claude's background-session registry
// (sesh#16). See internal/agents/claude/background.go for the mechanism; the short
// version is that a claude conversation can be OWNED by a background session, and
// while it is, `claude --resume <leaf>` refuses — so every revive of the thread
// whose leaf that is fails, indefinitely, with nothing surfacing it.
//
// Two surfaces use this:
//   - doctor: report a hold BEFORE someone trips over it (the whole point — the two
//     production cases went unnoticed for 5 and 10 days).
//   - revive: refuse with the hold NAMED, and clear it on an explicit --force.
//
// Everything here is claude-only, deliberately: no other agent sesh drives has a
// background-session concept. codex's analogue is its shared app-server holding a
// rollout writer lock, which sesh prevents outright by pinning daemon_auto_start
// off (sesh#15, codexdaemon.go) — a pin has no analogue here, because backgrounding
// is a first-class claude feature and not a setting sesh may override.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/agents/claude"
	"github.com/lukastk/sesh/internal/api"
)

// claudeRegistryTimeout bounds the `claude agents --json` / `claude stop` hops. Both
// are local and sub-second in practice; the bound exists so neither can wedge a
// doctor request or a revive.
const claudeRegistryTimeout = 20 * time.Second

// daemonShell is the shell the daemon launches agents through ($SHELL, as tmux and
// headless turns use). Kept in one place so the registry hop resolves claude the
// same way a spawn does.
func daemonShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// heldBackgroundSession reports the background session that currently owns
// sessionID, if any.
//
// Three outcomes, and keeping them distinct is the point:
//   - (bg, true, nil)  — held; bg names the remedy.
//   - (_, false, nil)  — provably not held.
//   - (_, false, err)  — COULD NOT TELL. Never collapsed into "not held"; each
//     caller decides what to do with uncertainty, and neither pretends.
//
// Non-claude kinds and an empty sessionID are provably not held (no other agent has
// the concept; a thread with no session id has no conversation to hold).
func (d *Daemon) heldBackgroundSession(ctx context.Context, kind agents.Kind, sessionID string) (claude.BackgroundSession, bool, error) {
	if kind != agents.Claude || sessionID == "" {
		return claude.BackgroundSession{}, false, nil
	}
	sessions, err := claude.BackgroundSessions(ctx, daemonShell())
	if err != nil {
		return claude.BackgroundSession{}, false, err
	}
	for _, bg := range sessions {
		if bg.SessionID == sessionID {
			return bg, true, nil
		}
	}
	return claude.BackgroundSession{}, false, nil
}

// heldSessionRefusal explains a revive that FAILED because the thread's conversation
// is owned by a live background session. It is only ever used after claude has
// actually refused, so it states a fact rather than a prediction — and it names the
// holder, what it is doing and every remedy in typeable form, because a refusal whose
// remedy the caller cannot type is only half a loud error (H95).
func heldSessionRefusal(threadID string, bg claude.BackgroundSession, leaf string) string {
	doing := bg.State
	if doing == "" {
		doing = "unknown state"
	}
	msg := fmt.Sprintf(
		"revive: this thread's claude conversation is owned by a BACKGROUND SESSION (%s, state %q%s), and claude "+
			"refused to resume it — which is why the pane exited immediately. That happens when the holder was "+
			"started by a DIFFERENT claude build than the one reviving (claude updates near-daily, so any holder "+
			"that outlives a release lands here).",
		bg.ID, doing, nameSuffix(bg.Name))
	if leaf != "" {
		msg += fmt.Sprintf(" Held session: %s.", leaf)
	}
	msg += fmt.Sprintf(
		" Remedies: `claude attach %s` to open it in THIS terminal without stopping it; "+
			"or re-run with --force, which runs `claude stop %s` (the conversation is KEPT) and then revives; "+
			"or `claude stop %s` by hand and retry. "+
			"NB stopping a session whose state is \"working\" interrupts a turn that is running right now.",
		bg.ID, bg.ID, bg.ID)
	if threadID != "" {
		msg += " Thread: " + threadID + "."
	}
	return msg
}

func nameSuffix(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	return ", " + fmt.Sprintf("%q", name)
}

// clearHeldBackgroundSession stops the background session holding a thread's
// conversation and CONFIRMS the hold is gone before returning. The confirmation is
// not ceremony: `claude stop` exiting 0 is not proof the registry released the
// session, and returning success here would hand the caller a revive that then dies
// on claude's own 409 with the generic "agent exited immediately" message — the
// very confusion this change removes.
func (d *Daemon) clearHeldBackgroundSession(ctx context.Context, bg claude.BackgroundSession) error {
	sh := daemonShell()
	if err := claude.StopBackgroundSession(ctx, sh, bg.ID); err != nil {
		return err
	}
	// Measured on 2.1.286: the registry drops the entry within ~1 s of `claude stop`
	// returning. Poll rather than sleep a fixed slug, and fail loudly if it is still
	// there — a still-held session is a different problem from a failed stop.
	deadline := time.Now().Add(10 * time.Second)
	for {
		sessions, err := claude.BackgroundSessions(ctx, sh)
		if err != nil {
			return fmt.Errorf("after `claude stop %s`: %w", bg.ID, err)
		}
		still := false
		for _, s := range sessions {
			if s.SessionID == bg.SessionID {
				still = true
				break
			}
		}
		if !still {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("`claude stop %s` returned success but the session is STILL registered as a background session", bg.ID)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// doctorClaudeBackground adds the background-session checks.
//
// It runs only where claude has been used (the claude home exists), mirroring the
// codex rows' guard: creating or probing a claude home on a machine with no claude
// (termux) would report about nothing.
func (d *Daemon) doctorClaudeBackground(add func(name, status, detail string)) {
	homes := agents.ResolveHomes(d.cfg.CodexHome)
	if st, err := os.Stat(homes.Claude); err != nil || !st.IsDir() {
		return // claude never used here
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeRegistryTimeout)
	defer cancel()
	sessions, err := claude.BackgroundSessions(ctx, daemonShell())
	if err != nil {
		// "Could not tell" is reported as such. An absent check is not evidence of
		// health — the H75 lesson, and the reason the api row was made to speak up.
		add("claude background sessions", "warn", "cannot read claude's background-session registry: "+err.Error()+
			" — a held session would be invisible here")
		return
	}
	if len(sessions) == 0 {
		add("claude background sessions", "ok", "none registered")
		return
	}

	held, cerr := d.threadsHeldByBackgroundSessions(sessions, homes)
	if cerr != nil {
		add("claude background sessions", "warn", fmt.Sprintf(
			"%d background session(s) registered, but sesh could not check them against its threads: %v", len(sessions), cerr))
		return
	}
	if len(held) == 0 {
		add("claude background sessions", "ok", fmt.Sprintf(
			"%d registered, none owning a live thread's conversation", len(sessions)))
		return
	}
	for _, h := range held {
		// warn, not fail: a hold does not always BREAK a revive, it changes what one
		// means. MEASURED — a holder from a DIFFERENT claude build cannot be taken over,
		// so claude refuses `--resume` and the thread cannot be revived at all (the
		// production case, unnoticed for days); a holder from the SAME build makes an
		// interactive claude re-exec itself as `claude attach`, so the revive appears to
		// work but the pane is a VIEW onto the background session rather than a
		// conversation it owns. Both are worth knowing before someone meets them at
		// revive time; neither is a daemon fault, so neither is a "fail".
		add("claude background session:"+h.Thread.ID[:8], "warn", fmt.Sprintf(
			"thread %q (%s): its claude conversation is owned by background session %s (state %q%s, leaf %s). "+
				"While it is held, reviving this thread either is REFUSED by claude (a live holder) or silently becomes "+
				"`claude attach %s` instead of a resume (a settled one). "+
				"`sesh thread headful --id %s --force` stops the holder (`claude stop %s`, conversation KEPT) and does a real resume; "+
				"`claude attach %s` opens it here without stopping it. NB state \"working\" means a turn is running right now.",
			h.Thread.Name, h.Thread.ID[:8], h.Background.ID, h.Background.State, nameSuffix(h.Background.Name),
			h.Leaf, h.Background.ID, h.Thread.ID[:8], h.Background.ID, h.Background.ID))
	}
}

// heldThread pairs a local thread with the background session owning its leaf.
type heldThread struct {
	Thread     api.Thread
	Background claude.BackgroundSession
	Leaf       string
}

// threadsHeldByBackgroundSessions finds the local, non-archived claude threads whose
// resolved leaf session is one of the given background sessions.
//
// CANDIDATE NARROWING, and it is exact rather than an optimisation that could miss:
// a thread's leaf is resolved inside ProjectDir(claudeHome, thread.Cwd), so a
// session can only ever be a thread's leaf if the two live in the SAME project dir.
// Comparing project-dir names therefore cannot drop a real hold — while resolving
// the leaf of every claude thread would scan every transcript in every project dir
// (48 MB files exist on this fleet) on a request meant to be a quick diagnostic.
func (d *Daemon) threadsHeldByBackgroundSessions(sessions []claude.BackgroundSession, homes agents.Homes) ([]heldThread, error) {
	threads, err := d.store.ListThreads(false) // non-archived only: an archived thread is not revived
	if err != nil {
		return nil, err
	}
	resolve := func(sessionID, cwd string) (string, error) {
		return agents.ResolveCurrentSession(agents.Claude, sessionID, cwd, homes)
	}
	return matchHeldThreads(threads, sessions, resolve)
}

// matchHeldThreads is the pure matcher behind threadsHeldByBackgroundSessions, with
// the leaf resolver injected so the NARROWING and MATCHING rules are tested without
// a store, a claude home or any transcript on disk (the codexdaemon.go precedent).
// It also makes the narrowing observable: the resolver must be called for exactly
// the candidate threads and no others.
func matchHeldThreads(threads []api.Thread, sessions []claude.BackgroundSession, resolve func(sessionID, cwd string) (string, error)) ([]heldThread, error) {
	byProject := map[string][]claude.BackgroundSession{}
	for _, bg := range sessions {
		if bg.Cwd == "" {
			// No cwd means no project dir to narrow on. Rather than silently ignore
			// the session, say so: it is a shape we have not seen and would make the
			// check incomplete without admitting it.
			return nil, fmt.Errorf("background session %s has no cwd — cannot tell which threads it could hold", bg.ID)
		}
		key := claude.ProjectDirName(bg.Cwd)
		byProject[key] = append(byProject[key], bg)
	}
	var out []heldThread
	for _, th := range threads {
		if th.Archived {
			continue // an archived thread is not revived
		}
		if th.AgentKind != string(agents.Claude) || th.AgentSessionID == "" || th.Cwd == "" {
			continue
		}
		cands := byProject[claude.ProjectDirName(th.Cwd)]
		if len(cands) == 0 {
			continue
		}
		leaf, rerr := resolve(th.AgentSessionID, th.Cwd)
		if rerr != nil {
			return nil, fmt.Errorf("resolve the live session of thread %s: %w", th.ID[:8], rerr)
		}
		for _, bg := range cands {
			if bg.SessionID == leaf {
				out = append(out, heldThread{Thread: th, Background: bg, Leaf: leaf})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Thread.ID < out[j].Thread.ID })
	return out, nil
}
