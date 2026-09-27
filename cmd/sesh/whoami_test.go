package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/config"
)

// nameOfStub is the best-effort name lookup whoamiGate takes; the refusal texts
// quote it, nothing branches on it.
func nameOfStub(string) string { return "adi-requests" }

// TestWhoamiGate is the whole contract: which resolutions may a process claim
// as its own identity. The GATE is the product — `sesh info` resolves exactly
// the same answers and exits 0 for every row below.
func TestWhoamiGate(t *testing.T) {
	const id = "c194478c-e3be-4203-a09b-c58a63845de2"

	t.Run("a pane-verified identity is accepted", func(t *testing.T) {
		if err := whoamiGate(id, srcPane, nil, false, nameOfStub); err != nil {
			t.Fatalf("a live pane marker is the one thing that cannot be inherited; want accepted, got %v", err)
		}
	})

	t.Run("an explicit identity is accepted", func(t *testing.T) {
		if err := whoamiGate(id, srcExplicit, nil, false, nameOfStub); err != nil {
			t.Fatalf("want accepted, got %v", err)
		}
	})

	// THE RESIDUAL cwd corroboration cannot reach, and the reason whoami exists
	// as a separate verb at all: nothing is contradicted, so the resolver is
	// silent and `sesh info` exits 0 — but nothing is CONFIRMED either.
	t.Run("an uncontradicted env identity is REFUSED (absence of contradiction is not evidence)", func(t *testing.T) {
		err := whoamiGate(id, srcEnv, nil, false, nameOfStub)
		if err == nil {
			t.Fatal("an inherited $SESH_THREAD_ID with no pane to confirm it must not pass as an identity")
		}
		if !strings.Contains(err.Error(), "NOT a verified identity") {
			t.Fatalf("the refusal must say plainly that this is not an identity; got %q", err)
		}
		if !strings.Contains(err.Error(), "--allow-unverified") {
			t.Fatalf("the refusal must name its override; got %q", err)
		}
	})

	t.Run("--allow-unverified downgrades the gate to info's tolerance", func(t *testing.T) {
		if err := whoamiGate(id, srcEnv, nil, true, nameOfStub); err != nil {
			t.Fatalf("want accepted under --allow-unverified, got %v", err)
		}
	})

	t.Run("a CONTRADICTED env identity is refused and names both directories", func(t *testing.T) {
		src := &unverifiedError{
			ThreadID:  id,
			ThreadCwd: "/home/lukastk/dev/20260622_oo996d__ADI-website",
			CallerCwd: "/home/lukastk/dev/20260917_4cr9iq__mosaic-v3/courses/finnish",
		}
		err := whoamiGate("", "", src, false, nameOfStub)
		if err == nil {
			t.Fatal("want refused")
		}
		for _, want := range []string{"NOT a verified identity", "ADI-website", "finnish", "adi-requests"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal must contain %q so the reader can see WHICH thread it nearly became; got %q", want, err)
			}
		}
	})

	// THIS ASSERTION WAS REWRITTEN ON 2026-09-27, and the old one is the reason.
	// It pinned the phrase "NO sesh thread identity", and an agent believed it: it
	// WAS a thread (1a26989d — pane, marker and cwd all registered) but was
	// reparented away from its pane and carried another project's
	// $SESH_THREAD_ID, so nothing in its environment could say so. It read the
	// refusal as a statement about what it was, concluded it was not a thread at
	// all, and wrote a wrong id into three other threads' inboxes. Unresolvable
	// and not-a-thread are different claims, and only one of them is ours to make.
	t.Run("no identity resolved says UNRESOLVED, not 'you are not a thread'", func(t *testing.T) {
		err := whoamiGate("", "", &noIdentityError{}, false, nameOfStub)
		if err == nil {
			t.Fatal("want refused")
		}
		msg := err.Error()
		if !strings.Contains(msg, "could not establish a verified identity") {
			t.Fatalf("the refusal must be about RESOLUTION failing, not about what the caller is; got %q", msg)
		}
		// The load-bearing half: it must actively warn that a real thread can look
		// exactly like this, or the reader draws the old wrong conclusion again.
		if !strings.Contains(msg, "UNRESOLVED") || !strings.Contains(msg, "reparented") {
			t.Errorf("the refusal must say plainly that unresolved is not the same as not-a-thread, and name "+
				"the case where that bites; got %q", msg)
		}
		if strings.Contains(msg, "NO sesh thread identity") {
			t.Errorf("the old wording asserted something about the caller that sesh cannot know; got %q", msg)
		}
		// Nothing to override: offering --allow-unverified here would invite a
		// caller to "allow" an id that does not exist.
		if strings.Contains(err.Error(), "--allow-unverified") {
			t.Fatalf("there is no unverified id to allow — the override must not be offered; got %q", err)
		}
	})

	// THE H95 RULE: a remedy the caller cannot type is only half a loud error.
	// whoami has no --id, so no refusal of its may send the reader to one.
	t.Run("no refusal offers a flag whoami does not have", func(t *testing.T) {
		refusals := []error{
			whoamiGate(id, srcEnv, nil, false, nameOfStub),
			whoamiGate("", "", &unverifiedError{ThreadID: id, ThreadCwd: "/a", CallerCwd: "/b"}, false, nameOfStub),
			whoamiGate("", "", &noIdentityError{}, false, nameOfStub),
		}
		for _, err := range refusals {
			if err == nil {
				t.Fatal("expected every case to refuse")
			}
			if strings.Contains(err.Error(), "--id") {
				t.Fatalf("whoami takes no --id; a refusal naming it sends the caller to a parse error: %q", err)
			}
		}
	})

	t.Run("an unrelated error passes through untouched", func(t *testing.T) {
		boom := errors.New("daemon unreachable")
		if err := whoamiGate("", "", boom, false, nameOfStub); !errors.Is(err, boom) {
			t.Fatalf("a transport failure must not be re-phrased as an identity refusal; got %v", err)
		}
	})
}

