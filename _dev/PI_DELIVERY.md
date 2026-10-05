# Headed Pi RPC delivery (2026-10-05)

Approved: ordinary messages use the live socket, never the editor; busy messages steer.
Command support lives in lukastk/pi-rpc-socket (installed by myagent), not in a sesh
copy of Pi's command parser. Findings: experiments/10_pi_headed_delivery/FINDINGS.md.

## Contract

- `thread send --text` is literal Pi message input, including leading `/`. It does
  not expand commands/templates/skills. Claude/Codex/shell remain terminal-delivered.
- New `thread command --id ... --text '/compact ...' --timeout 5m` explicitly invokes
  a Pi command. Timeout is required. Commands: compact with instructions, model,
  thinking, name, session info, registered extension commands/templates/skills.
  Unknown/unsupported commands refuse. Session replacement, reload and UI pickers
  are not part of this change.
- Commands have request IDs and pollable operation results. Compaction success is
  ONLY reported from Pi's onComplete callback; onError is a failure. HTTP requests
  stay short (under routing's 15s bound) while the CLI polls long operations.
- Pi's public sendUserMessage API is void. Literal messages and resource dispatch
  acknowledge **submitted**, NOT completed. Async errors still appear in Pi's UI;
  neither protocol nor CLI claims successful execution of a resource handler.
- Socket absence, timeout, negative/malformed ACK fail loudly. Failure after writing
  is potentially ambiguous and is never automatically retried or pasted instead.
- Ordinary RPC messages bypass the terminal typing guard; no editor is involved.
  All headed send sites (including initial --msg) share transport selection.
  Initial --msg still runs asynchronously: creation is not a delivery receipt, and
  delivery failures go to the daemon log, as before. Subscription failures likewise
  retain their existing daemon-log channel; neither path silently tries another transport.
  Scheduler busy/hold/etc guards stay in force; only the Pi typing guard is inapplicable.
- Existing running extensions already support the stable message operation. No
  protocol upgrade is required for ordinary sends. New commands require capability
  protocol 2; old runtimes refuse with a reload instruction. No automatic reloads.

## Operations and lifetime

The extension holds command operations for 1 hour after completion, maximum 256
retained operations, rejecting excess rather than evicting live work. Polling an
unknown/expired operation is a loud error. Reload/process death loses operations;
callers report uncertainty, not retry. IDs correlate replies; no request replay.

## Tests and deployment

Unit tests for protocol validation, correlation, ACKs, deadlines, command results,
capability checks and transport selection. New matrix cells Pi x local/real SSH
remote: draft preservation, busy steering, literal slash input, broken socket,
explicit command dispatch and compaction outcomes. Typing-guard cells retain their
assertions with real Claude as the terminal-delivered subject. Other headed delivery,
spawn, schedule, subscription and ticket cells form the blast radius.

API additive version bump, no store migration. Deploy extension first, then daemon
via the service manager. Existing Pi processes gain ordinary RPC sending immediately;
new command support requires Pi /reload or restart. Update self-compact's Pi branch to
explicit command + actual completion, keeping Claude/Codex's terminal workflow.
