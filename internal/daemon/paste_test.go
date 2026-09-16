package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// pasteFixture is a real tmux server with a `cat` pane (it echoes what it is
// sent, so a delivery is observable in capture-pane), a REAL nested client
// attached to that session from a driver pane (keystrokes into the driver flow
// through the client and bump the session's client_activity — the H48 signal
// the guard reads), a store with the thread record, and a bare daemon.
type pasteFixture struct {
	t    *testing.T
	sock string
	st   *store.Store
	d    *Daemon
	th   api.Thread
	pane string
}

func newPasteFixture(t *testing.T) *pasteFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	sock := "seshpaste-" + strings.ReplaceAll(t.Name(), "/", "_")
	f := &pasteFixture{t: t, sock: sock, st: st}
	// The pane runs `cat` under the argv0 "pi" (a symlink — a shebang script
	// would read as "sh"), so the runtime resolver sees a live pi of the right
	// kind: the scheduler's state read, like the maintainer's, requires an
	// agent process under the marked pane, not just the pane.
	catBin, err := exec.LookPath("cat")
	if err != nil {
		t.Fatal(err)
	}
	fakePi := filepath.Join(t.TempDir(), "pi")
	if err := os.Symlink(catBin, fakePi); err != nil {
		t.Fatal(err)
	}
	// stdout to /dev/null: the tty echo alone shows what arrived, once (cat's
	// own output would print every line a second time).
	if _, err := f.raw("-f", "/dev/null", "new-session", "-d", "-s", "watched", "-x", "80", "-y", "24", fakePi+" >/dev/null"); err != nil {
		t.Fatalf("new-session watched: %v", err)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() }) //nolint:errcheck
	if _, err := f.raw("-f", "/dev/null", "new-session", "-d", "-s", "drv", "-x", "80", "-y", "24"); err != nil {
		t.Fatalf("new-session drv: %v", err)
	}
	pane, err := f.raw("list-panes", "-t", "watched", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id: %v", err)
	}
	f.pane = pane
	f.th = api.Thread{ID: "tid-paste-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")), Machine: "test", SessionName: "watched", AgentKind: "pi", Name: "target", Cwd: "/tmp"}
	if err := st.InsertThread(f.th); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	if out, err := f.raw("set-option", "-p", "-t", pane, tmux.ThreadIDOption, f.th.ID); err != nil {
		t.Fatalf("mark pane: %v %s", err, out)
	}
	f.d = &Daemon{store: st, tmux: tmux.NewServer(sock), pasteStop: make(chan struct{})}
	t.Cleanup(f.d.stopPastes) // wake any drainer before the server/store go away
	// Attach a real nested client to `watched` from the driver pane.
	if _, err := f.raw("send-keys", "-t", "drv", "-l", fmt.Sprintf("env -u TMUX tmux -L %s attach -t watched", sock)); err != nil {
		t.Fatalf("type attach: %v", err)
	}
	if _, err := f.raw("send-keys", "-t", "drv", "Enter"); err != nil {
		t.Fatalf("submit attach: %v", err)
	}
	if !eventuallyD(5*time.Second, func() bool {
		acts, err := f.d.tmux.AttachedSessions()
		_, ok := acts["watched"]
		return err == nil && ok
	}) {
		t.Fatalf("nested client never attached to watched")
	}
	return f
}

