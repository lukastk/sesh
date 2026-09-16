package tmux

import (
	"fmt"
	"regexp"
	"strings"
)

// Status options — _dev/STATUS_OPTIONS.md.
//
// The work tmux server's status row shows the thread owning the focused pane
// (name · agent · [tags] · archived · flag). It used to be a `#(zsh -lc
// 'sesh-current-status …')` job, which tmux re-runs about once a second per
// attached client (measured on tmux 3.6b and 3.7c with status-interval at its
// 15 s default), each run a login shell sourcing the whole rig — and on termux
// each run leaked an ssh-agent (AGENTS.local.md H108). So the owning daemon's
// maintainer now stamps the RECORD fields that row renders as PANE user
// options on every pane carrying a @sesh-thread-id marker, kept current within
// a tick, and a status format renders them with lookups alone:
//
//	#{?@sesh-name,sesh: #{@sesh-name} [#{=8:@sesh-thread-id}] · #{@sesh-agent}…,}
//
// Zero forks per redraw. The options are PANE-scoped (never window/session):
// tmux resolves user options with inheritance during format expansion, so a
// coarser scope would make an unmarked pane render its neighbour's thread —
// the trap ShellIDOption documents. Values are the record's, verbatim: an empty
// value is a real, empty option (a format conditional reads it as false), the
// booleans are "1"/"". A `#[…]` style sequence inside a thread name styles the
// status line exactly as it did through the shell job — not escaped here.
const (
	StatusNameOption         = "@sesh-name"
	StatusAgentOption        = "@sesh-agent"
	StatusTagsOption         = "@sesh-tags"          // comma-joined; "" when none
	StatusArchivedOption     = "@sesh-archived"      // "1" or ""
	StatusFlaggedOption      = "@sesh-flagged"       // "1" or ""
	StatusFlagDisabledOption = "@sesh-flag-disabled" // "1" or ""
)

// StatusOptions is every status option, in write order.
var StatusOptions = []string{
	StatusNameOption, StatusAgentOption, StatusTagsOption,
	StatusArchivedOption, StatusFlaggedOption, StatusFlagDisabledOption,
}

// PaneOptions is one pane's user-option assignment for ApplyPaneOptions: every
// Set key is written to its value, every Unset name is removed. A pane appears
// at most once per batch.
type PaneOptions struct {
	Pane  string
	Set   map[string]string
	Unset []string
}

// paneOptionsBatchBytes bounds one tmux invocation's argv. The whole command
// list travels to the server as ONE imsg (MAX_IMSGSIZE 16 KiB — the same cap
// that bit `set-buffer`, H90), so a batch is cut well below it.
const paneOptionsBatchBytes = 8 * 1024

var noSuchPaneRe = regexp.MustCompile(`no such pane:?\s*(%\d+)`)

