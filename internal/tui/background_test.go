package tui

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestBackgroundCell pins the BG column (issue #24) and api.BackgroundState's
// one-line summary: nil (not reported) and a positive empty report both render
// blank in the column, but the summary tells them apart.
func TestBackgroundCell(t *testing.T) {
	shell := api.BackgroundTask{ID: "b1", Type: "shell", Description: "npm run dev"}
	mon := api.BackgroundTask{ID: "b2", Type: "monitor", Description: "tail -f log"}
	wake := api.BackgroundWakeup{ID: "c1", Schedule: "*/20 * * * *", Recurring: true}
	for _, tc := range []struct {
		name          string
		bg            *api.BackgroundState
		cell, summary string
	}{
		{"not reported", nil, "", "not reported"},
		{"none", &api.BackgroundState{}, "", "none"},
		{"one shell", &api.BackgroundState{Tasks: []api.BackgroundTask{shell}}, "1 shell", "1 shell: npm run dev"},
		{"two tasks", &api.BackgroundState{Tasks: []api.BackgroundTask{shell, mon}}, "2 tasks", "2 tasks (shell, monitor)"},
		{"wake only", &api.BackgroundState{Wakeups: []api.BackgroundWakeup{wake}}, "wake", "1 wake-up (*/20 * * * *)"},
		{"shell + wake", &api.BackgroundState{Tasks: []api.BackgroundTask{shell}, Wakeups: []api.BackgroundWakeup{wake, wake}}, "1 shell +w", "1 shell: npm run dev + 2 wake-ups"},
	} {
		if got := backgroundCell(tc.bg); got != tc.cell {
			t.Errorf("%s: cell %q, want %q", tc.name, got, tc.cell)
		}
		if got := tc.bg.Summary(); got != tc.summary {
			t.Errorf("%s: summary %q, want %q", tc.name, got, tc.summary)
		}
	}
}
