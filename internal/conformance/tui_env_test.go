package conformance

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("env-column", claimEnvColumn)
	registerTUIClaim("env-manager", claimEnvManager)
}

// claimEnvColumn: the ENV column shows the count on a REAL record and follows
// `env set` / `env clear` on the daemon.
func claimEnvColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	with := newThreadWithEnv(t, sb, "pi", "with-vars", true, "SESHT_A=1", "SESHT_B=2")
	sb.newHeadlessThread(t, "pi", "without-vars")

	cols, err := tui.ResolveColumns([]string{"name", "env", "tags"})
	if err != nil {
		t.Fatal(err)
	}
	m := tui.New(sb.Home+"/daemon.sock", false).WithColumns(cols)
	m, view := renderUntilRowView(t, m, "without-vars")
	if !strings.Contains(view, "ENV") {
		t.Fatalf("no ENV header:\n%s", view)
	}
	if l := lineWith(view, "with-vars"); !strings.Contains(l, "2") {
		t.Errorf("the count is not shown: %q", l)
	}
	if l := lineWith(view, "without-vars"); strings.ContainsAny(l, "0123456789") {
		t.Errorf("a thread without variables shows a count: %q", l)
	}
	runEnvChange(t, sb, "env", "clear", "--id", with.ID, "--json")
	if !waitUntil(30*time.Second, func() bool {
		m, view = renderUntilRowView(t, m, "with-vars")
		return !strings.ContainsAny(lineWith(view, "with-vars"), "0123456789")
	}) {
		t.Fatalf("the count survived a clear:\n%s", view)
	}
}

// envDrive sends one key and runs everything it starts, batches included: a change
// answers with a refetch of the variables and of the grid.
func envDrive(t *testing.T, m tui.Model, msg tea.Msg) tui.Model {
	t.Helper()
	nm, cmd := m.Update(msg)
	m = nm.(tui.Model)
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		out := c()
		if batch, ok := out.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		nm, next := m.Update(out)
		m = nm.(tui.Model)
		// A change's follow-up is the refetch. Anything else may be the grid's
		// polling tick, which never ends.
		if fmt.Sprintf("%T", out) == "tui.envChangedMsg" {
			queue = append(queue, next)
		}
	}
	return m
}

func envKeyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// claimEnvManager drives the manager against a real daemon: what shows is what the
// owner holds, and what the keys change is what `env show` then reports.
func claimEnvManager(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := newThreadWithEnv(t, sb, "pi", "managed", true, "SESHT_KEPT=hunter2")

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "managed")
	m = sendMsg(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m = selectRowByName(t, m, "managed")
	m = runCommand(t, m, "env-edit")
	if !m.EnvOpen() {
		t.Fatal("env-edit did not open the manager")
	}
	v := m.View()
	if !strings.Contains(v, "thread env · managed") || !strings.Contains(v, "SESHT_KEPT") {
		t.Fatalf("the manager does not list the REAL variable:\n%s", v)
	}
	if strings.Contains(v, "hunter2") {
		t.Fatalf("a value shows before it is revealed:\n%s", v)
	}
	if m = runKey(t, m, "v"); !strings.Contains(m.View(), "hunter2") {
		t.Fatalf("`v` did not reveal the value:\n%s", m.View())
	}
	m = runKey(t, m, "v")

	// Add.
	m = runKey(t, m, "a")
	m = envDrive(t, m, envKeyRunes("SESHT_ADDED=one"))
	m = envDrive(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m = envDrive(t, m, envKeyRunes("word"))
	m = envDrive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	wantEnv(t, "after add", showEnv(t, sb, th.ID), map[string]string{"SESHT_KEPT": "hunter2", "SESHT_ADDED": "one word"})
	if v := m.View(); !strings.Contains(v, "SESHT_ADDED") || !strings.Contains(v, "next-launch") {
		t.Fatalf("the list or the owner's answer is not shown:\n%s", v)
	}

	// Edit the selected value (SESHT_ADDED sorts first).
	m = runKey(t, m, "e")
	m = envDrive(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = envDrive(t, m, envKeyRunes("edited"))
	m = envDrive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	wantEnv(t, "after edit", showEnv(t, sb, th.ID), map[string]string{"SESHT_KEPT": "hunter2", "SESHT_ADDED": "edited"})

	// A bad name is refused by the owner, and said.
	m = runKey(t, m, "a")
	m = envDrive(t, m, envKeyRunes("SESH_THREAD_ID=x"))
	m = envDrive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.View(), "reserved") {
		t.Fatalf("the refusal is not shown:\n%s", m.View())
	}

	// Delete takes a second press: any other key in between cancels it.
	m = runKey(t, m, "d")
	wantEnv(t, "one press deletes nothing", showEnv(t, sb, th.ID), map[string]string{"SESHT_KEPT": "hunter2", "SESHT_ADDED": "edited"})
	m = runKey(t, m, "v")
	m = runKey(t, m, "d")
	wantEnv(t, "an interrupted press deletes nothing", showEnv(t, sb, th.ID), map[string]string{"SESHT_KEPT": "hunter2", "SESHT_ADDED": "edited"})
	m = envDrive(t, m, envKeyRunes("d"))
	wantEnv(t, "after delete", showEnv(t, sb, th.ID), map[string]string{"SESHT_KEPT": "hunter2"})

	if m = runKey(t, m, "q"); m.EnvOpen() {
		t.Fatal("q did not close the manager")
	}
}
