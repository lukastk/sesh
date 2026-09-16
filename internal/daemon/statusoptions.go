package daemon

import (
	"log"
	"sort"
	"strings"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/tmux"
)

// statusFields is the record-derived tuple the maintainer stamps on a marked
// pane as tmux user options (tmux.StatusOptions): exactly the fields the work
// server's status row renders. Comparable, so "did anything change" is one
// struct compare per pane per tick.
type statusFields struct {
	name, agent, tags, archived, flagged, flagDisabled string
}

func statusFieldsOf(th api.Thread) statusFields {
	f := statusFields{name: th.Name, agent: th.AgentKind, tags: strings.Join(th.Tags, ",")}
	if th.Archived {
		f.archived = "1"
	}
	if th.Flagged {
		f.flagged = "1"
	}
	if th.FlagDisabled {
		f.flagDisabled = "1"
	}
	return f
}

func (f statusFields) options() map[string]string {
	return map[string]string{
		tmux.StatusNameOption:         f.name,
		tmux.StatusAgentOption:        f.agent,
		tmux.StatusTagsOption:         f.tags,
		tmux.StatusArchivedOption:     f.archived,
		tmux.StatusFlaggedOption:      f.flagged,
		tmux.StatusFlagDisabledOption: f.flagDisabled,
	}
}

// syncStatusOptions makes every marked pane's status options match its
// thread's record (_dev/STATUS_OPTIONS.md). Per tick it is a map walk and a
// struct compare per marked pane; tmux is invoked ONLY when something differs
// from what was last written — one batched invocation for every changed pane,
// followed by a status-line repaint of the attached clients (so a rename/flag
// shows within a tick, not on the next status-interval beat).
//
// byID is the record index (nil = no records: every cached pane is cleared),
// panes the marked-pane index, existing the set of ALL live pane ids (a cached
// pane absent from it is gone, its options with it — nothing to unset), and
// clients the attached client names to repaint. A marked pane whose thread has
// no record (a hand-stamped marker) carries no status options.
func (m *maintainer) syncStatusOptions(byID map[string]api.Thread, panes map[string]api.PaneLocator, existing map[string]bool, clients []string) {
	want := make(map[string]statusFields, len(panes))
	for tid, loc := range panes {
		if th, ok := byID[tid]; ok {
			want[loc.Pane] = statusFieldsOf(th)
		}
	}
	var batch []tmux.PaneOptions
	for pane, f := range want {
		if prev, ok := m.paneStatus[pane]; ok && prev == f {
			continue
		}
		batch = append(batch, tmux.PaneOptions{Pane: pane, Set: f.options()})
	}
	for pane := range m.paneStatus {
		if _, ok := want[pane]; ok {
			continue
		}
		if existing[pane] {
			batch = append(batch, tmux.PaneOptions{Pane: pane, Unset: tmux.StatusOptions})
		} else {
			delete(m.paneStatus, pane)
		}
	}
	if len(batch) == 0 {
		return
	}
	sort.Slice(batch, func(i, j int) bool { return batch[i].Pane < batch[j].Pane })
	applied, err := m.d.tmux.ApplyPaneOptions(batch, clients)
	for _, pane := range applied {
		if f, ok := want[pane]; ok {
			m.paneStatus[pane] = f
		} else {
			delete(m.paneStatus, pane)
		}
	}
	m.statusWrites += len(applied)
	if err != nil {
		// Whatever was not applied stays out of paneStatus, so the next tick
		// retries exactly that.
		log.Printf("maintainer: status options: %v (retrying next tick)", err)
	}
}
