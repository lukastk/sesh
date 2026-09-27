package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

type fakeMeshThreadClient struct {
	local []api.Thread
	mesh  api.MeshSnapshot
}

func (f fakeMeshThreadClient) ThreadList(context.Context, bool, bool) (api.ThreadListResponse, error) {
	return api.ThreadListResponse{Threads: f.local}, nil
}

func (f fakeMeshThreadClient) Mesh(context.Context) (api.MeshSnapshot, error) {
	return f.mesh, nil
}

// TestGuardEmptyIDFlag proves the empty-selector footgun guard: an --id that is
// EXPLICITLY passed but empty (e.g. `--id "$X"` with $X unset) is a loud error,
// while an OMITTED --id is fine (that is the intended current-thread inference).
func TestGuardEmptyIDFlag(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"omitted infers (no error)", []string{}, false},
		{"explicit empty errors", []string{"--id", ""}, true},
		{"explicit whitespace errors", []string{"--id", "   "}, true},
		{"explicit value is fine", []string{"--id", "abc123"}, false},
		{"id=form empty errors", []string{"--id="}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.String("id", "", "")
			if err := fs.Parse(tc.args); err != nil {
				t.Fatalf("parse: %v", err)
			}
			err := guardEmptyIDFlag(fs)
			if tc.wantErr && err == nil {
				t.Errorf("args %v: expected a loud error, got nil", tc.args)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("args %v: expected no error, got %v", tc.args, err)
			}
			if err != nil && !strings.Contains(err.Error(), "--id") {
				t.Errorf("error should name --id: %v", err)
			}
		})
	}
}

// TestGuardEmptyFlagNamed proves the guard generalizes to other selector flags
// (e.g. `hooks test --thread`).
func TestGuardEmptyFlagNamed(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.String("thread", "", "")
	if err := fs.Parse([]string{"--thread", ""}); err != nil {
		t.Fatal(err)
	}
	if err := guardEmptyFlag(fs, "thread"); err == nil {
		t.Errorf("explicit empty --thread should be a loud error")
	}

	fs2 := flag.NewFlagSet("t", flag.ContinueOnError)
	fs2.String("thread", "", "")
	if err := fs2.Parse([]string{}); err != nil {
		t.Fatal(err)
	}
	if err := guardEmptyFlag(fs2, "thread"); err != nil {
		t.Errorf("omitted --thread should be fine (synthetic default): %v", err)
	}
}

