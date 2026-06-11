package main

// CLI help (G6). The dispatch in main.go is hand-rolled (no flag library), so help
// is a central registry keyed by command path ("thread", "thread new", "tmux nav",
// …). `-h`/`--help` at any level, and `sesh help [cmd [sub]]`, resolve a path and
// print that node's summary, usage (with every flag), subcommands, and examples — so
// an agent can learn the whole tool from --help alone. The registry is the single
// source of help truth; a meta-test (help_test.go) fails if a dispatched command has
// no entry, so help can't silently drift behind the CLI surface.

import (
	"fmt"
	"sort"
	"strings"
)

type cmdHelp struct {
	summary  string
	usage    string
	long     string
	examples []string
}

// helpRegistry: command path → help. Top-level commands have a single-word key;
// subcommands use "<parent> <sub>".
var helpRegistry = map[string]cmdHelp{
	"daemon": {
		summary:  "per-machine daemon lifecycle (run | start | stop | restart | status)",
		usage:    "sesh daemon <run|start|stop|restart|status>",
		examples: []string{"sesh daemon start", "sesh daemon status --json"},
	},
	"daemon run": {
		summary:  "run the daemon in the foreground (the server loop that `start` spawns detached; used directly by tests)",
		usage:    "sesh daemon run",
		examples: []string{"sesh daemon run"},
	},
	"daemon start": {
		summary:  "spawn the daemon detached and wait until it answers health",
		usage:    "sesh daemon start",
		examples: []string{"sesh daemon start"},
	},
	"daemon stop": {
		summary:  "ask the running daemon to shut down and wait until its socket is gone",
		usage:    "sesh daemon stop",
		examples: []string{"sesh daemon stop"},
	},
	"daemon restart": {
		summary:  "stop then start the daemon (a not-running daemon is a legitimate restart precondition)",
		usage:    "sesh daemon restart",
		examples: []string{"sesh daemon restart"},
	},
	"daemon status": {
		summary:  "print the running daemon's status (machine, pid, version, uptime, db, socket, schema)",
		usage:    "sesh daemon status [--json]",
		examples: []string{"sesh daemon status", "sesh daemon status --json"},
	},

	"tmux": {
		summary:  "tmux orchestration layer (current | info | create-session | create-pane | send-text | stage-file | nav | master-current)",
		usage:    "sesh tmux <current|info|create-session|create-pane|send-text|stage-file|nav|master-current>",
		examples: []string{"sesh tmux current --json", "sesh tmux nav --to macbook:sesh_fix-bug"},
	},
	"tmux current": {
		summary:  "print the thread/session/pane the caller's own $TMUX pane is in (client-side; needs the caller's $TMUX)",
		usage:    "sesh tmux current [--json]",
		examples: []string{"sesh tmux current", "sesh tmux current --json"},
	},
	"tmux info": {
		summary:  "list the work server's tmux sessions as JSONL (one session object per line); routes cross-machine",
		usage:    "sesh tmux info [--session <name>] [--machine <m>]",
		examples: []string{"sesh tmux info", "sesh tmux info --machine macbook"},
	},
	"tmux create-session": {
		summary:  "create a tmux session on the work server with an optional start dir and env",
		usage:    "sesh tmux create-session --name <name> [--dir <dir>] [--env <KEY=VALUE>]... [--machine <m>]",
		examples: []string{"sesh tmux create-session --name scratch --dir ~/proj", "sesh tmux create-session --name w --env FOO=bar --env BAZ=qux"},
	},
	"tmux create-pane": {
		summary:  "split a tmux target into a new pane with an optional start dir",
		usage:    "sesh tmux create-pane --target <session/window/pane> [--dir <dir>] [--machine <m>]",
		examples: []string{"sesh tmux create-pane --target scratch --dir ~/proj"},
	},
	"tmux send-text": {
		summary:  "send literal text (optionally with a trailing Enter) to a tmux target pane",
		usage:    "sesh tmux send-text --target <pane> --text <text> [--enter] [--machine <m>]",
		examples: []string{"sesh tmux send-text --target sesh_x:0.0 --text 'echo hi' --enter"},
	},
	"tmux stage-file": {
		summary:  "stage a local file's bytes onto a machine (pipes bytes over ssh for a remote target); prints the staged path. Carve-out: stays on ssh transport",
		usage:    "sesh tmux stage-file --to <machine> [--stdin --name <name>] [<localpath>] [--machine <m>]",
		examples: []string{"sesh tmux stage-file --to macbook ./screenshot.png", "cat f | sesh tmux stage-file --to mymain --stdin --name f"},
	},
	"tmux nav": {
		summary:  "navigate to a thread's session: switch the master's outer+inner clients, or the current client (--in-client), or attach this terminal (--attach). Carve-out: stays on ssh transport",
		usage:    "sesh tmux nav --to <machine>:<session> [--in-client [--client <client>]] [--attach] [--machine <m>]",
		examples: []string{"sesh tmux nav --to mymain:sesh_fix-bug", "sesh tmux nav --to mymain:sesh_x --in-client"},
	},
	"tmux master-current": {
		summary:  "print the thread id (or --session name) the origin master's window currently shows on this work server; routes cross-machine",
		usage:    "sesh tmux master-current --origin <machine> [--session] [--machine <m>]",
		examples: []string{"sesh tmux master-current --origin mymain", "sesh tmux master-current --origin mymain --session --machine macbook"},
	},

	"thread": {
		summary:  "thread layer — coding-agent sessions (new | list | stop | delete | resume | headful | rename | tag | archive | send | send-headless | status | grid | …)",
		usage:    "sesh thread <new|list|stop|pane|capture|status|send|send-headless|headless-reply|rename|info|adopt|transcript|notify|reparent|tag|archive|delete|resume|headful|grid|snapshot>",
		examples: []string{"sesh thread new --agent claude --name fix-bug --cwd ~/proj", "sesh thread list --all-machines"},
	},
	"thread new": {
		summary:  "spawn a new headed thread (agent in a real tmux pane) or a headless thread (--headless); supports forking and parent inference",
		usage:    "sesh thread new --agent <claude|codex|pi> --name <name> --cwd <dir> [--headless] [--parent <id>] [--no-parent] [--fork-from <id>] [--message-id <n>] [--yolo|--sandbox] [--msg <text>] [--machine <m>] [--json]",
		examples: []string{"sesh thread new --agent claude --name fix-bug --cwd ~/proj", "sesh thread new --agent pi --name notes --cwd /tmp --headless"},
	},
	"thread list": {
		summary:  "list threads (id, agent, name, session, cwd); optionally archived and/or fanned out across the mesh",
		usage:    "sesh thread list [--archived] [--all-machines] [--machine <m>] [--json]",
		examples: []string{"sesh thread list", "sesh thread list --all-machines --json"},
	},
	"thread stop": {
		summary:  "end a thread's runtime (agent + tmux session) but keep the record, making it a dead, resumable thread",
		usage:    "sesh thread stop --id <id> [--machine <m>]",
		examples: []string{"sesh thread stop --id 1a2b3c4d"},
	},
	"thread pane": {
		summary:  "print a thread's live tmux pane locator (session:window pane, pid); errors if dead",
		usage:    "sesh thread pane --id <id> [--machine <m>] [--json]",
		examples: []string{"sesh thread pane --id 1a2b3c4d"},
	},
	"thread capture": {
		summary:  "print the live text of a thread's tmux pane (supervise a possibly-stalled child agent); routed cross-machine",
		usage:    "sesh thread capture [--id <id>] [--lines <n>] [--machine <m>] [--json]",
		examples: []string{"sesh thread capture --id 1a2b3c4d --lines 80"},
	},
	"thread status": {
		summary:  "print a thread's two state axes (head, busy), attachment, agent-running, needs-input, and pane",
		usage:    "sesh thread status --id <id> [--machine <m>] [--json]",
		examples: []string{"sesh thread status --id 1a2b3c4d --json"},
	},
	"thread send": {
		summary:  "send a message into a headed thread's live pane (requires a live pane; 409 otherwise)",
		usage:    "sesh thread send --id <id> --text <text> [--machine <m>]",
		examples: []string{"sesh thread send --id 1a2b3c4d --text 'run the tests'"},
	},
	"thread send-headless": {
		summary:  "start a headless turn on an idle thread (a stateless --resume turn); 409 if a pane is live or a turn is in flight",
		usage:    "sesh thread send-headless --id <id> --text <text> [--machine <m>]",
		examples: []string{"sesh thread send-headless --id 1a2b3c4d --text 'summarize the diff'"},
	},
	"thread headless-reply": {
		summary:  "poll for a headless turn's completion and print whether it's working and its reply",
		usage:    "sesh thread headless-reply --id <id> [--machine <m>] [--json]",
		examples: []string{"sesh thread headless-reply --id 1a2b3c4d --json"},
	},
	"thread rename": {
		summary:  "rename a thread",
		usage:    "sesh thread rename --id <id> --name <name> [--machine <m>]",
		examples: []string{"sesh thread rename --id 1a2b3c4d --name better-name"},
	},
	"thread info": {
		summary:  "describe one thread: record, both state axes, attachment, tmux locator, and tickets (alias of `sesh info`)",
		usage:    "sesh thread info [--id <id>] [--machine <m>] [--json]",
		examples: []string{"sesh thread info --id 1a2b3c4d"},
	},
	"thread adopt": {
		summary:  "bring a manually-launched agent (a work-server pane) under sesh management",
		usage:    "sesh thread adopt --name <name> [--pane <pane>] [--machine <m>] [--json]",
		examples: []string{"sesh thread adopt --name adopted-claude --pane %42", "sesh thread adopt --name here"},
	},
	"thread transcript": {
		summary:  "print a thread conversation's raw transcript lines (owner-side read; current thread inferred when no id)",
		usage:    "sesh thread transcript [--id <id>] [--tail <n>] [--machine <m>] [--json]",
		examples: []string{"sesh thread transcript --id 1a2b3c4d --tail 50"},
	},
	"thread notify": {
		summary:  "toggle a thread's notification gate on or off",
		usage:    "sesh thread notify (--on|--off) [--id <id>] [--machine <m>]",
		examples: []string{"sesh thread notify --on --id 1a2b3c4d", "sesh thread notify --off"},
	},
	"thread reparent": {
		summary:  "re-parent a thread to a new parent, or make it a root (--root)",
		usage:    "sesh thread reparent (--parent <id>|--root) [--id <id>] [--machine <m>]",
		examples: []string{"sesh thread reparent --id 1a2b3c4d --parent 9f8e7d6c", "sesh thread reparent --id 1a2b3c4d --root"},
	},
	"thread tag": {
		summary:  "add and/or remove tags on a thread (repeatable --add/--remove)",
		usage:    "sesh thread tag --id <id> [--add <tag>]... [--remove <tag>]... [--machine <m>]",
		examples: []string{"sesh thread tag --id 1a2b3c4d --add wip --add urgent", "sesh thread tag --id 1a2b3c4d --remove wip"},
	},
	"thread archive": {
		summary:  "archive (park) a thread, or --unarchive to restore it",
		usage:    "sesh thread archive --id <id> [--unarchive] [--machine <m>]",
		examples: []string{"sesh thread archive --id 1a2b3c4d", "sesh thread archive --id 1a2b3c4d --unarchive"},
	},
	"thread delete": {
		summary:  "delete a thread's record (refuses a live thread unless --force, which orphans the agent); never infers the id",
		usage:    "sesh thread delete --id <id> [--force] [--machine <m>]",
		examples: []string{"sesh thread delete --id 1a2b3c4d", "sesh thread delete --id 1a2b3c4d --force"},
	},
	"thread resume": {
		summary:  "revive a dead thread into a new headed tmux pane, restoring its conversation (also top-level `sesh resume`)",
		usage:    "sesh thread resume --id <id> [--machine <m>] [--json]",
		examples: []string{"sesh thread resume --id 1a2b3c4d", "sesh thread resume 1a2b3c4d"},
	},
	"thread headful": {
		summary:  "promote a live headless thread into a headed tmux pane; 409 if a turn is in flight, N/A for a codex thread with no first turn yet",
		usage:    "sesh thread headful --id <id> [--machine <m>] [--json]",
		examples: []string{"sesh thread headful --id 1a2b3c4d", "sesh thread headful 1a2b3c4d"},
	},
	"thread grid": {
		summary:  "emit the live thread grid rows (head, busy, attachment, machine, agent, name, id); optionally archived and/or across the mesh",
		usage:    "sesh thread grid [--archived] [--all-machines] [--machine <m>] [--json]",
		examples: []string{"sesh thread grid --all-machines", "sesh thread grid --json"},
	},
	"thread snapshot": {
		summary:  "emit this daemon's per-thread runtime snapshot rows (head, busy, attachment, agent, name, id)",
		usage:    "sesh thread snapshot [--machine <m>] [--json]",
		examples: []string{"sesh thread snapshot --json"},
	},

	"ticket": {
		summary:  "ticket layer (create | list | set-status | needs-input | send-prompt); auto-routes to the configured ticket owner",
		usage:    "sesh ticket <create|list|set-status|needs-input|send-prompt>",
		examples: []string{"sesh ticket create --name fix-login", "sesh ticket set-status --id t1 --status ready"},
	},
	"ticket create": {
		summary:  "create a ticket with a name and optional description/prompt",
		usage:    "sesh ticket create --name <name> [--description <text>] [--prompt <text>] [--machine <m>] [--json]",
		examples: []string{"sesh ticket create --name fix-login --prompt 'fix the OAuth redirect'"},
	},
	"ticket list": {
		summary:  "list tickets (id, status, name, thread); optionally filter to one thread",
		usage:    "sesh ticket list [--thread <id>] [--machine <m>] [--json]",
		examples: []string{"sesh ticket list", "sesh ticket list --thread 1a2b3c4d --json"},
	},
	"ticket set-status": {
		summary:  "set a ticket's status (triage|ready|active|done|dropped); --thread binds the thread (required for active)",
		usage:    "sesh ticket set-status --id <id> --status <triage|ready|active|done|dropped> [--thread <id>] [--machine <m>] [--json]",
		examples: []string{"sesh ticket set-status --id t1 --status active --thread 1a2b3c4d"},
	},
	"ticket needs-input": {
		summary:  "report whether a ticket needs input or a restart, with its status and the bound thread's head/busy axes",
		usage:    "sesh ticket needs-input --id <id> [--machine <m>] [--json]",
		examples: []string{"sesh ticket needs-input --id t1 --json"},
	},
	"ticket send-prompt": {
		summary:  "send a ticket's prompt into its bound thread",
		usage:    "sesh ticket send-prompt --id <id> [--machine <m>]",
		examples: []string{"sesh ticket send-prompt --id t1"},
	},

	"tui": {
		summary:  "launch the live cross-machine thread-grid TUI (a thin client over the daemon; Enter navigates/attaches)",
		usage:    "sesh tui [--all-machines] [--cursor] [--filter] [--expand] [--columns <c1,c2,…>] [--machine <m>]",
		long:     "Keys: ↑/↓ move · ^j/^k scroll · ←/→ fold · h/l pan columns · enter nav · / filter · tab view · r rename · t tag · T untag · P parent · n notify · x stop · d delete · a archive · q quit.",
		examples: []string{"sesh tui --all-machines", "sesh tui --cursor --filter"},
	},
	"info": {
		summary:  "describe one thread: record, both state axes, attachment, tmux locator, and its tickets (current thread inferred when no id)",
		usage:    "sesh info [<id>] [--id <id>] [--machine <m>] [--json]",
		examples: []string{"sesh info", "sesh info 1a2b3c4d --json"},
	},
	"mesh": {
		summary:  "print the merged cross-machine view from the local daemon's cache (every machine's threads with live state + freshness)",
		usage:    "sesh mesh [--machine <m>] [--json]",
		examples: []string{"sesh mesh", "sesh mesh --json"},
	},
	"tail": {
		summary:  "print the last N transcript lines of a thread (default 20); current thread inferred when no id",
		usage:    "sesh tail [<id>] [-n <n>] [--machine <m>]",
		examples: []string{"sesh tail", "sesh tail 1a2b3c4d -n 50"},
	},
	"transcript": {
		summary:  "print a thread's whole transcript dump; current thread inferred when no id",
		usage:    "sesh transcript [<id>] [--machine <m>]",
		examples: []string{"sesh transcript", "sesh transcript 1a2b3c4d"},
	},
	"subscribe": {
		summary:  "subscribe a thread to a subscribee's completed turns (delivered into the subscriber; defaults subscriber to the current thread)",
		usage:    "sesh subscribe <subscribee-id> [--from <subscriber-id>] [--allow-cycle] [--machine <m>]",
		examples: []string{"sesh subscribe 9f8e7d6c", "sesh subscribe 9f8e7d6c --from 1a2b3c4d"},
	},
	"unsubscribe": {
		summary:  "remove a subscription edge (stop delivering a subscribee's turns to the subscriber)",
		usage:    "sesh unsubscribe <subscribee-id> [--from <subscriber-id>] [--machine <m>]",
		examples: []string{"sesh unsubscribe 9f8e7d6c --from 1a2b3c4d"},
	},
	"subscriptions": {
		summary:  "list subscription edges (optionally for one thread)",
		usage:    "sesh subscriptions [<id>] [--machine <m>] [--json]",
		examples: []string{"sesh subscriptions", "sesh subscriptions 1a2b3c4d --json"},
	},
	"await": {
		summary:  "block until a thread's turn completes (busy → idle), then print its state; polls the merged mesh view so any machine's thread is awaitable by id",
		usage:    "sesh await [<id>] [--id <id>] [--timeout <dur>] [--poll <dur>] [--machine <m>] [--json]",
		examples: []string{"sesh await 1a2b3c4d", "sesh await 1a2b3c4d --timeout 5m --json"},
	},
	"delegate": {
		summary:  "spawn a headless worker, send it a task, await the reply, print it, then delete the worker (--keep retains it); cross-machine via --machine",
		usage:    "sesh delegate --agent <pi|claude|codex> <task> [--cwd <dir>] [--name <name>] [--keep] [--timeout <dur>] [--sandbox|--yolo] [--machine <m>] [--json]",
		examples: []string{"sesh delegate --agent pi 'summarize this repo'", "sesh delegate --agent claude 'run the tests' --cwd ~/proj --keep"},
	},
	"copy": {
		summary:  "copy a thread's transcript into a local dir (--to-dir) or ship it to a peer and natively restore it (--to, claude only)",
		usage:    "sesh copy [<id>] (--to-dir <dir>|--to <machine>) [--machine <m>]",
		examples: []string{"sesh copy 1a2b3c4d --to-dir ~/backups", "sesh copy 1a2b3c4d --to macbook"},
	},
	"backup": {
		summary:  "idempotent sha256 transcript backup of every local thread (or one --id) into a portable SQLite file",
		usage:    "sesh backup --to <file> [--id <id>] [--machine <m>]",
		examples: []string{"sesh backup --to ~/sesh-backup.sqlite", "sesh backup --to b.sqlite --id 1a2b3c4d"},
	},
	"restore": {
		summary:  "reconstruct transcripts from a backup file into a dir (--to-dir, every agent) or at the agent's native path (--native, claude only)",
		usage:    "sesh restore --from <file> (--to-dir <dir>|--native) [--id <id>] [--rewrite-cwd <dir>] [--force] [--machine <m>]",
		examples: []string{"sesh restore --from b.sqlite --to-dir ~/out", "sesh restore --from b.sqlite --id 1a2b3c4d --native --force"},
	},
	"resume": {
		summary:  "revive a dead thread into a new headed tmux pane, restoring its conversation (top-level alias of `sesh thread resume`)",
		usage:    "sesh resume <id> [--id <id>] [--machine <m>] [--json]",
		examples: []string{"sesh resume 1a2b3c4d"},
	},
	"import": {
		summary:  "import this machine's v1 sesh thread records into v2 (per-machine; idempotent; --dry-run previews)",
		usage:    "sesh import --from-v1 <v1-home> [--dry-run] [--machine <m>]",
		examples: []string{"sesh import --from-v1 ~/.sesh --dry-run", "sesh import --from-v1 ~/.sesh"},
	},
	"doctor": {
		summary:  "diagnose a sesh install: client-side checks (binary, config parsing, SESH_MACHINE) plus the daemon's checks; a fail sets a non-zero exit",
		usage:    "sesh doctor [--machine <m>] [--json]",
		examples: []string{"sesh doctor", "sesh doctor --json"},
	},
	"cwd-label": {
		summary:  "apply the [[cwd_label]] rules to a path and print the label (the same transform the TUI's CWD column uses); pure config, no daemon",
		usage:    "sesh cwd-label <path>",
		examples: []string{"sesh cwd-label ~/proj/sub"},
	},

	"meta": {
		summary:  "arbitrary per-thread key/value metadata (set | get | unset | list); feeds the TUI's meta.<key> predicates",
		usage:    "sesh meta <set|get|unset|list> [--id <id>]",
		examples: []string{"sesh meta set priority high", "sesh meta list"},
	},
	"meta set": {
		summary:  "set a per-thread metadata key to a value (an empty value is `meta unset`); current thread inferred when no --id",
		usage:    "sesh meta set <key> <value> [--id <id>] [--machine <m>]",
		examples: []string{"sesh meta set priority high", "sesh meta set priority high --id 1a2b3c4d"},
	},
	"meta get": {
		summary:  "print a per-thread metadata value; a missing key is a loud error (a typo must not read as empty)",
		usage:    "sesh meta get <key> [--id <id>] [--machine <m>]",
		examples: []string{"sesh meta get priority"},
	},
	"meta unset": {
		summary:  "remove a per-thread metadata key",
		usage:    "sesh meta unset <key> [--id <id>] [--machine <m>]",
		examples: []string{"sesh meta unset priority"},
	},
	"meta list": {
		summary:  "list all per-thread metadata key=value pairs",
		usage:    "sesh meta list [--id <id>] [--machine <m>]",
		examples: []string{"sesh meta list", "sesh meta list --id 1a2b3c4d"},
	},

	"hooks": {
		summary:  "manage the [[hooks]] event hooks (list | enable | disable | test); definitions live in config.toml",
		usage:    "sesh hooks <list|enable|disable|test>",
		examples: []string{"sesh hooks list", "sesh hooks test --name notify-done"},
	},
	"hooks list": {
		summary:  "list configured event hooks with their event, edge, enabled/disabled state, and command",
		usage:    "sesh hooks list [--machine <m>] [--json]",
		examples: []string{"sesh hooks list", "sesh hooks list --json"},
	},
	"hooks enable": {
		summary:  "enable a configured hook by name",
		usage:    "sesh hooks enable --name <name> [--machine <m>]",
		examples: []string{"sesh hooks enable --name notify-done"},
	},
	"hooks disable": {
		summary:  "disable (mute) a configured hook by name",
		usage:    "sesh hooks disable --name <name> [--machine <m>]",
		examples: []string{"sesh hooks disable --name notify-done"},
	},
	"hooks test": {
		summary:  "run a hook synchronously to verify the whole chain end to end (optionally using a real thread's snapshot for the event)",
		usage:    "sesh hooks test --name <name> [--thread <id>] [--machine <m>]",
		examples: []string{"sesh hooks test --name notify-done", "sesh hooks test --name notify-done --thread 1a2b3c4d"},
	},

	"master": {
		summary:  "master-tmux cockpit — one window per machine, each an auto-reconnecting attach into that machine's work server (up | window | attach | down | ensure | watchers). NOT --machine routable",
		usage:    "sesh master <up|window|attach|down|ensure|watchers>",
		examples: []string{"sesh master up --tmux-conf ~/.sesh/myrig/tmux.master.conf", "sesh master attach"},
	},
	"master up": {
		summary:  "build the master server (one window per machine, each running the per-window supervisor); loudly refuses if already up",
		usage:    "sesh master up [--machines <m1,m2,…>] [--tmux-conf <file>]",
		examples: []string{"sesh master up", "sesh master up --machines self,macbook --tmux-conf ~/.sesh/myrig/tmux.master.conf"},
	},
	"master window": {
		summary:  "the per-window supervisor: attach into a machine's work server and self-heal with backoff on every drop (never returns)",
		usage:    "sesh master window <machine>",
		examples: []string{"sesh master window macbook"},
	},
	"master attach": {
		summary:  "attach this terminal to the master server (replaces the process)",
		usage:    "sesh master attach",
		examples: []string{"sesh master attach"},
	},
	"master down": {
		summary:  "tear the master server down (idempotent: already-down is not an error)",
		usage:    "sesh master down",
		examples: []string{"sesh master down"},
	},
	"master ensure": {
		summary:  "converge the master to one window per machine: build it if down, else (re)create only missing windows (the prefix+R recovery)",
		usage:    "sesh master ensure [--machines <m1,m2,…>] [--tmux-conf <file>]",
		examples: []string{"sesh master ensure", "sesh master ensure --machines self,macbook"},
	},
	"master watchers": {
		summary:  "list the origin machines whose master cockpit currently has a live window-attach into THIS machine's work server (who is watching me)",
		usage:    "sesh master watchers [--json]",
		examples: []string{"sesh master watchers", "sesh master watchers --json"},
	},

	"peer": {
		summary:  "local mesh registry of remote machines (add | list | remove). NOT --machine routable",
		usage:    "sesh peer <add|list|remove>",
		examples: []string{"sesh peer add --machine macbook --ssh lukas@macbook --home /Users/lukas/.sesh", "sesh peer list"},
	},
	"peer add": {
		summary:  "register a remote machine; an --api-addr (with a token) opts the peer into HTTP transport, otherwise ssh",
		usage:    "sesh peer add --machine <m> --ssh <user@host> --home <remote-home> [--port <p>] [--binary <path>] [--tmux-socket <name>] [--codex-home <dir>] [--tmux-conf <file>] [--api-addr <host:port> (--api-token <t>|--api-token-file <file>)]",
		examples: []string{"sesh peer add --machine macbook --ssh lukas@macbook --home /Users/lukas/.sesh --tmux-socket mytmux", "sesh peer add --machine work --ssh lukas@work --home ~/.sesh --api-addr 100.x.y.z:7070 --api-token-file ~/.sesh/token"},
	},
	"peer list": {
		summary:  "list registered peers (machine, transport, ssh/api target, home)",
		usage:    "sesh peer list",
		examples: []string{"sesh peer list"},
	},
	"peer remove": {
		summary:  "drop a peer from the local mesh registry (unknown name is a loud error)",
		usage:    "sesh peer remove --machine <m>",
		examples: []string{"sesh peer remove --machine macbook"},
	},

	"matrix": {
		summary:  "report the feature-matrix state from the last conformance run's artifact (grid | skips); does NOT run tests. NOT --machine routable",
		usage:    "sesh matrix <grid|skips> [--artifact <path>] [--json]",
		examples: []string{"sesh matrix grid", "sesh matrix skips --json"},
	},
	"matrix grid": {
		summary:  "render the feature-matrix grid (exit non-zero unless all-green)",
		usage:    "sesh matrix grid [--artifact <path>] [--json]",
		examples: []string{"sesh matrix grid", "sesh matrix grid --json"},
	},
	"matrix skips": {
		summary:  "list the skipped (NOT IMPLEMENTED) matrix cells with their reasons",
		usage:    "sesh matrix skips [--artifact <path>] [--json]",
		examples: []string{"sesh matrix skips"},
	},
}

