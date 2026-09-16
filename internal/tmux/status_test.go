package tmux

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// statusTestServer starts an isolated tmux server with one session of three
// panes and returns the server plus the pane ids (in creation order).
func statusTestServer(t *testing.T) (*Server, []string, func(args ...string) (string, error)) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := "seshstatus-" + strings.ReplaceAll(t.Name(), "/", "_")
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := raw("-f", "/dev/null", "new-session", "-d", "-s", "a", "-x", "80", "-y", "40", "sleep 300"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() }) //nolint:errcheck
	first, err := raw("list-panes", "-t", "a", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	panes := []string{first}
	for i := 0; i < 2; i++ {
		p, err := raw("split-window", "-d", "-t", "a", "-P", "-F", "#{pane_id}", "sleep 300")
		if err != nil {
			t.Fatalf("split-window: %v %s", err, p)
		}
		panes = append(panes, p)
	}
	return NewServer(sock), panes, raw
}

// optionListed reports whether `show-options -p` lists the option at all — the
// only way to tell an EMPTY value from an UNSET option (-v prints "" for both).
func optionListed(t *testing.T, raw func(...string) (string, error), pane, key string) bool {
	t.Helper()
	out, err := raw("show-options", "-p", "-t", pane)
	if err != nil {
		t.Fatalf("show-options -p %s: %v %s", pane, err, out)
	}
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `(\s|$)`).MatchString(out)
}

func TestApplyPaneOptionsWritesUnsetsAndSkipsVanishedPanes(t *testing.T) {
	s, panes, raw := statusTestServer(t)
	p1, p2, p3 := panes[0], panes[1], panes[2]
	value := func(pane, key string) string {
		out, err := raw("show-options", "-p", "-t", pane, "-v", key)
		if err != nil {
			t.Fatalf("show-options %s %s: %v %s", pane, key, err, out)
		}
		return out
	}

	// Writes: a plain value, an EMPTY value (must exist as an option), and the
	// one value tmux's argv parser would otherwise read as a command separator.
	applied, err := s.ApplyPaneOptions([]PaneOptions{
		{Pane: p1, Set: map[string]string{"@sesh-name": "alpha", "@sesh-tags": ""}},
		{Pane: p2, Set: map[string]string{"@sesh-name": ";"}},
		{Pane: p3, Set: map[string]string{"@sesh-name": "gamma"}},
	}, nil)
	if err != nil {
		t.Fatalf("ApplyPaneOptions: %v", err)
	}
	if strings.Join(applied, ",") != p1+","+p2+","+p3 {
		t.Fatalf("applied = %v, want all three panes in order", applied)
	}
	if got := value(p1, "@sesh-name"); got != "alpha" {
		t.Fatalf("p1 @sesh-name = %q", got)
	}
	if got := value(p1, "@sesh-tags"); got != "" || !optionListed(t, raw, p1, "@sesh-tags") {
		t.Fatalf("p1 @sesh-tags = %q listed=%v, want an existing EMPTY option", got, optionListed(t, raw, p1, "@sesh-tags"))
	}
	if got := value(p2, "@sesh-name"); got != ";" {
		t.Fatalf("p2 @sesh-name = %q, want the literal semicolon", got)
	}
	if got := value(p3, "@sesh-name"); got != "gamma" {
		t.Fatalf("p3 @sesh-name = %q", got)
	}

	// A pane that vanished before the write: tmux aborts the list AT it. The
	// panes before it are applied, it is dropped, the panes after it are
	// retried — so p3's write must land even though p2 sat between them.
	if out, err := raw("kill-pane", "-t", p2); err != nil {
		t.Fatalf("kill-pane: %v %s", err, out)
	}
	applied, err = s.ApplyPaneOptions([]PaneOptions{
		{Pane: p1, Set: map[string]string{"@sesh-name": "alpha2"}},
		{Pane: p2, Set: map[string]string{"@sesh-name": "vanished"}},
		{Pane: p3, Set: map[string]string{"@sesh-name": "gamma2"}},
	}, nil)
	if err != nil {
		t.Fatalf("ApplyPaneOptions with a vanished pane: %v", err)
	}
	if strings.Join(applied, ",") != p1+","+p3 {
		t.Fatalf("applied = %v, want %s and %s (the vanished %s dropped)", applied, p1, p3, p2)
	}
	if got := value(p3, "@sesh-name"); got != "gamma2" {
		t.Fatalf("p3 @sesh-name = %q after the vanished-pane batch, want gamma2 — the remainder was not retried", got)
	}

	// Unset removes the options (listing no longer names them).
	if _, err := s.ApplyPaneOptions([]PaneOptions{{Pane: p1, Unset: []string{"@sesh-name", "@sesh-tags"}}}, nil); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if optionListed(t, raw, p1, "@sesh-name") || optionListed(t, raw, p1, "@sesh-tags") {
		t.Fatalf("p1 still lists a status option after unset")
	}

	// A detached client in the refresh list: the writes precede the refreshes
	// in the list, so they land, and the vanished client is not an error.
	applied, err = s.ApplyPaneOptions([]PaneOptions{{Pane: p3, Set: map[string]string{"@sesh-name": "gamma3"}}}, []string{"/dev/sesh-no-such-client"})
	if err != nil {
		t.Fatalf("refresh of a vanished client must not fail the writes: %v", err)
	}
	if strings.Join(applied, ",") != p3 || value(p3, "@sesh-name") != "gamma3" {
		t.Fatalf("applied=%v value=%q after a vanished-client refresh", applied, value(p3, "@sesh-name"))
	}
}