// TestWhoamiRefusesBeforeTouchingTheDaemon covers the two argument refusals.
// Both must fire before any daemon contact, which is also what lets this test
// run with a zero config.
func TestWhoamiRefusesBeforeTouchingTheDaemon(t *testing.T) {
	t.Run("--machine is refused with the reason, not a bare parse error", func(t *testing.T) {
		err := runWhoami(config.Config{}, []string{"--machine", "macbook"})
		if err == nil {
			t.Fatal("routing whoami answers the wrong question and must be refused")
		}
		if !strings.Contains(err.Error(), "cannot be routed") || !strings.Contains(err.Error(), "its OWN pane") {
			t.Fatalf("the refusal must explain WHY routing is meaningless; got %q", err)
		}
	})

	t.Run("a positional thread id is refused and points at info", func(t *testing.T) {
		err := runWhoami(config.Config{}, []string{"1a2b3c4d"})
		if err == nil {
			t.Fatal("whoami answers who THIS process is; a thread argument must be refused")
		}
		if !strings.Contains(err.Error(), "sesh info") {
			t.Fatalf("the refusal must name the verb that does take one; got %q", err)
		}
	})
}

// TestNoIdentityErrorTextUnchanged pins the message of the error type extracted
// from the fmt.Errorf that used to sit inline, for the ~20 verbs that simply
// surface it. The extraction was for whoami's benefit and must be invisible
// everywhere else.
func TestNoIdentityErrorTextUnchanged(t *testing.T) {
	if got, want := (&noIdentityError{}).Error(),
		"not inside a sesh thread: no --id, no valid $SESH_THREAD_ID, and no thread-marked tmux pane — pass --id"; got != want {
		t.Fatalf("text drifted:\n got %q\nwant %q", got, want)
	}
	if got, want := (&noIdentityError{Flag: "--from"}).Error(),
		"not inside a sesh thread: no --from, no valid $SESH_THREAD_ID, and no thread-marked tmux pane — pass --from"; got != want {
		t.Fatalf("the per-command flag must still travel with the refusal:\n got %q\nwant %q", got, want)
	}
}

// A live turn passes the gate. This is the acceptance half of the feature: the
// worker is told its own uuid and exits 0, so `TID=$(sesh whoami) || exit 1`
// works inside a scheduled headless run for the first time.
func TestWhoamiGateAcceptsALiveTurn(t *testing.T) {
	nameOf := func(string) string { return "health-analyst" }
	const id = "5a5a655f-e24c-4126-aa4e-46aa2c344635"
	if err := whoamiGate(id, srcTurn, nil, false, nameOf); err != nil {
		t.Fatalf("a turn-derived identity must pass the gate without --allow-unverified: %v", err)
	}
	if !srcTurn.verified() {
		t.Error("srcTurn must report as verified — `sesh info --json` and the gate read the same flag")
	}
}