func (f *pasteFixture) raw(args ...string) (string, error) {
	out, err := exec.Command("tmux", append([]string{"-L", f.sock}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// typeThrough sends one keystroke THROUGH the nested client (bumps activity).
func (f *pasteFixture) typeThrough() {
	f.t.Helper()
	if _, err := f.raw("send-keys", "-t", "drv", "-l", "x"); err != nil {
		f.t.Fatalf("type through client: %v", err)
	}
}

// captured returns the watched pane's text.
func (f *pasteFixture) captured() string {
	out, err := f.raw("capture-pane", "-p", "-t", f.pane)
	if err != nil {
		f.t.Fatalf("capture-pane: %v", err)
	}
	return out
}

func (f *pasteFixture) request(text string, g pasteGuard) pasteRequest {
	return pasteRequest{
		thread: f.th, text: text, guard: g, sender: "test",
		resolve: func() (string, string, bool, error) {
			loc, found, err := f.d.tmux.FindPaneByThreadID(f.th.ID)
			if err != nil || !found {
				return "", "", false, err
			}
			return loc.Pane, loc.Session, true, nil
		},
	}
}

func eventuallyD(d time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if fn() {
			return true
		}
		if time.Now().After(deadline) {
			return fn()
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestPasteGuardSkipRefusesWhileTyping: with a viewer who just typed, the skip
// policy refuses loudly and nothing reaches the pane; with the guard OFF the
// same paste lands at once.
func TestPasteGuardSkipRefusesWhileTyping(t *testing.T) {
	f := newPasteFixture(t)
	f.typeThrough()
	time.Sleep(200 * time.Millisecond)
	_, err := f.d.pasteNow(f.request("SKIPPED-ONE", pasteGuard{Window: 30 * time.Second, Deadline: time.Minute}))
	var typing errTyping
	if !errors.As(err, &typing) {
		t.Fatalf("skip policy with fresh input: want errTyping, got %v", err)
	}
	if strings.Contains(f.captured(), "SKIPPED-ONE") {
		t.Fatalf("refused paste reached the pane:\n%s", f.captured())
	}
	out, err := f.d.pasteNow(f.request("GUARD-OFF", pasteGuard{Window: 0}))
	if err != nil || !out.Sent {
		t.Fatalf("guard off: want sent, got %+v %v", out, err)
	}
	if !eventuallyD(3*time.Second, func() bool { return strings.Contains(f.captured(), "GUARD-OFF") }) {
		t.Fatalf("guard-off paste never reached the pane:\n%s", f.captured())
	}
}

// TestPasteGuardDefersThenDelivers: the defer policy holds a delivery while the
// viewer is active and lands it — in order — once the pane has been quiet for
// the window, with no typing between.
func TestPasteGuardDefersThenDelivers(t *testing.T) {
	f := newPasteFixture(t)
	f.typeThrough()
	time.Sleep(200 * time.Millisecond)
	g := pasteGuard{Window: 2 * time.Second, Deadline: 30 * time.Second}
	first, err := f.d.pasteDefer(f.request("HELD-A", g))
	if err != nil || !first.Deferred || first.Sent {
		t.Fatalf("want deferred, got %+v %v", first, err)
	}
	second, err := f.d.pasteDefer(f.request("HELD-B", g))
	if err != nil || !second.Deferred {
		t.Fatalf("second: want deferred, got %+v %v", second, err)
	}
	if n := f.d.pendingPastes(f.th.ID); n != 2 {
		t.Fatalf("pending = %d, want 2", n)
	}
	if strings.Contains(f.captured(), "HELD-") {
		t.Fatalf("a held paste reached the pane early:\n%s", f.captured())
	}
	if !eventuallyD(10*time.Second, func() bool {
		c := f.captured()
		return strings.Contains(c, "HELD-A") && strings.Contains(c, "HELD-B")
	}) {
		t.Fatalf("held pastes never delivered after the quiet window:\n%s", f.captured())
	}
	c := f.captured()
	if strings.Index(c, "HELD-A") > strings.Index(c, "HELD-B") {
		t.Fatalf("deliveries out of order (FIFO broken):\n%s", c)
	}
	if n := f.d.pendingPastes(f.th.ID); n != 0 {
		t.Fatalf("pending after delivery = %d, want 0", n)
	}
	held, delivered, flagged := f.d.pasteQueueStats()
	if held != 2 || delivered != 2 || flagged != 0 {
		t.Fatalf("stats held=%d delivered=%d flagged=%d", held, delivered, flagged)
	}
}

// TestPasteGuardDeadlineFlags: a viewer who keeps typing past the deadline
// makes the held delivery FAIL — nothing pasted — and the thread is flagged
// with the undelivered message as the reason.
func TestPasteGuardDeadlineFlags(t *testing.T) {
	f := newPasteFixture(t)
	f.typeThrough()
	time.Sleep(200 * time.Millisecond)
	g := pasteGuard{Window: 3 * time.Second, Deadline: 4 * time.Second}
	out, err := f.d.pasteDefer(f.request("NEVER-LANDS", g))
	if err != nil || !out.Deferred {
		t.Fatalf("want deferred, got %+v %v", out, err)
	}
	// Keep the viewer busy past the deadline.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(700 * time.Millisecond):
				f.raw("send-keys", "-t", "drv", "-l", "y") //nolint:errcheck
			}
		}
	}()
	if !eventuallyD(15*time.Second, func() bool {
		th, err := f.st.GetThread(f.th.ID)
		return err == nil && th.Flagged
	}) {
		close(stop)
		t.Fatalf("thread never flagged after the deadline")
	}
	close(stop)
	th, _ := f.st.GetThread(f.th.ID)
	if !strings.Contains(th.FlagReason, "undelivered message from test") {
		t.Fatalf("flag reason = %q, want the undelivered-message reason", th.FlagReason)
	}
	if strings.Contains(f.captured(), "NEVER-LANDS") {
		t.Fatalf("the deadline-failed paste reached the pane anyway (the collision):\n%s", f.captured())
	}
	if _, _, flagged := f.d.pasteQueueStats(); flagged != 1 {
		t.Fatalf("flagged counter = %d, want 1", flagged)
	}
}

// TestPasteGuardWaitBudget: wait mode blocks up to its budget and, still
// typing, returns typing=true with NOTHING queued; once quiet it pastes.
func TestPasteGuardWaitBudget(t *testing.T) {
	f := newPasteFixture(t)
	f.typeThrough()
	time.Sleep(200 * time.Millisecond)
	g := pasteGuard{Window: 2 * time.Second, Deadline: time.Minute}
	start := time.Now()
	out, err := f.d.pasteWait(context.Background(), f.request("WAITED", g), 500*time.Millisecond)
	if err != nil || !out.Typing || out.Sent {
		t.Fatalf("short budget: want typing, got %+v %v", out, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("wait overran its budget: %s", time.Since(start))
	}
	if n := f.d.pendingPastes(f.th.ID); n != 0 {
		t.Fatalf("wait mode queued a delivery (%d pending) — it must not", n)
	}
	out, err = f.d.pasteWait(context.Background(), f.request("WAITED", g), 6*time.Second)
	if err != nil || !out.Sent {
		t.Fatalf("long budget: want sent once quiet, got %+v %v", out, err)
	}
	if !eventuallyD(3*time.Second, func() bool { return strings.Contains(f.captured(), "WAITED") }) {
		t.Fatalf("waited paste never reached the pane:\n%s", f.captured())
	}
}

// TestPasteGuardDetachedIsQuiet: with no client on the session there is nobody
// to collide with — the guard passes immediately whatever the window.
func TestPasteGuardDetachedIsQuiet(t *testing.T) {
	f := newPasteFixture(t)
	if _, err := f.raw("kill-session", "-t", "drv"); err != nil {
		t.Fatalf("kill drv: %v", err)
	}
	if !eventuallyD(3*time.Second, func() bool {
		acts, _ := f.d.tmux.AttachedSessions()
		_, ok := acts["watched"]
		return !ok
	}) {
		t.Fatalf("watched still attached after killing the driver")
	}
	out, err := f.d.pasteNow(f.request("DETACHED", pasteGuard{Window: time.Hour, Deadline: time.Hour}))
	if err != nil || !out.Sent {
		t.Fatalf("detached: want sent, got %+v %v", out, err)
	}
}

// TestPasteGuardFor: request overrides beat the [send] config; nil means config.
func TestPasteGuardFor(t *testing.T) {
	d := &Daemon{}
	d.send.RespectTyping = 60 * time.Second
	d.send.RespectTypingDeadline = 10 * time.Minute
	if g := d.pasteGuardFor(nil, nil); g.Window != 60*time.Second || g.Deadline != 10*time.Minute {
		t.Fatalf("nil overrides: %+v", g)
	}
	zero, five := 0, 5000
	if g := d.pasteGuardFor(&zero, &five); g.Window != 0 || g.Deadline != 5*time.Second {
		t.Fatalf("explicit overrides: %+v", g)
	}
	if g := d.pasteGuardFor(nil, &zero); g.Deadline != 10*time.Minute {
		t.Fatalf("a zero deadline must fall back to config, got %+v", g)
	}
}
