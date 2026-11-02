package conformance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	registerTUIClaim("model-column", claimModelColumn)
}

// claimModelColumn: the MODEL column is in the DEFAULT set and renders a REAL
// thread's model from the owner: the registered model in parentheses before any
// turn, then — after a real headless turn run on a one-turn --model override —
// the model the transcript says it ran, marked `*` because it disagrees with
// the registered one. A thread with neither shows a blank cell.
func claimModelColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	out, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "codex", "--name", "pinned-thread", "--cwd", "/tmp", "--headless", "--model", "gpt-6-luna", "--json")
	if err != nil {
		t.Fatalf("thread new: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
		t.Fatal(err)
	}
	sb.newHeadlessThread(t, "codex", "bare-thread")

	cols, err := tui.ResolveColumns(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := tui.New(sb.Home+"/daemon.sock", false).WithColumns(cols)
	m, view := renderUntilRowView(t, m, "bare-thread")
	if !strings.Contains(view, "MODEL") {
		t.Fatalf("MODEL is not in the default column set:\n%s", view)
	}
	if l := lineWith(view, "pinned-thread"); !strings.Contains(l, "(gpt-6-luna)") {
		t.Errorf("a registered, not-yet-run thread should show (gpt-6-luna): %q\n%s", l, view)
	}
	if l := lineWith(view, "bare-thread"); strings.Contains(l, "gpt-") || strings.Contains(l, "(") {
		t.Errorf("a thread with no model shows one: %q", l)
	}

	sb.headlessTurnArgs(t, th.ID, "Reply with exactly the word OK and nothing else.", "--model", "gpt-6-astra")
	if !waitUntil(30*time.Second, func() bool {
		m, view = renderUntilRowView(t, m, "bare-thread")
		return strings.Contains(lineWith(view, "pinned-thread"), "gpt-6-astra*")
	}) {
		t.Fatalf("after a turn on gpt-6-astra the row never showed gpt-6-astra*:\n%s", view)
	}
}
