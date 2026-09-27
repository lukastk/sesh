package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// startChild runs a real child process and returns its pid. Killed by RECORDED
// pid in cleanup — never by pattern, which has reached into other sessions'
// polling loops before.
func startChild(t *testing.T) int {
	t.Helper()
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Process.Kill()
		_, _ = c.Process.Wait()
	})
	return c.Process.Pid
}

// turnOwnerOf is the daemon half of a headless worker's identity: real
// processes, the real registry, the real ancestry walk. Nothing is stubbed —
// there is nothing here to stub, which is the point of choosing ancestry over a
// token.
func TestTurnOwnerOfRealProcesses(t *testing.T) {
	d := &Daemon{turnPID: map[string]int{}}
	child := startChild(t)

	t.Run("nothing in flight is a specific answer, not a bare no", func(t *testing.T) {
		id, pid, reason := d.turnOwnerOf(child)
		if id != "" || pid != 0 {
			t.Fatalf("got (%q, %d), want no identity", id, pid)
		}
		if !strings.Contains(reason, "no headless turn is in flight") {
			t.Errorf("reason = %q; a caller needs to know WHICH no this is", reason)
		}
	})

	// The test process stands in for the turn's root: the child really is inside
	// its tree, exactly as an agent's `sesh whoami` is inside the turn's.
	d.turnPID["thread-a"] = os.Getpid()

	t.Run("a process inside the turn's tree resolves to that thread", func(t *testing.T) {
		id, pid, reason := d.turnOwnerOf(child)
		if id != "thread-a" {
			t.Fatalf("got %q (reason %q), want thread-a", id, reason)
		}
		if pid != os.Getpid() {
			t.Errorf("turn pid = %d, want %d (the diagnostic must name the real root)", pid, os.Getpid())
		}
	})

	t.Run("the turn's own root process resolves too", func(t *testing.T) {
		if id, _, reason := d.turnOwnerOf(os.Getpid()); id != "thread-a" {
			t.Errorf("got %q (%q), want thread-a", id, reason)
		}
	})

	t.Run("a process OUTSIDE the tree is refused, and says why", func(t *testing.T) {
		// pid 1 is nobody's descendant. This is the detached/reparented shape —
		// the one the reported incident was in — and it must not resolve.
		id, _, reason := d.turnOwnerOf(1)
		if id != "" {
			t.Fatalf("pid 1 resolved to %q; a process outside every turn must get nothing", id)
		}
		if !strings.Contains(reason, "not inside any of the") {
			t.Errorf("reason = %q", reason)
		}
	})

	t.Run("several in-flight turns claiming one pid is REFUSED, not guessed", func(t *testing.T) {
		// Impossible by construction (the daemon starts each turn as its own
		// child, so one turn's tree cannot contain another's root) — which is
		// exactly why it must be loud if it ever happens: the assumption, not
		// the caller, would be wrong.
		d.turnPID["thread-b"] = 1 // everything descends from init
		defer delete(d.turnPID, "thread-b")
		id, _, reason := d.turnOwnerOf(child)
		if id != "" {
			t.Fatalf("resolved %q from an ambiguous tree; refusing is the only honest answer", id)
		}
		if !strings.Contains(reason, "refusing to guess") ||
			!strings.Contains(reason, "thread-a") || !strings.Contains(reason, "thread-b") {
			t.Errorf("reason must name both candidates: %q", reason)
		}
	})

	t.Run("the identity does not outlive the turn, and the reason DISCRIMINATES", func(t *testing.T) {
		// This is the observable that tells a cleared registry from a leaked one:
		// a dropped entry gives "no turn in flight", whereas an entry left behind
		// would still give "not inside any of the 1 turn(s) in flight" for the
		// same pid. The end-to-end cell cannot see the difference — every vantage
		// point that survives a turn is outside its tree and is refused either
		// way — so the discrimination lives here.
		delete(d.turnPID, "thread-a")
		id, _, reason := d.turnOwnerOf(child)
		if id != "" {
			t.Fatalf("resolved %q after the turn ended", id)
		}
		if !strings.Contains(reason, "no headless turn is in flight") {
			t.Errorf("reason = %q — an entry appears to have been left behind", reason)
		}
		d.turnPID["thread-a"] = os.Getpid() // restore for any later subtest
	})

	t.Run("an unreadable tree is a refusal carrying the read error", func(t *testing.T) {
		const impossible = 2147483646 // above any pid_max
		id, _, reason := d.turnOwnerOf(impossible)
		if id != "" {
			t.Fatalf("resolved %q for a pid that does not exist", id)
		}
		// "could not tell" must not be reported as "provably not yours".
		if !strings.Contains(reason, "could not read the process tree") {
			t.Errorf("reason = %q, want the read failure surfaced", reason)
		}
	})
}

// The endpoint: a well-formed pid is always 200 ("you are not a turn" is an
// answer, not an error), a malformed one is a loud 400.
func TestTurnIdentityHandler(t *testing.T) {
	d := &Daemon{turnPID: map[string]int{}, cfg: config.Config{Machine: "testbox"}}
	child := startChild(t)
	d.turnPID["thread-a"] = os.Getpid()

	get := func(q string) (int, api.TurnIdentityResponse) {
		t.Helper()
		rec := httptest.NewRecorder()
		d.handleTurnIdentity(rec, httptest.NewRequest(http.MethodGet, "/v1/threads/turn-identity?pid="+q, nil))
		var out api.TurnIdentityResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := get(strconv.Itoa(child))
	if code != http.StatusOK || out.ID != "thread-a" {
		t.Fatalf("child pid: code %d, id %q", code, out.ID)
	}
	if out.Schema != api.SchemaVersion || out.Machine != "testbox" {
		t.Errorf("response must carry schema + machine: %+v", out)
	}

	code, out = get("1")
	if code != http.StatusOK {
		t.Errorf("a non-turn pid is a 200 with an empty id, not an error: code %d", code)
	}
	if out.ID != "" || out.Reason == "" {
		t.Errorf("want no id and a reason, got %+v", out)
	}

	for _, bad := range []string{"", "0", "-3", "abc"} {
		if code, _ := get(bad); code != http.StatusBadRequest {
			t.Errorf("pid=%q: code %d, want 400", bad, code)
		}
	}
}
