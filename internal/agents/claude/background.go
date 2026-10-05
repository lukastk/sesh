package claude

// background.go reads claude's BACKGROUND SESSION registry.
//
// WHY sesh cares. A claude conversation can stop being owned by a terminal and
// become a *background session*: `claude --bg [--resume <id>]` at launch, or a live
// session going through the `/background` code path, which writes a
//
//	{"type":"continued-in","sessionId":"<old>","continuedInSessionId":"<new>"}
//
// record into the old transcript, registers the successor as a background session,
// and EXITS the pane. Measured twice on Lukas's fleet — 07998bab -> ef96bf74 at
// 2026-09-23T07:53:00.008Z and f1a4d35a -> 92e176d4 at 2026-09-18T20:54:58.363Z —
// with claude's daemon log recording `[bg] bg claimed-spare <id> (slash)`, the same
// tag typing `/background` produces. BUT NOBODY TYPED IT: claude's prompt history
// (~/.claude/history.jsonl) records every slash command and holds no `/background` or
// `/bg` at all. What invokes that path is not yet known; ruled out by measurement are a
// pane kill (sesh thread stop / the TUI's `x`, even mid-tool), Ctrl-C, Ctrl-B, /quit,
// /clear and a claude auto-update.
//
// While a session is held, `claude --resume <id>` REFUSES:
//
//	That session is running in the background (<short id>). Run `claude attach
//	<short id>` to open it, or `claude stop <short id>` first to resume it here.
//
// sesh resolves a claude thread's session forward through the drift chain
// (ResolveLeafSession — correct, and necessary: the pre-handoff file is frozen),
// so the leaf it lands on is EXACTLY the held session, and every revive of that
// thread fails until the hold is released. Two threads sat un-revivable for 5
// and 10 days this way with nothing surfacing it, which is why sesh now both
// reports the hold (doctor) and can clear it on request (revive --force).
//
// SCOPE: the registry is per-CLAUDE_CONFIG_DIR, so it must be read with the same
// environment sesh launches claude panes with — otherwise a held session is
// invisible and we would report a clean bill of health for the wrong registry.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// BackgroundSession is one background entry of `claude agents --json`.
type BackgroundSession struct {
	// ID is the SHORT id, and it is the one the remedy needs: `claude stop|attach|
	// logs <ID>`. The full uuid is not accepted by those verbs.
	ID string
	// SessionID is the full session uuid — what sesh's leaf resolution yields, and
	// therefore the key a thread is matched on.
	SessionID string
	Cwd       string
	Name      string
	// State is claude's own word for what the session is doing: measured values
	// include "working" (a turn in flight), "blocked" (idle, awaiting input) and
	// "done". Passed through verbatim rather than interpreted — it is claude's
	// vocabulary and it decides whether stopping the session costs live work.
	State  string
	Status string
}

// agentsEntry is the subset of one `claude agents --json` element that sesh reads
// (shape verified against claude 2.1.286).
type agentsEntry struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Status    string `json:"status"`
}

// ParseBackgroundSessions is the PURE parser over `claude agents --json` output,
// kept separate so its scope is tested without running claude.
//
// It is deliberately LOUD about shape: a background entry missing either id is an
// error, not a skipped row. Both fields are load-bearing — SessionID is the key a
// thread is matched on, ID is the remedy the message has to name — so silently
// dropping such a row would report "nothing is held" about a registry we could not
// actually read, which is the exact failure this file exists to prevent. Entries of
// any other kind (interactive, …) are not background sessions and are ignored.
func ParseBackgroundSessions(out []byte) ([]BackgroundSession, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, fmt.Errorf("claude agents --json printed nothing")
	}
	var entries []agentsEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return nil, fmt.Errorf("claude agents --json: %w (got %q)", err, snippet(trimmed))
	}
	var bg []BackgroundSession
	for i, e := range entries {
		if e.Kind != "background" {
			continue
		}
		if e.SessionID == "" || e.ID == "" {
			return nil, fmt.Errorf(
				"claude agents --json: background entry %d has id=%q sessionId=%q — sesh needs both "+
					"(sessionId to match a thread, id for the `claude stop <id>` remedy); the output shape has changed",
				i, e.ID, e.SessionID)
		}
		bg = append(bg, BackgroundSession{
			ID:        e.ID,
			SessionID: e.SessionID,
			Cwd:       e.Cwd,
			Name:      e.Name,
			State:     e.State,
			Status:    e.Status,
		})
	}
	return bg, nil
}

// BackgroundSessions lists the background sessions claude currently holds, run
// through shell so PATH and CLAUDE_CONFIG_DIR resolve exactly as they do for a
// spawned pane. An error means "could not tell", never "none held".
func BackgroundSessions(ctx context.Context, shell string) ([]BackgroundSession, error) {
	cmd := exec.CommandContext(ctx, shell, "-lc", "claude agents --json")
	out, err := cmd.Output()
	if err != nil {
		detail := err.Error()
		if ee, ok := err.(*exec.ExitError); ok {
			if s := strings.TrimSpace(string(ee.Stderr)); s != "" {
				detail += ": " + snippet(s)
			}
		}
		return nil, fmt.Errorf("run `claude agents --json`: %s", detail)
	}
	return ParseBackgroundSessions(out)
}

// StopBackgroundSession runs `claude stop <id>`, which stops the background session
// while KEEPING its conversation — claude's own help: "Its conversation is kept:
// `claude attach <id>` opens it again, `claude --resume` works once it is stopped".
func StopBackgroundSession(ctx context.Context, shell, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("stop background session: empty id")
	}
	// The id comes from claude's own registry output and is matched against the
	// shell-safe shape below before it is ever put in a command line.
	if !safeSessionRef(id) {
		return fmt.Errorf("stop background session: refusing id %q — not a plain session reference", id)
	}
	cmd := exec.CommandContext(ctx, shell, "-lc", "claude stop "+id)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run `claude stop %s`: %w: %s", id, err, snippet(strings.TrimSpace(string(out))))
	}
	return nil
}

// safeSessionRef accepts only the hex-and-dash shape of a claude session id (short
// or full). The id is interpolated into a shell command line, so anything else is
// refused rather than quoted — a registry that starts emitting something exotic
// should fail loudly here, not become a command injection.
func safeSessionRef(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

func snippet(s string) string {
	const max = 200
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