// ApplyPaneOptions writes/removes user options on MANY panes in as few tmux
// invocations as the argv budget allows (`set-option -p -t A k v ; set-option
// … ; refresh-client -S -t c …`), then repaints every named client's status
// line in the same invocation. It returns the ids of the panes whose
// assignment was applied.
//
// Two abort semantics of a tmux command list are handled, both measured:
//   - a pane that vanished between enumeration and write makes tmux stop at
//     that sub-command with "no such pane: %N" — the panes BEFORE it are
//     applied, that pane is dropped, and the remainder is retried in a fresh
//     batch (the CapturePanes pattern);
//   - a client that detached makes the trailing `refresh-client` fail with
//     "can't find client" — every pane write precedes the refreshes, so they
//     all landed; the repaint of the remaining clients is left to the next
//     status-interval beat (a repaint is a hint, never state).
//
// Any other failure is returned loudly with what was applied so far.
func (s *Server) ApplyPaneOptions(batch []PaneOptions, refreshClients []string) (applied []string, err error) {
	remaining := append([]PaneOptions(nil), batch...)
	for len(remaining) > 0 {
		var args []string
		n, size := 0, 0
		for _, po := range remaining {
			pa := paneOptionArgs(po)
			psize := 0
			for _, a := range pa {
				psize += len(a) + 1
			}
			if n > 0 && size+psize > paneOptionsBatchBytes {
				break
			}
			if n > 0 {
				args = append(args, ";")
			}
			args = append(args, pa...)
			size += psize
			n++
		}
		chunk := remaining[:n]
		if n == len(remaining) { // the last chunk carries the repaint
			for _, c := range refreshClients {
				args = append(args, ";", "refresh-client", "-S", "-t", c)
			}
		}
		_, stderr, runErr := s.runSplit(args...)
		if runErr == nil {
			applied = append(applied, paneIDs(chunk)...)
			remaining = remaining[n:]
			continue
		}
		if failed := noSuchPane(stderr); failed != "" {
			idx := -1
			for i, po := range chunk {
				if po.Pane == failed {
					idx = i
					break
				}
			}
			if idx < 0 {
				return applied, fmt.Errorf("tmux set-option batch: %w: %s", runErr, strings.TrimSpace(stderr))
			}
			applied = append(applied, paneIDs(chunk[:idx])...)
			remaining = remaining[idx+1:] // skip the vanished pane, retry the rest
			continue
		}
		if strings.Contains(stderr, "can't find client") {
			applied = append(applied, paneIDs(chunk)...)
			remaining = remaining[n:]
			continue
		}
		return applied, fmt.Errorf("tmux set-option batch: %w: %s", runErr, strings.TrimSpace(stderr))
	}
	return applied, nil
}

// paneOptionArgs renders one pane's assignment as tmux sub-commands.
func paneOptionArgs(po PaneOptions) []string {
	var args []string
	for _, k := range sortedKeys(po.Set) {
		if len(args) > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-option", "-p", "-t", po.Pane, k, optionValueArg(po.Set[k]))
	}
	for _, k := range po.Unset {
		if len(args) > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-option", "-p", "-t", po.Pane, "-u", k)
	}
	return args
}

// optionValueArg escapes the one value tmux's argv parser would misread: a
// lone ";" element is the command separator, and `\;` is tmux's own spelling
// of a literal one (stored back as ";", verified). Any other value, "" and
// "a;b" included, travels verbatim — argv elements never pass through a shell.
func optionValueArg(v string) string {
	if v == ";" {
		return `\;`
	}
	return v
}

func paneIDs(batch []PaneOptions) []string {
	out := make([]string, 0, len(batch))
	for _, po := range batch {
		out = append(out, po.Pane)
	}
	return out
}

// noSuchPane extracts the pane id from set-option's "no such pane: %N" (empty
// for any other stderr). NB it is a DIFFERENT message from capture-pane's
// "can't find pane: %N" — the two verbs word the same condition differently.
func noSuchPane(stderr string) string {
	if m := noSuchPaneRe.FindStringSubmatch(stderr); m != nil {
		return m[1]
	}
	return ""
}

// AttachedClients is AttachedSessions plus the NAMES of every attached client,
// from the same single list-clients call — the maintainer needs both per tick
// (activity for the attachment axis, names to repaint status lines after a
// status-option write) and must not pay a second enumeration for the second.
func (s *Server) AttachedClients() (sessions map[string]int64, clients []string, err error) {
	// Activity first (a bare integer); the client name LAST (a tty path,
	// tab-free); the session name — which may contain spaces but never the
	// TAB tmux passes through verbatim — is everything in between.
	out, err := s.run("list-clients", "-F", "#{client_activity}\t#{client_session}\t#{client_name}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return map[string]int64{}, nil, nil
		}
		return nil, nil, err
	}
	sessions = map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		act, rest, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, nil, fmt.Errorf("tmux: list-clients line %q has no field separator", line)
		}
		i := strings.LastIndex(rest, "\t")
		if i < 0 {
			return nil, nil, fmt.Errorf("tmux: list-clients line %q has no client name field", line)
		}
		sess, name := rest[:i], rest[i+1:]
		n, err := parseActivity(act)
		if err != nil {
			return nil, nil, err
		}
		if n > sessions[sess] {
			sessions[sess] = n
		}
		if name != "" {
			clients = append(clients, name)
		}
	}
	return sessions, clients, nil
}