// TestApplyPaneOptionsChunksUnderArgvBudget: a batch bigger than one imsg is
// split, and every pane still lands.
func TestApplyPaneOptionsChunksUnderArgvBudget(t *testing.T) {
	s, panes, raw := statusTestServer(t)
	big := strings.Repeat("x", 3000) // 3 panes × 3 KB > the 8 KiB batch budget
	var batch []PaneOptions
	for i, p := range panes {
		batch = append(batch, PaneOptions{Pane: p, Set: map[string]string{"@sesh-name": big + string(rune('a'+i))}})
	}
	applied, err := s.ApplyPaneOptions(batch, nil)
	if err != nil {
		t.Fatalf("ApplyPaneOptions: %v", err)
	}
	if len(applied) != 3 {
		t.Fatalf("applied %d panes, want 3", len(applied))
	}
	for i, p := range panes {
		out, err := raw("show-options", "-p", "-t", p, "-v", "@sesh-name")
		if err != nil || out != big+string(rune('a'+i)) {
			t.Fatalf("pane %s: value mismatch (err %v, len %d)", p, err, len(out))
		}
	}
}

// TestAttachedClientsNamesTheClient: the per-tick attachment probe also yields
// the attached client names (what the status repaint targets), from the one
// list-clients call.
func TestAttachedClientsNamesTheClient(t *testing.T) {
	s, _, raw := statusTestServer(t)
	sessions, clients, err := s.AttachedClients()
	if err != nil {
		t.Fatalf("AttachedClients: %v", err)
	}
	if len(sessions) != 0 || len(clients) != 0 {
		t.Fatalf("detached server: sessions=%v clients=%v, want none", sessions, clients)
	}
	// A REAL nested client attached to session a (the harness pattern).
	if out, err := raw("new-session", "-d", "-s", "viewer", "env -u TMUX tmux -L "+s.Socket()+" attach -t a"); err != nil {
		t.Fatalf("viewer: %v %s", err, out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sessions, clients, err = s.AttachedClients()
		if err != nil {
			t.Fatalf("AttachedClients: %v", err)
		}
		if len(clients) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(clients) != 1 || !strings.HasPrefix(clients[0], "/dev/") {
		t.Fatalf("clients = %v, want the one viewer tty", clients)
	}
	if _, ok := sessions["a"]; !ok {
		t.Fatalf("sessions = %v, want a attached", sessions)
	}
}