// TestIsFullUUID pins the full-uuid fast path's gate: only a canonical
// 36-char lowercase-hex uuid qualifies (it skips the whole-list prefix
// resolve — the expensive round trip on routed verbs); anything else falls
// through to prefix resolution exactly as before.
func TestIsFullUUID(t *testing.T) {
	yes := []string{
		"95276330-5abf-48e0-8793-d9da5d250446",
		"00000000-0000-0000-0000-000000000000",
	}
	no := []string{
		"",
		"95276330",                              // a prefix
		"95276330-5abf-48e0-8793-d9da5d25044",   // 35 chars
		"95276330-5abf-48e0-8793-d9da5d2504467", // 37 chars
		"95276330-5ABF-48e0-8793-d9da5d250446",  // uppercase → conservative fallthrough
		"95276330-5abf-48e0-8793_d9da5d250446",  // wrong separator
		"g5276330-5abf-48e0-8793-d9da5d250446",  // non-hex
		"952763305abf48e08793d9da5d250446",      // undashed
	}
	for _, s := range yes {
		if !isFullUUID(s) {
			t.Errorf("isFullUUID(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isFullUUID(s) {
			t.Errorf("isFullUUID(%q) = true, want false", s)
		}
	}
}

// TestResolveIDPrefixFullUUIDSkipsList proves the fast path is real: a full
// uuid resolves with NO daemon list call at all (nil client — any list attempt
// would panic), while a prefix still goes to the list (and errors loudly when
// the daemon is unreachable, as before).
func TestResolveIDPrefixFullUUIDSkipsList(t *testing.T) {
	id := "95276330-5abf-48e0-8793-d9da5d250446"
	got, err := resolveIDPrefix(nil, id)
	if err != nil || got != id {
		t.Fatalf("full uuid should resolve to itself without a list fetch: got %q, %v", got, err)
	}
}

func TestResolveMeshThreadIDValidatesFullUUID(t *testing.T) {
	remoteID := "95276330-5abf-48e0-8793-d9da5d250446"
	localID := "11111111-1111-4111-8111-111111111111"
	c := fakeMeshThreadClient{
		local: []api.Thread{{ID: localID}},
		mesh: api.MeshSnapshot{Machines: []api.MachineView{{
			Machine: "peer",
			Threads: []api.ThreadSnapshot{{Thread: api.Thread{ID: remoteID}}},
		}}},
	}

	if got, err := resolveMeshThreadID(c, config.Config{}, remoteID); err != nil || got != remoteID {
		t.Fatalf("remote full uuid = (%q, %v), want observed mesh id", got, err)
	}
	if got, err := resolveMeshThreadID(c, config.Config{}, localID); err != nil || got != localID {
		t.Fatalf("local full uuid = (%q, %v), want observed local id", got, err)
	}
	unknown := "22222222-2222-4222-8222-222222222222"
	if _, err := resolveMeshThreadID(c, config.Config{}, unknown); err == nil || !strings.Contains(err.Error(), unknown) {
		t.Fatalf("unknown full uuid must fail loudly, got %v", err)
	}
}

// TestGuardEmptyPositionalRef proves the positional twin (`sesh info ""`): an
// explicitly-supplied empty positional id is a loud error; an omitted one is fine.
func TestGuardEmptyPositionalRef(t *testing.T) {
	if err := guardEmptyPositionalRef(true, ""); err == nil {
		t.Errorf("supplied empty positional should be a loud error")
	}
	if err := guardEmptyPositionalRef(true, "   "); err == nil {
		t.Errorf("supplied whitespace positional should be a loud error")
	}
	if err := guardEmptyPositionalRef(false, ""); err != nil {
		t.Errorf("omitted positional should be fine (inference): %v", err)
	}
	if err := guardEmptyPositionalRef(true, "abc"); err != nil {
		t.Errorf("supplied non-empty positional should be fine: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PROVENANCE / no-pane corroboration (ticket d7be88ef).
//
// The incident: an agent in the boxyard-go thread (cwd ~/dev/…__boxyard-go) ran
// as a detached background job, so it had NO tmux pane; its inherited
// $SESH_THREAD_ID named the unrelated "mysetup - sesh" thread (cwd
// ~/mysetup/sesh). `sesh info` reported that thread as "the current thread"
// with no hedging, and a self-compact runner built on that answer compacted the
// victim and injected a foreign handover prompt into it.
//
// The previously-covered case was stale-env-vs-LIVE-PANE (the pane wins, drift
// noted). This is env-with-NO-pane, where there is no pane to lose to — which
// is exactly how it got through.

// realDir makes a directory UNDER base that survives canonicalDir's symlink
// resolution (t.TempDir() sits under a symlinked /tmp on macOS, so comparing an
// unresolved path against a resolved one would read as two unrelated trees —
// the very false positive the resolution exists to avoid).
//
// It takes an explicit base because t.TempDir() mints a NEW directory on every
// call: building a parent and its child from two separate t.TempDir() calls
// makes them siblings under different roots, and the containment cases then
// "fail" against perfectly correct code.
func realDir(t *testing.T, base string, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{base}, parts...)...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("evalsymlinks %s: %v", p, err)
	}
	return resolved
}

// TestCwdContradicts pins the corroboration truth table, including every case
// that must read as "no contradiction". Only a POSITIVE contradiction may
// refuse (H82's one-directional evidence rule): a false positive is a loud
// error the user can work around, a false negative is someone else's session.
func TestCwdContradicts(t *testing.T) {
	base := t.TempDir()
	root := realDir(t, base, "root")
	sub := realDir(t, base, "root", "pkg", "deep")
	other := realDir(t, base, "elsewhere")

	cases := []struct {
		name       string
		threadCwd  string
		callerCwd  string
		contradict bool
	}{
		{"identical", root, root, false},
		{"caller in a subdirectory of the thread cwd", root, sub, false},
		{"caller is a parent of the thread cwd (ambiguous, not proof)", sub, root, false},
		{"unrelated trees — the reported incident", root, other, true},
		{"no thread cwd is no evidence", "", other, false},
		{"no caller cwd is no evidence", root, "", false},
		{"both empty", "", "", false},
		{"relative thread cwd is not comparable", "some/relative", other, false},
		{"trailing slashes do not matter", root + "/", sub, false},
		{"sibling prefix is NOT containment", root, root + "-sibling", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cwdContradicts(tc.threadCwd, tc.callerCwd); got != tc.contradict {
				t.Errorf("cwdContradicts(%q, %q) = %v, want %v", tc.threadCwd, tc.callerCwd, got, tc.contradict)
			}
		})
	}
}

// TestResolveCurrentThreadProvenance is the core regression: it drives the
// inference truth table with the pane, the env and the calling directory all
// injected, and asserts BOTH the id and its provenance.
func TestResolveCurrentThreadProvenance(t *testing.T) {
	const (
		mine    = "1777a4ac-83e7-40cc-abc6-6e9c9697f497" // boxyard-go
		foreign = "093da760-ea1c-40a3-b5bb-29aa566eea7f" // "mysetup - sesh"
	)
	base := t.TempDir()
	box := realDir(t, base, "dev", "20260822_tsl6xn__boxyard-go")
	boxSub := realDir(t, base, "dev", "20260822_tsl6xn__boxyard-go", "internal")
	seshRepo := realDir(t, base, "mysetup", "sesh")
	c := fakeMeshThreadClient{local: []api.Thread{
		{ID: mine, Name: "boxyard-go", Cwd: box},
		{ID: foreign, Name: "mysetup - sesh", Cwd: seshRepo},
	}}

	t.Run("pane marker is verified and needs no corroboration", func(t *testing.T) {
		// Even standing somewhere unrelated: in a pane the marker is read from
		// the pane this process actually runs in, so cwd is irrelevant.
		id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{paneID: mine, cwd: seshRepo})
		if err != nil || id != mine || src != srcPane {
			t.Fatalf("got (%s, %s, %v), want (%s, pane, nil)", id, src, err, mine)
		}
		if !src.verified() {
			t.Error("a pane-derived id must be verified")
		}
		if len(notes) != 0 {
			t.Errorf("no notes expected, got %v", notes)
		}
	})

	t.Run("pane beats a disagreeing env and says so", func(t *testing.T) {
		id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{paneID: mine, env: foreign, cwd: box})
		if err != nil || id != mine || src != srcPane {
			t.Fatalf("got (%s, %s, %v), want (%s, pane, nil)", id, src, err, mine)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "stale") {
			t.Errorf("expected a drift note, got %v", notes)
		}
	})

	t.Run("env with no pane resolves but is flagged UNVERIFIED", func(t *testing.T) {
		// The legitimate no-pane case with nothing better available: a pane-less
		// caller whose cwd agrees. Since schema 50 a daemon-launched turn has a
		// VERIFIED source of its own (srcTurn, below), so what lands here is a
		// caller the daemon does not recognise — or one on a pre-50 daemon. It
		// must still resolve, and must still say it is unverified.
		id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{env: mine, cwd: box})
		if err != nil || id != mine || src != srcEnv {
			t.Fatalf("got (%s, %s, %v), want (%s, env, nil)", id, src, err, mine)
		}
		if src.verified() {
			t.Error("an env-derived id must NOT be verified")
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "unverified") {
			t.Errorf("expected an unverified note, got %v", notes)
		}
	})

	t.Run("env with no pane, caller in a subdirectory, still resolves", func(t *testing.T) {
		id, _, _, err := resolveCurrentThreadFrom(c, currentInputs{env: mine, cwd: boxSub})
		if err != nil || id != mine {
			t.Fatalf("a subdirectory of the thread cwd must corroborate: got (%s, %v)", id, err)
		}
	})

	t.Run("THE INCIDENT: env names an unrelated thread and is REFUSED", func(t *testing.T) {
		id, _, _, err := resolveCurrentThreadFrom(c, currentInputs{env: foreign, cwd: box})
		if err == nil {
			t.Fatalf("resolved %s from an env id whose thread cwd is unrelated to the caller — "+
				"this is the reported bug (a self-compact then hijacked that thread)", id)
		}
		if id != "" {
			t.Errorf("a refusal must return no id, got %q", id)
		}
		var ue *unverifiedError
		if !errors.As(err, &ue) {
			t.Fatalf("want an *unverifiedError (so optional-inference callers can tell it apart from "+
				"'not in a thread'), got %T: %v", err, err)
		}
		for _, want := range []string{"093da760", "--id", "--allow-unverified"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal must name %q; got: %v", want, err)
			}
		}
	})

	t.Run("--allow-unverified overrides the refusal, loudly", func(t *testing.T) {
		id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{env: foreign, cwd: box, allowUnverified: true})
		if err != nil || id != foreign || src != srcEnv {
			t.Fatalf("got (%s, %s, %v), want (%s, env, nil)", id, src, err, foreign)
		}
		joined := strings.Join(notes, "\n")
		if !strings.Contains(joined, "--allow-unverified") || !strings.Contains(joined, "unverified") {
			t.Errorf("the override must still announce itself, got %v", notes)
		}
	})

	t.Run("a thread with no recorded cwd is no evidence — still resolves", func(t *testing.T) {
		// A virtual/grouping thread has no cwd; absence must never refuse.
		cNoCwd := fakeMeshThreadClient{local: []api.Thread{{ID: foreign, Name: "group"}}}
		if id, _, _, err := resolveCurrentThreadFrom(cNoCwd, currentInputs{env: foreign, cwd: box}); err != nil || id != foreign {
			t.Fatalf("got (%s, %v), want (%s, nil)", id, err, foreign)
		}
	})

	t.Run("nothing at all is a loud error", func(t *testing.T) {
		_, _, _, err := resolveCurrentThreadFrom(c, currentInputs{cwd: box})
		if err == nil {
			t.Fatal("expected a loud error with neither pane nor env")
		}
		var ue *unverifiedError
		if errors.As(err, &ue) {
			t.Error("'not inside a sesh thread' must NOT be an unverifiedError — thread new " +
				"treats those differently (root thread quietly vs a loud refusal)")
		}
	})

	t.Run("an env id the daemon does not know is a loud error", func(t *testing.T) {
		if _, _, _, err := resolveCurrentThreadFrom(c, currentInputs{env: "dead-thread", cwd: box}); err == nil {
			t.Fatal("expected a loud error for an unknown env id")
		}
	})
}

