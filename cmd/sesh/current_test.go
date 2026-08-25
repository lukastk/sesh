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
		// The legitimate no-pane case: a headless turn, whose cwd is its
		// thread's. It must still work — and must still say it is unverified.
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
