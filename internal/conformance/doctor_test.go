package conformance

// daemon.doctor cells (PARITY_ROADMAP E2): `sesh doctor` reports REAL health
// — daemon-side agent-on-$SHELL-PATH checks (the deploy-env failure class),
// config-policy parse status, and a FAIL exit on a genuinely broken state.
// LOCAL-only: the daemon checks its own environment; routing wouldn't change
// what's tested.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("daemon.doctor", matrix.AgentAgnostic, matrix.Local, testDoctor)
}

func testDoctor(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	out, stderr, err := sb.Runner.Run(t, "doctor", "--json")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stderr)
	}
	var resp api.DoctorResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	byName := map[string]api.DoctorCheck{}
	for _, c := range resp.Checks {
		byName[c.Name] = c
	}

	// The poster-child check exists and is real: pi is resolvable on the
	// daemon's $SHELL PATH (the conformance daemon inherits the dev shell, so
	// pi IS found — and the check reports its real path).
	pi, ok := byName["agent:pi"]
	if !ok {
		t.Fatalf("doctor has no agent:pi check; checks: %v", resp.Checks)
	}
	if pi.Status != "ok" || !strings.Contains(pi.Detail, "pi") {
		t.Errorf("agent:pi check = %+v, want ok with a path", pi)
	}
	// The daemon SHELL check + the daemon reachability are present.
	if byName["daemon"].Status != "ok" {
		t.Errorf("daemon check not ok: %+v", byName["daemon"])
	}
	if _, ok := byName["daemon SHELL"]; !ok {
		t.Errorf("no daemon SHELL check")
	}
	if byName["SESH_MACHINE"].Status != "ok" {
		t.Errorf("SESH_MACHINE check: %+v", byName["SESH_MACHINE"])
	}

	// A BROKEN config policy makes doctor report a FAIL line AND exit non-zero
	// (without it ever reaching the daemon — the client parses config too).
	// Use a fresh sandbox whose daemon isn't started (a broken hook config
	// would refuse the daemon anyway; here we assert the CLIENT-side fail).
	bad := newSandbox(t, matrix.Local)
	if err := os.WriteFile(filepath.Join(bad.Home, "config.toml"),
		[]byte("[[cwd_label]]\nmatch='('\nlabel='x'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err = bad.Runner.Run(t, "doctor", "--json")
	if err == nil {
		t.Errorf("doctor with a broken config exited 0 (must fail)")
	}
	var bresp api.DoctorResponse
	_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &bresp)
	sawFail := false
	for _, c := range bresp.Checks {
		if c.Name == "config:cwd_label" && c.Status == "fail" {
			sawFail = true
		}
	}
	if !sawFail {
		t.Errorf("doctor did not flag the broken cwd_label config: %v", bresp.Checks)
	}

	// API bind state is reported HONESTLY (ticket aeaca0d0: an unresolvable bind
	// host spun the background retry loop for 12 days while doctor said "ok" and
	// the mesh silently showed the machine offline). A daemon whose SESH_API_ADDR
	// can never bind must show api=fail naming the reason; a daemon actually
	// listening shows api=ok.
	apiCheck := func(sb *Sandbox) api.DoctorCheck {
		t.Helper()
		out, stderr, rerr := sb.Runner.Run(t, "doctor", "--json")
		var r api.DoctorResponse
		if uerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); uerr != nil {
			t.Fatalf("doctor decode: %v (runErr=%v)\n%s%s", uerr, rerr, out, stderr)
		}
		for _, c := range r.Checks {
			if c.Name == "api" {
				return c
			}
		}
		t.Fatalf("no api check in doctor output: %v", r.Checks)
		return api.DoctorCheck{}
	}

	unbound := newSandbox(t, matrix.Local, withAPI("api-unbindable.invalid:7878", "tok"))
	unbound.startDaemon(t)
	if c := apiCheck(unbound); c.Status != "fail" || !strings.Contains(c.Detail, "NOT BOUND") {
		t.Errorf("unbindable api addr: check = %+v, want fail/NOT BOUND", c)
	}

	bound := newSandbox(t, matrix.Local, withAPI(freePort(t), "tok"))
	bound.startDaemon(t)
	if c := apiCheck(bound); c.Status != "ok" || !strings.Contains(c.Detail, "listening") {
		t.Errorf("bound api addr: check = %+v, want ok/listening", c)
	}
}