// TestExtractAllowUnverifiedFlag pins the pseudo-global stripping: it is
// accepted anywhere in the args (before or after the verb) and is REMOVED, so
// the subcommand flagsets — which do not declare it — never see it.
func TestExtractAllowUnverifiedFlag(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantAllow bool
		wantRest  []string
	}{
		{"absent", []string{"info"}, false, []string{"info"}},
		{"after the verb", []string{"info", "--allow-unverified"}, true, []string{"info"}},
		{"before the verb", []string{"--allow-unverified", "info"}, true, []string{"info"}},
		{"single dash", []string{"info", "-allow-unverified"}, true, []string{"info"}},
		{"among other flags", []string{"thread", "archive", "--allow-unverified", "--json"}, true, []string{"thread", "archive", "--json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allow, rest := extractAllowUnverifiedFlag(tc.args)
			if allow != tc.wantAllow {
				t.Errorf("allow = %v, want %v", allow, tc.wantAllow)
			}
			if strings.Join(rest, " ") != strings.Join(tc.wantRest, " ") {
				t.Errorf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

// A LOUD refusal has to name a remedy the caller can actually type. Most verbs
// take --id, but `sesh subscribe`/`unsubscribe` take --from and `ticket list
// --current` / `hooks test` take --thread — none of those has an --id flag at
// all, so the generic "Pass --id <thread>" sends the caller straight into
// `flag provided but not defined: -id`.
//
// This is not hypothetical. On 2026-08-27 a supervisor thread ran
// `sesh subscribe $ID` from a claude Bash call with no tmux pane and a stale
// $SESH_THREAD_ID; the refusal was correct, but its remedy named a flag the
// command does not have, and the subscriptions silently did not exist for an
// hour (the command's own >/dev/null 2>&1 hid the error too).
func TestRefusalNamesTheCommandsOwnFlag(t *testing.T) {
	const other = "aaaaaaaa-1111-2222-3333-444444444444"
	base := t.TempDir()
	theirs := realDir(t, base, "dev", "someone-else")
	here := realDir(t, base, "dev", "mine")
	c := fakeMeshThreadClient{local: []api.Thread{{ID: other, Name: "elsewhere", Cwd: theirs}}}
	in := currentInputs{env: other, cwd: here} // positively contradicted: siblings

	t.Run("default is --id", func(t *testing.T) {
		_, _, _, err := resolveCurrentThreadFrom(c, in)
		var ue *unverifiedError
		if !errors.As(err, &ue) {
			t.Fatalf("want an unverifiedError, got %v", err)
		}
		if !strings.Contains(err.Error(), "Pass --id <thread>") {
			t.Fatalf("default refusal should suggest --id:\n%s", err)
		}
	})

	t.Run("names the command's own flag", func(t *testing.T) {
		for _, flag := range []string{"--from", "--thread"} {
			withFlag := in
			withFlag.idFlag = flag
			_, _, _, err := resolveCurrentThreadFrom(c, withFlag)
			if err == nil {
				t.Fatalf("%s: want a refusal", flag)
			}
			if !strings.Contains(err.Error(), "Pass "+flag+" <thread>") {
				t.Errorf("refusal for a %s command must suggest %s, got:\n%s", flag, flag, err)
			}
			if strings.Contains(err.Error(), "--id") {
				t.Errorf("refusal for a %s command must NOT suggest --id (that flag does not exist there):\n%s", flag, err)
			}
		}
	})

	// The other refusal from the same resolver — "not inside a sesh thread" —
	// carries the same remedy and had the same bug.
	t.Run("the not-in-a-thread refusal too", func(t *testing.T) {
		empty := fakeMeshThreadClient{}
		_, _, _, err := resolveCurrentThreadFrom(empty, currentInputs{cwd: here, idFlag: "--from"})
		if err == nil {
			t.Fatal("want a refusal with no env and no pane")
		}
		if strings.Contains(err.Error(), "--id") || !strings.Contains(err.Error(), "--from") {
			t.Fatalf("not-in-a-thread refusal must name --from here, got:\n%s", err)
		}
	})
}

// TestResolveCurrentThreadTurnSource covers the THIRD source: a headless turn
// the local daemon launched.
//
// The gap it closes: a daemon-launched worker (a `schedule spawn --headless` run,
// any `send-headless` turn) has no tmux pane, so before schema 50 the only thing
// naming its thread was the inherited $SESH_THREAD_ID — which the gate refuses,
// correctly, because a detached background job carries a perfectly valid id
// belonging to unrelated work. The daemon started the turn and knows which thread
// it is for; turnOf is the worker proving it is that process.
func TestResolveCurrentThreadTurnSource(t *testing.T) {
	const (
		worker  = "5a5a655f-e24c-4126-aa4e-46aa2c344635" // the thread whose turn is running
		foreign = "093da760-ea1c-40a3-b5bb-29aa566eea7f" // a stranger's thread
	)
	base := t.TempDir()
	workerCwd := realDir(t, base, "dev", "20260917_4cr9iq__mosaic-v3")
	elsewhere := realDir(t, base, "mysetup", "sesh")
	c := fakeMeshThreadClient{local: []api.Thread{
		{ID: worker, Name: "health-analyst", Cwd: workerCwd},
		{ID: foreign, Name: "adi-requests", Cwd: elsewhere},
	}}
	turn := func(id string) func() (string, error) {
		return func() (string, error) { return id, nil }
	}

	t.Run("a live turn is a VERIFIED identity", func(t *testing.T) {
		id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{
			env: worker, cwd: workerCwd, turnOf: turn(worker),
		})
		if err != nil || id != worker || src != srcTurn {
			t.Fatalf("got (%s, %s, %v), want (%s, turn, nil)", id, src, err, worker)
		}
		if !src.verified() {
			t.Fatal("a turn-derived id must be VERIFIED: it is the whole point — the worker can now sign, attach and publish as itself")
		}
		if len(notes) != 0 {
			t.Errorf("a verified answer needs no caveat on stderr, got %v", notes)
		}
	})

	t.Run("a verified turn is NOT corroborated against the cwd", func(t *testing.T) {
		// A worker that has cd'd somewhere unrelated is still that worker. cwd
		// corroboration exists to bound an UNVERIFIED guess; applying it to a
		// verified source would refuse the one caller whose identity is certain.
		id, src, _, err := resolveCurrentThreadFrom(c, currentInputs{
			env: worker, cwd: elsewhere, turnOf: turn(worker),
		})
		if err != nil || id != worker || src != srcTurn {
			t.Fatalf("got (%s, %s, %v), want the turn accepted regardless of cwd", id, src, err)
		}
	})

	t.Run("the turn outranks a DISAGREEING env, and says so", func(t *testing.T) {
		id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{
			env: foreign, cwd: workerCwd, turnOf: turn(worker),
		})
		if err != nil || id != worker || src != srcTurn {
			t.Fatalf("got (%s, %s, %v), want the TURN to win", id, src, err)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "disagrees") {
			t.Errorf("a disagreement between the env and the real turn must be loud, got %v", notes)
		}
	})

	t.Run("the pane is checked FIRST and the daemon is not asked at all", func(t *testing.T) {
		// Not just a precedence claim: asking second is what keeps the cost off
		// the pane agent, which is the overwhelmingly common caller.
		asked := false
		id, src, _, err := resolveCurrentThreadFrom(c, currentInputs{
			paneID: foreign, cwd: elsewhere,
			turnOf: func() (string, error) { asked = true; return worker, nil },
		})
		if err != nil || id != foreign || src != srcPane {
			t.Fatalf("got (%s, %s, %v), want the pane marker", id, src, err)
		}
		if asked {
			t.Error("a marked pane resolved the identity; the daemon should not have been asked")
		}
	})

	t.Run("no turn falls through to the unverified env exactly as before", func(t *testing.T) {
		id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{
			env: worker, cwd: workerCwd, turnOf: turn(""),
		})
		if err != nil || id != worker || src != srcEnv {
			t.Fatalf("got (%s, %s, %v), want (%s, env, nil)", id, src, err, worker)
		}
		if src.verified() {
			t.Error("an env-derived id must still NOT be verified")
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "unverified") {
			t.Errorf("expected the unverified note, got %v", notes)
		}
	})

	t.Run("an unanswerable daemon is a NOTE, not a silent downgrade", func(t *testing.T) {
		// The mid-rollout case: a machine still on a pre-50 binary 404s the
		// route. The worker is refused either way, but it must be told the
		// difference between "your identity was checked and rejected" and "your
		// identity could not be checked here".
		id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{
			env: worker, cwd: workerCwd,
			turnOf: func() (string, error) { return "", errors.New("daemon: HTTP 404") },
		})
		if err != nil || id != worker || src != srcEnv {
			t.Fatalf("got (%s, %s, %v), want the old env behaviour", id, src, err)
		}
		joined := strings.Join(notes, "\n")
		if !strings.Contains(joined, "could not ask the local daemon") || !strings.Contains(joined, "404") {
			t.Errorf("the failure and its reason must both surface: %v", notes)
		}
	})

	t.Run("no env and no turn: no noise about a question nobody asked", func(t *testing.T) {
		// A plain shell with no daemon running is not a mis-identification risk,
		// so it gets the ordinary "not inside a sesh thread" and nothing else.
		_, _, notes, err := resolveCurrentThreadFrom(c, currentInputs{
			cwd: elsewhere, turnOf: func() (string, error) { return "", errors.New("connection refused") },
		})
		var none *noIdentityError
		if !errors.As(err, &none) {
			t.Fatalf("err = %v, want noIdentityError", err)
		}
		if len(notes) != 0 {
			t.Errorf("no notes expected for a caller with no identity to check, got %v", notes)
		}
	})
}