// topLevelCommands is the dispatched command set (mirrors main.go's switch). The
// meta-test asserts every one has a help entry — the "no silent gap" guard.
var topLevelCommands = []string{
	"matrix", "daemon", "tmux", "thread", "resume", "ticket", "tui", "info",
	"delegate", "meta", "backup", "restore", "copy", "tail", "transcript",
	"subscribe", "unsubscribe", "subscriptions", "await", "hooks", "import",
	"doctor", "cwd-label", "mesh", "master", "peer",
}

// resolveHelpRequest reports whether args (os.Args[1:]) is a help request and, if so,
// the command path to describe. Forms: `sesh help [cmd [sub]]`, `sesh -h|--help`, and
// `sesh <cmd> [sub] --help|-h` (the leading non-flag words are the path).
func resolveHelpRequest(args []string) ([]string, bool) {
	if len(args) == 0 {
		return nil, false
	}
	switch args[0] {
	case "help":
		return leadingWords(args[1:]), true
	case "-h", "--help":
		return nil, true
	}
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return leadingWords(args), true
		}
	}
	return nil, false
}

// leadingWords returns the leading non-flag tokens (the command path) of args.
func leadingWords(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		out = append(out, a)
	}
	return out
}

// printCommandHelp writes the help for a command path to stdout.
func printCommandHelp(path []string) { fmt.Print(renderHelp(path)) }

