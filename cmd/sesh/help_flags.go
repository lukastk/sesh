package main

// Per-flag help. The registry's `usage:` line lists a command's flags; flagDocs
// explains each one, rendered as a "flags:" section by renderHelp. A meta-test
// (help_test.go) asserts flagDocs covers EXACTLY the flags in each usage line, so a
// new/renamed flag can't silently land undocumented. Descriptions are condensed
// from each command's own flagset usage strings (cmd/sesh/*.go). The `--columns`
// value list for `tui` is injected programmatically at render (tui.ValidColumnNames).
type flagDoc struct {
	name string
	desc string
}

// flagDocs: help-registry key -> ordered flag explanations (usage-line order).
var flagDocs = map[string][]flagDoc{
	"await": {
		{"--id", "thread id/prefix to await (default: the current thread; also accepts a positional id)"},
		{"--timeout", "give up after this duration (0 = no limit)"},
		{"--poll", "poll interval for checking turn completion (default 500ms)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON on completion instead of the text form"},
	},
	"backup": {
		{"--to", "backup SQLite file to write/update (required)"},
		{"--id", "back up only this thread (default: every local thread, archived included)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"copy": {
		{"--to-dir", "local: copy the transcript into this dir; exclusive with --to"},
		{"--to", "remote: ship to this peer and restore natively, claude only; exclusive with --to-dir"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"daemon status": {
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"delegate": {
		{"--agent", "agent kind for the worker: pi|claude|codex (required)"},
		{"--cwd", "working directory for the worker (default: $PWD)"},
		{"--name", "worker thread name, mostly for --keep (default: delegate-<ts>)"},
		{"--keep", "retain the worker thread instead of deleting it after the reply"},
		{"--timeout", "give up after this duration (worker is still deleted unless --keep; default 10m)"},
		{"--sandbox", "restrict the worker (codex read-only; claude default-deny; pi: refused); excludes --yolo"},
		{"--yolo", "bypass the worker's permissions, overriding [spawn] config; excludes --sandbox"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit {reply, id} as JSON instead of the text form"},
	},
	"doctor": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"hooks disable": {
		{"--name", "name of the hook to disable (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"hooks enable": {
		{"--name", "name of the hook to enable (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"hooks list": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"hooks test": {
		{"--name", "name of the hook to fire (required)"},
		{"--thread", "use this thread's real snapshot for the event (default: a synthetic event)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"import": {
		{"--from-v1", "the v1 SESH_HOME to import thread records from, e.g. ~/.sesh (required)"},
		{"--dry-run", "preview the import without writing any records"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"info": {
		{"--id", "thread id or unique prefix to describe (default: the current thread)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"master ensure": {
		{"--machines", "comma-separated machines to ensure windows for (default: self + all peers; 'self' allowed)"},
		{"--tmux-conf", "tmux config file, used only when the master server must be CREATED"},
	},
	"master up": {
		{"--machines", "comma-separated machines to build windows for (default: self + all peers; 'self' allowed)"},
		{"--tmux-conf", "tmux config file for the master server, loaded via -f instead of ~/.tmux.conf"},
	},
	"master watchers": {
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"matrix": {
		{"--artifact", "path to the conformance-run artifact to read (defaults to the standard artifact path)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"matrix grid": {
		{"--artifact", "path to the conformance-run artifact to read (defaults to the standard artifact path)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"matrix skips": {
		{"--artifact", "path to the conformance-run artifact to read (defaults to the standard artifact path)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"mesh": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit the full MeshSnapshot as JSON instead of the text form"},
	},
	"meta": {
		{"--id", "thread id/prefix to target (default: the current thread)"},
	},
	"meta get": {
		{"--id", "thread id/prefix to target (default: the current thread)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"meta list": {
		{"--id", "thread id/prefix to target (default: the current thread)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"meta set": {
		{"--id", "thread id/prefix to target (default: the current thread)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"meta unset": {
		{"--id", "thread id/prefix to target (default: the current thread)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"peer add": {
		{"--machine", "remote machine identity to register (required)"},
		{"--ssh", "ssh destination for the peer, e.g. user@host (required)"},
		{"--home", "remote SESH_HOME on the peer (required)"},
		{"--port", "ssh port for the peer (default 22)"},
		{"--binary", "path to the sesh binary on the remote machine (default: sesh)"},
		{"--tmux-socket", "remote mytmux socket name, used for tmux nav into the peer"},
		{"--codex-home", "remote SESH_CODEX_HOME (default: the peer's ~/.codex)"},
		{"--tmux-conf", "remote work tmux -f config the master's remote window starts the peer's work server with"},
		{"--api-addr", "peer daemon's TCP API addr host:port; set => reach it over HTTP instead of ssh"},
		{"--api-token", "literal bearer token for the peer's TCP API (prefer --api-token-file)"},
		{"--api-token-file", "path to a file holding the peer's TCP API bearer token"},
	},
	"peer remove": {
		{"--machine", "peer machine name to remove from the local mesh registry (required)"},
	},
	"restore": {
		{"--from", "backup SQLite file to read (required)"},
		{"--to-dir", "write <thread-id>.jsonl files into this dir; exclusive with --native"},
		{"--native", "reconstruct at the agent's real location, claude only; exclusive with --to-dir"},
		{"--id", "restore only this thread id/prefix (default: everything in the backup)"},
		{"--rewrite-cwd", "rewrite the cwd for the native path (cross-cwd recovery)"},
		{"--force", "overwrite existing files instead of skipping them"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"resume": {
		{"--id", "thread id to resume (required; also accepts a positional id)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"subscribe": {
		{"--from", "subscriber thread id/prefix that receives the turns (default: the current thread)"},
		{"--allow-cycle", "permit a delivery cycle (a circuit breaker still caps runaway loops)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"subscriptions": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"tail": {
		{"-n", "number of transcript lines to print (default 20)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"thread adopt": {
		{"--name", "thread name for the adopted agent (required)"},
		{"--pane", "tmux pane id on the work server to adopt (default: $TMUX_PANE; ignored for headless adopt)"},
		{"--session-id", "agent conversation id; supplied when it can't be auto-detected (e.g. a claude launched with a bare -r); REQUIRED for headless adopt"},
		{"--agent", "agent kind (claude|codex|pi) — selects HEADLESS adopt: register an existing, not-running conversation as a headless thread (no pane used)"},
		{"--cwd", "headless adopt working directory, relative or ~ ok (default: the current dir '.')"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"thread archive": {
		{"--id", "thread id or unique prefix (required)"},
		{"--unarchive", "unarchive (restore) the thread instead of archiving it"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"thread capture": {
		{"--id", "thread id or unique prefix (default: the current thread)"},
		{"--lines", "lines to capture (0 = the visible area only; default 50)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"thread delete": {
		{"--id", "thread id or unique prefix (required; never inferred for this destructive verb)"},
		{"--force", "delete even if the runtime is live (orphans the agent)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"thread grid": {
		{"--archived", "include archived threads"},
		{"--all-machines", "fan out across the mesh (this machine + peers)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit JSON, one row per line, instead of the text form"},
	},
	"thread headful": {
		{"--id", "thread id or unique prefix (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"thread headless-reply": {
		{"--id", "thread id or unique prefix (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"thread info": {
		{"--id", "thread id or unique prefix (default: the current thread)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"thread list": {
		{"--archived", "include archived (parked) threads"},
		{"--all-machines", "fan out across the mesh (this machine + peers)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"thread new": {
		{"--agent", "agent to spawn: claude | codex | pi (required)"},
		{"--cwd", "start directory; a relative path expands against the invocation dir, a ~/… path resolves against the OWNER machine's home (portable cross-machine) (default: the current dir '.')"},
		{"--name", "thread name (optional; empty = a nameless thread)"},
		{"--model", "agent model to pin to the thread (opaque pass-through, e.g. haiku | anthropic/claude-opus-4-8 | gpt-5.5; empty = the agent's default; applied on spawn, resume, and every headless turn)"},
		{"--headless", "spawn headless (a durable conversation with no tmux window)"},
		{"--parent", "parent thread id/prefix (default: the CURRENT thread when run inside one)"},
		{"--no-parent", "force a root thread, suppressing parent inference"},
		{"--fork-from", "branch this thread's conversation from a source thread; agent/cwd default to the source's"},
		{"--message-id", "fork after the Nth assistant turn (0 = the whole conversation)"},
		{"--yolo", "launch with permissions bypassed, overriding [spawn] config"},
		{"--sandbox", "launch restricted (codex read-only; claude default-deny headless; pi: refused)"},
		{"--msg", "send this initial prompt once the agent is ready (headed spawns)"},
		{"--into-session", "place the thread as a new WINDOW of an existing session (shared session)"},
		{"--into-window", "place the thread as a SPLIT pane of target (a pane id or session:window)"},
		{"--into-pane", "bind the thread to an EXISTING shell pane and run the agent in place (register-then-exec; pair with --exec)"},
		{"--exec", "with --into-pane: replace THIS process with the agent, running it in the calling pane"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"thread notify": {
		{"--on", "enable the thread's notification gate (mutually exclusive with --off)"},
		{"--off", "disable the thread's notification gate (mutually exclusive with --on)"},
		{"--id", "thread id or unique prefix (default: the current thread)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"thread pane": {
		{"--id", "thread id or unique prefix (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"thread rename": {
		{"--id", "thread id or unique prefix (required)"},
		{"--name", "the new thread name (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"thread reparent": {
		{"--parent", "new parent thread id/prefix (mutually exclusive with --root)"},
		{"--root", "make the thread a root with no parent (mutually exclusive with --parent)"},
		{"--id", "thread id or unique prefix (default: the current thread)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"thread resume": {
		{"--id", "thread id or unique prefix (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"thread send": {
		{"--id", "thread id or unique prefix (required)"},
		{"--text", "message text to send into the pane (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"thread send-headless": {
		{"--id", "thread id or unique prefix (required)"},
		{"--text", "turn text for the headless --resume turn (required)"},
		{"--model", "override the thread's pinned model for THIS turn only (opaque pass-through; empty = the thread's model / agent default)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"thread snapshot": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit JSON, one row per line, instead of the text form"},
	},
	"thread status": {
		{"--id", "thread id or unique prefix (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"thread stop": {
		{"--id", "thread id or unique prefix (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"thread tag": {
		{"--id", "thread id or unique prefix (required)"},
		{"--add", "a tag to add (repeatable)"},
		{"--remove", "a tag to remove (repeatable)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"thread transcript": {
		{"--id", "thread id or unique prefix (default: the current thread)"},
		{"--tail", "only the last N lines (default: all)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit JSON (lines + last_reply + reply_count) instead of the text form"},
	},
	"ticket create": {
		{"--name", "name for the new ticket (required)"},
		{"--prompt", "prompt text for the ticket (the work instructions)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"ticket delete": {
		{"--id", "ticket id to delete (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"ticket get": {
		{"--id", "ticket id to fetch (required)"},
		{"--field", "print one field raw: id|name|prompt|status|thread|created|closed|notes"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"ticket find": {
		{"--id", "ticket id to resolve across the mesh (required)"},
		{"--local", "resolve only against this daemon's store (no mesh fan-out; the leaf form)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"ticket list": {
		{"--thread", "filter to tickets bound to this thread id"},
		{"--current", "auto-detect the current thread and list its tickets"},
		{"--all-machines", "fan out across the mesh (this machine + peers); emits machine + thread name per ticket"},
		{"--local", "with --all-machines: only this daemon's tickets (the leaf form the fan-out calls on each peer)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"blob add": {
		{"--name", "stored filename (default: the basename of <path>; required with --stdin)"},
		{"--stdin", "read the bytes from stdin instead of a file path (requires --name)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the @blob token + summary"},
	},
	"blob ls": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON (one blob per line)"},
	},
	"fs list": {
		{"--path", "the home-rooted path to list (e.g. ~/dev or ~/mysetup); a path outside home is refused"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON"},
	},
	"plugins list": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON"},
	},
	"plugins run": {
		{"--field", "an action capability's field value as key=value, passed as ARGV (repeatable)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON"},
	},
	"blob get": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"blob rm": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"blob path": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"blob expand": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"ticket move": {
		{"--id", "ticket id/prefix to move (required)"},
		{"--to", "destination machine the ticket relocates to (required)"},
		{"--from", "source machine the ticket currently lives on (default: this machine)"},
	},
	"ticket import": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--id", "(in the piped `ticket get` half) the ticket to read; import itself reads the record from stdin"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"ticket unbind": {
		{"--id", "ticket id to detach from its thread (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"ticket needs-input": {
		{"--id", "ticket id to flag as needing input (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"ticket send-prompt": {
		{"--id", "ticket id whose prompt to send to its bound thread (required)"},
		{"--prepend", "prepend the ticket's name + id to the delivered prompt (overrides the config default)"},
		{"--no-prepend", "do NOT prepend the ticket's name + id (overrides the [ticket] send_prepend config default)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"ticket set": {
		{"--id", "ticket id to update (required)"},
		{"--name", "set the ticket name"},
		{"--prompt", "set the ticket prompt"},
		{"--notes", "REPLACE the ticket notes (mutually exclusive with --append-note)"},
		{"--append-note", "append text to the ticket notes (blank-line separated)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"ticket set-status": {
		{"--id", "ticket id to update (required)"},
		{"--status", "new status: triage|ready|active|done|dropped (required)"},
		{"--thread", "thread to bind the ticket to (required when setting status to active)"},
		{"--note", "append a note to the ticket as part of this status change (e.g. what was done, on close)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"tmux create-pane": {
		{"--target", "tmux target (session/window/pane) to split into a new pane (required)"},
		{"--dir", "start directory for the new pane"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"tmux create-session": {
		{"--name", "name of the tmux session to create (required)"},
		{"--dir", "start directory for the new session (not ~-expanded)"},
		{"--env", "environment variable KEY=VALUE for the session; repeatable"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"tmux kill-session": {
		{"--target", "name of the tmux session to kill (required)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"tmux current": {
		{"--json", "emit machine-readable JSON instead of the text form"},
	},
	"tmux info": {
		{"--session", "filter the listing to a single tmux session by name"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"tmux master-current": {
		{"--origin", "the master's machine whose marker to read (required)"},
		{"--session", "print the current SESSION name instead of the thread id (for the prefix+a picker)"},
		{"--json", "emit the full {thread_id, session, window} (the prefix+L 'last window' recorder)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"tmux nav": {
		{"--to", "target as <machine>:<session> (required unless --last)"},
		{"--thread", "thread id whose @sesh-thread-id pane window to land on (omit for plain session nav)"},
		{"--last", "switch to the previous sesh-nav location — the cross-machine 'last window' toggle (ignores --to)"},
		{"--in-client", "switch the CURRENT tmux client to the target (local target on the current work socket; no master)"},
		{"--client", "with --in-client: the exact tmux client to switch; required when stdin is not a tty"},
		{"--attach", "ATTACH this terminal to the target thread (from a plain shell outside tmux); replaces the process"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"tmux send-text": {
		{"--target", "tmux target pane to send the text to (required)"},
		{"--text", "literal text to send to the pane (required)"},
		{"--enter", "send a trailing Enter after the text"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"tmux stage-file": {
		{"--to", "machine to stage the file onto (required)"},
		{"--stdin", "read the file bytes from stdin (used internally for remote staging)"},
		{"--name", "staged file name; required when using --stdin"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"transcript": {
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"tui": {
		{"--all-machines", "show threads from every machine in the mesh (also defaults on via [tui] all_machines in config.toml)"},
		{"--cursor", "start with the cursor on the current pane's thread ($SESH_TUI_PANE from a popup binding, else $TMUX_PANE)"},
		{"--filter", "start in filter mode (type-to-narrow immediately)"},
		{"--expand", "start with tree nodes expanded (default from [tui] expand_children in config.toml)"},
		{"--columns", "comma-separated visible columns (default from [tui] columns in config.toml)"},
		{"--editor", "editor for in-TUI ticket field edits (default: [tui] editor, then $EDITOR)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
	"unsubscribe": {
		{"--from", "subscriber thread id/prefix whose subscription edge is removed (default: the current thread)"},
		{"--machine", "route this command to machine <m> over the mesh (instead of the local daemon)"},
	},
}