// TestResolveCurrentThreadHarnessSource covers the FOURTH source, and the case
// that produced it (2026-09-27).
//
// An agent working in a mosaic-v3 course box WAS thread 1a26989d — pane, marker
// and cwd all registered with the daemon — while its inherited $SESH_THREAD_ID
// named `adi-requests` in a different project. Its process ancestry was
// zsh <- claude <- claude <- systemd: reparented, so it reached neither the pane
// nor the pane's recorded pid, and H113's turn ancestry cannot help it either. It
// discovered who it was by capturing a pane and recognising its own prose.
//
// The harness's own session id closes that gap, because the daemon has recorded
// the same id as exactly one thread's agent_session_id. But it is a CLAIM the
// process presents rather than a fact read from its container, so the bar is
// higher: the cwd must corroborate AND the thread must still be live.
func TestResolveCurrentThreadHarnessSource(t *testing.T) {
	const (
		mine     = "1a26989d-7bb0-4d45-800e-3b48e390675b" // mosaic-finnish, the real one
		mineSess = "07998bab-96b1-40f5-a9cb-78b263772cf7" // its claude --session-id
		foreign  = "c194478c-e3be-4203-a09b-c58a63845de2" // adi-requests, what the env said
		foreSess = "aaaa1111-2222-3333-4444-555566667777"
	)
	base := t.TempDir()
	box := realDir(t, base, "dev", "mosaic-v3", "courses", "finnish")
	adi := realDir(t, base, "dev", "ADI-website")
	c := fakeMeshThreadClient{local: []api.Thread{
		{ID: mine, Name: "mosaic-finnish", Cwd: box, AgentKind: "claude", AgentSessionID: mineSess},
		{ID: foreign, Name: "adi-requests", Cwd: adi, AgentKind: "claude", AgentSessionID: foreSess},
	}}
	allLive := func(string) bool { return true }

	t.Run("THE REPORTED CASE: the harness names the real thread and the env is called out", func(t *testing.T) {
		id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{
			env: foreign, cwd: box, harnessSession: mineSess, paneLive: allLive,
		})
		if err != nil || id != mine || src != srcHarness {
			t.Fatalf("got (%s, %s, %v), want (%s, harness, nil) — this is the whole point of the source", id, src, err, mine)
		}
		if !src.verified() {
			t.Fatal("a corroborated harness session must be verified, or the agent still cannot act as itself")
		}
		// Silence here would leave the operator thinking the env var was fine.
		joined := strings.Join(notes, "\n")
		if !strings.Contains(joined, "WRONG") || !strings.Contains(joined, short8(foreign)) {
			t.Errorf("a wrong $SESH_THREAD_ID must be named as wrong: %v", notes)
		}
	})

	t.Run("a harness session whose thread is elsewhere is REFUSED, not accepted", func(t *testing.T) {
		// The safety case: if a harness ever froze its session var the way
		// $SESH_THREAD_ID gets frozen, the claim would look perfect. The cwd is
		// what catches it, and it is why corroboration is mandatory here.
		_, _, _, err := resolveCurrentThreadFrom(c, currentInputs{
			cwd: box, harnessSession: foreSess, paneLive: allLive,
		})
		var hm *harnessMismatchError
		if !errors.As(err, &hm) {
			t.Fatalf("err = %v, want a harnessMismatchError", err)
		}
		if hm.ThreadID != foreign {
			t.Errorf("the refusal must name the thread it declined to become: %+v", hm)
		}
	})

	t.Run("a conversation that is not running does not certify anything", func(t *testing.T) {
		dead := func(string) bool { return false }
		id, src, notes, err := resolveCurrentThreadFrom(c, currentInputs{
			env: mine, cwd: box, harnessSession: mineSess, paneLive: dead,
		})
		// Falls through to the env, i.e. unverified — never certified.
		if err != nil || id != mine || src != srcEnv {
			t.Fatalf("got (%s, %s, %v), want the unverified env answer", id, src, err)
		}
		if !strings.Contains(strings.Join(notes, "\n"), "no live pane") {
			t.Errorf("the reason for not certifying must be said out loud: %v", notes)
		}
	})

	t.Run("an unreachable liveness probe reads as NOT live", func(t *testing.T) {
		// nil paneLive is "cannot check". It must never read as a pass: that would
		// make the strictest half of the warrant vanish whenever the daemon hiccups.
		_, src, _, _ := resolveCurrentThreadFrom(c, currentInputs{
			env: mine, cwd: box, harnessSession: mineSess, paneLive: nil,
		})
		if src == srcHarness {
			t.Fatal("certified a harness claim without being able to check liveness")
		}
	})

	t.Run("an archived thread is not an identity", func(t *testing.T) {
		arch := fakeMeshThreadClient{local: []api.Thread{
			{ID: mine, Name: "mosaic-finnish", Cwd: box, AgentSessionID: mineSess, Archived: true},
		}}
		_, src, notes, _ := resolveCurrentThreadFrom(arch, currentInputs{
			cwd: box, harnessSession: mineSess, paneLive: allLive,
		})
		if src == srcHarness {
			t.Fatal("certified an archived thread as the caller's identity")
		}
		if !strings.Contains(strings.Join(notes, "\n"), "archived") {
			t.Errorf("want an archived note, got %v", notes)
		}
	})

	t.Run("two threads on one session id is refused loudly, not resolved", func(t *testing.T) {
		dup := fakeMeshThreadClient{local: []api.Thread{
			{ID: mine, Name: "a", Cwd: box, AgentSessionID: mineSess},
			{ID: foreign, Name: "b", Cwd: box, AgentSessionID: mineSess},
		}}
		_, _, _, err := resolveCurrentThreadFrom(dup, currentInputs{
			cwd: box, harnessSession: mineSess, paneLive: allLive,
		})
		if err == nil {
			t.Fatal("want a loud refusal: the daemon claims a session per thread, so this means the assumption is wrong")
		}
		if !strings.Contains(err.Error(), "refusing to guess") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("the pane still wins, and the harness is not consulted", func(t *testing.T) {
		id, src, _, err := resolveCurrentThreadFrom(c, currentInputs{
			paneID: foreign, cwd: adi, harnessSession: mineSess, paneLive: allLive,
		})
		if err != nil || id != foreign || src != srcPane {
			t.Fatalf("got (%s, %s, %v), want the pane marker to outrank a harness claim", id, src, err)
		}
	})
}

// whoamiLead is what a refusal offers instead of a dead end. It must narrow, and
// it must never look like an answer.
func TestWhoamiLead(t *testing.T) {
	base := t.TempDir()
	here := realDir(t, base, "dev", "box")
	other := realDir(t, base, "dev", "elsewhere")
	live := func(string) bool { return true }
	dead := func(string) bool { return false }

	one := fakeMeshThreadClient{local: []api.Thread{
		{ID: "1a26989d-1111-2222-3333-444444444444", Name: "mosaic-finnish", Cwd: here, AgentKind: "claude"},
		{ID: "cccccccc-1111-2222-3333-444444444444", Name: "elsewhere", Cwd: other},
	}}

	t.Run("one live thread in this exact directory is offered as a LEAD", func(t *testing.T) {
		got := whoamiLead(one, here, live)
		if !strings.Contains(got, "1a26989d") || !strings.Contains(got, "mosaic-finnish") {
			t.Fatalf("the lead must name the candidate: %q", got)
		}
		// The wording is the safety property: this must not read as an identity.
		if !strings.Contains(got, "LEAD, NOT your identity") {
			t.Errorf("a lead that reads like an answer is worse than no lead: %q", got)
		}
		if !strings.Contains(got, "sesh info 1a26989d") {
			t.Errorf("it must hand over a command that confirms: %q", got)
		}
		// H95: whoami has no --id, so no refusal of its may send the reader to one.
		if strings.Contains(got, "--id") {
			t.Errorf("must use the positional form, not a flag whoami lacks: %q", got)
		}
	})

	t.Run("several candidates is not a lead", func(t *testing.T) {
		many := fakeMeshThreadClient{local: []api.Thread{
			{ID: "aaaaaaaa-1111-2222-3333-444444444444", Name: "one", Cwd: here},
			{ID: "bbbbbbbb-1111-2222-3333-444444444444", Name: "two", Cwd: here},
		}}
		got := whoamiLead(many, here, live)
		if !strings.Contains(got, "do not pick one") {
			t.Fatalf("with 2 candidates the honest move is to name none: %q", got)
		}
		if !strings.Contains(got, "aaaaaaaa") || !strings.Contains(got, "bbbbbbbb") {
			t.Errorf("it should still list them so they can be checked: %q", got)
		}
	})

	t.Run("dead threads are not leads, and neither is nothing", func(t *testing.T) {
		if got := whoamiLead(one, here, dead); got != "" {
			t.Errorf("a thread with no live pane cannot be the conversation this process is having: %q", got)
		}
		if got := whoamiLead(one, realDir(t, base, "dev", "empty"), live); got != "" {
			t.Errorf("no candidates means no paragraph: %q", got)
		}
		if got := whoamiLead(one, "", live); got != "" {
			t.Errorf("no cwd means no evidence: %q", got)
		}
	})

	t.Run("archived threads are excluded", func(t *testing.T) {
		arch := fakeMeshThreadClient{local: []api.Thread{
			{ID: "dddddddd-1111-2222-3333-444444444444", Name: "old", Cwd: here, Archived: true},
		}}
		if got := whoamiLead(arch, here, live); got != "" {
			t.Errorf("an archived thread is not a candidate identity: %q", got)
		}
	})
}