// renderHelp returns the help text for a command path ([] = the root overview).
func renderHelp(path []string) string {
	key := strings.Join(path, " ")
	if key == "" {
		return renderRootHelp()
	}
	h, ok := helpRegistry[key]
	if !ok {
		return fmt.Sprintf("sesh: no help for %q\n\n%s", key, renderRootHelp())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "sesh %s — %s\n\n", key, h.summary)
	fmt.Fprintf(&b, "usage: %s\n", h.usage)
	if h.long != "" {
		fmt.Fprintf(&b, "\n%s\n", h.long)
	}
	if children := helpChildren(key); len(children) > 0 {
		b.WriteString("\nsubcommands:\n")
		for _, c := range children {
			fmt.Fprintf(&b, "  %-16s %s\n", lastWord(c), helpRegistry[c].summary)
		}
	}
	if len(h.examples) > 0 {
		b.WriteString("\nexamples:\n")
		for _, e := range h.examples {
			fmt.Fprintf(&b, "  %s\n", e)
		}
	}
	if !strings.Contains(key, " ") && isRoutable(key) {
		b.WriteString("\nglobal: --machine <m> routes the command to remote machine <m>.\n")
	}
	return b.String()
}

// renderRootHelp is the top-level overview: every top-level command + summary.
func renderRootHelp() string {
	var b strings.Builder
	b.WriteString("sesh — multi-machine coding-agent session management\n\n")
	b.WriteString("usage: sesh <command> [args]\n")
	b.WriteString("       sesh <command> --help        help for a command\n")
	b.WriteString("       sesh help <command> [sub]    same\n\n")
	b.WriteString("commands:\n")
	names := append([]string(nil), topLevelCommands...)
	sort.Strings(names)
	for _, n := range names {
		if h, ok := helpRegistry[n]; ok {
			fmt.Fprintf(&b, "  %-13s %s\n", n, firstClause(h.summary))
		}
	}
	b.WriteString("\nglobal:\n  --machine <m>   route a command to remote machine <m> (most commands; not peer/matrix/master)\n")
	return b.String()
}

// helpChildren returns the immediate sub-paths of key, sorted.
func helpChildren(key string) []string {
	prefix := key + " "
	var out []string
	for k := range helpRegistry {
		if strings.HasPrefix(k, prefix) && !strings.Contains(k[len(prefix):], " ") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func lastWord(s string) string {
	if i := strings.LastIndex(s, " "); i >= 0 {
		return s[i+1:]
	}
	return s
}

// firstClause trims a summary to its first parenthetical/semicolon clause for the
// compact root listing.
func firstClause(s string) string {
	if i := strings.IndexAny(s, "(;—"); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// isRoutable mirrors route.go's routableSubcommand for the help footer.
func isRoutable(cmd string) bool {
	switch cmd {
	case "peer", "matrix", "master", "help", "-h", "--help":
		return false
	}
	return true
}
