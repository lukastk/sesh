package daemon

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// TestMaintainerStampsStatusOptions drives the REAL maintainer against a real
// store and a real tmux pane: the marked pane carries the record's status
// options after a tick, tmux is not touched again while nothing changed, every
// record mutation lands within a tick, unstamping the pane clears them, and
// deleting the last record (the zero-thread early-out) clears them too.
func TestMaintainerStampsStatusOptions(t *testing.T) {
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
	defer st.Close()

	sock := "seshstatusm-" + strings.ReplaceAll(t.Name(), "/", "_")
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := raw("-f", "/dev/null", "new-session", "-d", "-s", "s", "-x", "80", "-y", "20", "sleep 300"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck
	pane, err := raw("list-panes", "-t", "s", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id: %v", err)
	}

	const tid = "tid-status-options"
	if err := st.InsertThread(api.Thread{ID: tid, Machine: "test", SessionName: "s", AgentKind: "pi", Name: "alpha", Tags: []string{"a", "b"}, Cwd: "/tmp"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	mark := func() {
		if out, err := raw("set-option", "-p", "-t", pane, tmux.ThreadIDOption, tid); err != nil {
			t.Fatalf("mark pane: %v %s", err, out)
		}
	}
	mark()

	d := &Daemon{store: st, tmux: tmux.NewServer(sock)}
	m := newMaintainer(d)

	value := func(key string) string {
		t.Helper()
		out, err := raw("show-options", "-p", "-t", pane, "-v", key)
		if err != nil {
			t.Fatalf("show-options %s: %v %s", key, err, out)
		}
		return out
	}
	listed := func(key string) bool {
		t.Helper()
		out, err := raw("show-options", "-p", "-t", pane)
		if err != nil {
			t.Fatalf("show-options -p: %v %s", err, out)
		}
		return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `(\s|$)`).MatchString(out)
	}
	expect := func(step string, want map[string]string) {
		t.Helper()
		for k, v := range want {
			if !listed(k) {
				t.Fatalf("%s: %s is not set on the pane at all", step, k)
			}
			if got := value(k); got != v {
				t.Fatalf("%s: %s = %q, want %q", step, k, got, v)
			}
		}
	}

	m.tick()
	expect("first tick", map[string]string{
		tmux.StatusNameOption: "alpha", tmux.StatusAgentOption: "pi", tmux.StatusTagsOption: "a,b",
		tmux.StatusArchivedOption: "", tmux.StatusFlaggedOption: "", tmux.StatusFlagDisabledOption: "",
	})
	if m.statusWrites != 1 {
		t.Fatalf("statusWrites = %d after the first tick, want 1", m.statusWrites)
	}
	// Nothing changed: no tmux write.
	m.tick()
	m.tick()
	if m.statusWrites != 1 {
		t.Fatalf("statusWrites = %d after two unchanged ticks, want still 1 (rewrote unchanged options)", m.statusWrites)
	}

	// A lone ";" is the one value tmux's argv would misread; it must round-trip.
	if err := st.RenameThread(tid, ";"); err != nil {
		t.Fatalf("RenameThread: %v", err)
	}
	m.tick()
	expect("renamed to ;", map[string]string{tmux.StatusNameOption: ";"})

	if err := st.SetThreadTags(tid, []string{"x"}); err != nil {
		t.Fatalf("SetThreadTags: %v", err)
	}
	if err := st.SetThreadFlagAction(tid, "on", "test"); err != nil {
		t.Fatalf("SetThreadFlagAction on: %v", err)
	}
	if err := st.SetThreadArchived(tid, true, time.Now().Unix()); err != nil {
		t.Fatalf("SetThreadArchived: %v", err)
	}
	m.tick()
	expect("tag+flag+archive", map[string]string{
		tmux.StatusTagsOption: "x", tmux.StatusFlaggedOption: "1", tmux.StatusArchivedOption: "1",
	})
	if err := st.SetThreadFlagAction(tid, "disable", ""); err != nil {
		t.Fatalf("SetThreadFlagAction disable: %v", err)
	}
	m.tick()
	expect("flag disabled", map[string]string{tmux.StatusFlagDisabledOption: "1"})
	if m.statusWrites != 4 {
		t.Fatalf("statusWrites = %d, want 4 (one write per changed tick)", m.statusWrites)
	}

	// Unstamping the pane (no record write — a runtime change) clears the
	// options on the still-live pane.
	if out, err := raw("set-option", "-p", "-t", pane, "-u", tmux.ThreadIDOption); err != nil {
		t.Fatalf("unmark: %v %s", err, out)
	}
	m.tick()
	for _, k := range tmux.StatusOptions {
		if listed(k) {
			t.Fatalf("after unstamping the pane, %s is still set", k)
		}
	}
	if len(m.paneStatus) != 0 {
		t.Fatalf("paneStatus still tracks %d pane(s) after the clear", len(m.paneStatus))
	}

	// Re-stamped: back within a tick.
	mark()
	m.tick()
	expect("re-stamped", map[string]string{tmux.StatusNameOption: ";", tmux.StatusAgentOption: "pi"})

	// The last record deleted: the zero-thread early-out must still clear the
	// pane's options (one final tmux walk), then stop touching tmux.
	if err := st.DeleteThread(tid); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}
	m.tick()
	for _, k := range tmux.StatusOptions {
		if listed(k) {
			t.Fatalf("after deleting the record, %s is still set on its pane", k)
		}
	}
	writes := m.statusWrites
	m.tick()
	if m.statusWrites != writes || len(m.paneStatus) != 0 {
		t.Fatalf("zero-thread ticks keep writing: statusWrites %d -> %d, cached %d", writes, m.statusWrites, len(m.paneStatus))
	}
}
