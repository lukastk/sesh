# Headed Pi delivery — live derisking (2026-10-05)

Status: initial findings below are historical. The user approved literal messages plus
explicit extension-owned commands; implemented as API 53 / pi-rpc-socket 0.2.0.
See `../../PI_DELIVERY.md` and AGENTS.local.md H121 for the contract and current gates.

## Request and scope

Lukas requested ordinary sends into headed Pi sesh threads use the existing live RPC socket instead of terminal paste + Enter, with derisking and real tests before implementation. No silent terminal fallback. This includes the shared delivery paths (thread send, ticket prompts, subscriptions, schedules and initial --msg), not just the CLI handler.

## Measured environment and method

mymain, Pi 0.99.1, installed `git:github.com/lukastk/pi-rpc-socket` (package version 0.1.0). Used sesh's existing conformance Sandbox: isolated SESH_HOME, work/master sockets, real supervised-by-test daemon, real Pi process and a real nested tmux viewer. No production thread received input. The test cleans up its own daemon and servers.

Throwaway probe `probe_test.go` was temporarily run as `internal/conformance/pisend_probe_test.go` with `go test ./internal/conformance -run '^TestPiSendProbe$' -v -count=1`. Removed from production test discovery afterwards. Logs: `/tmp/sesh-pisend-probe.log` and `/tmp/sesh-pisend-probe2.log` (ephemeral). Two complete message probes; both reached the same compaction limitation. These are NOT passing conformance cells and do not make the matrix green.

## Confirmed

1. **Draft isolation.** Typed `DRAFT-NOT-SUBMITTED` through the attached nested viewer, then sent `{"message":"…"}` to the test session's Unix socket. A real tool call (`sleep 8`) ran; the draft remained visibly in the editor and never appeared in the session transcript.
2. **Busy = steering.** While the real sleep tool was running, injected a second message. The transcript ordering was initial user message → assistant tool call → tool result → injected user message → assistant reply `PROBE-STEER-DONE`. No abort; the sleep completed (8.1 seconds). Draft still intact after completion.
3. **Slash-text semantics differ.** Sent `/sesh-probe-literal Please reply PROBE-SLASH-LITERAL only.` through the message socket. It appeared as a user message in the transcript and got an ordinary model reply. Installed extension source calls `pi.sendUserMessage(text, {deliverAs: "steer"})`; installed Pi's AgentSession.sendUserMessage sets `expandPromptTemplates: false` by default. This is not the interactive slash-command dispatcher (nor Pi's separate stdin/stdout `--mode rpc` protocol).
4. **Compaction has a separate operation.** `{"compact":true}` returns `{"ok":true,"compacted":true,"note":"kicked off; completion is async"}`. Both probes subsequently showed `Error: Compaction failed: Nothing to compact (session too small)` in the actual Pi pane. There was no persisted compaction record. The ACK proves initiation, NOT completion or success. Do not turn that into a completed-compaction claim.
5. **Message ACK also has a narrower contract than delivery completion.** The installed extension invokes the void ExtensionAPI.sendUserMessage and immediately echoes `{ok:true, delivered:text}`. Pi handles asynchronous errors via extension error reporting, not the socket response. This source-inspected limitation was NOT experimentally exercised with a deliberately failing message. A sesh send can promise socket acceptance, not completed model processing; no automatic retries after ambiguous write/read failure.

## Existing implementation seams

- `internal/daemon/thread.go`: `handleThreadSend` → pasteByPolicy; `sendWhenReady` independently calls tmux.SendText.
- `internal/daemon/paste.go`: immediate and deferred delivery call tmux.SendText.
- `ticket.go`, `subscriptions.go`, `scheduler.go` construct the same pasteRequest.
- `internal/agents/pi/socket.go`: Message exists but has no production callers; roundTrip has no I/O deadline and Message accepts missing/false `ok` when `error` is empty. Fix before using for production delivery; include malformed/negative/stalled ACK unit tests.
- `internal/daemon/rpc.go`: existing WebSocket relay, with socket resolution independent of the Go client's default socket discovery.
- Existing typing-guard conformance cells use real Pi as their terminal-paste subject. They must move to a real terminal-delivered harness (Claude/Codex), not be weakened to keep passing when Pi stops pasting.

## Decisions still needed

Ordinary messages should use RPC steering (matching the existing extension). Socket absence/failure must be loud; the editor must never be a fallback.

The open contract is **slash commands**. Silently treating `/compact` as a user message breaks the self-compact workflow; silently retaining terminal input for slash commands reintroduces draft corruption. Options:

- Explicit supported RPC command mapping (starting with `/compact`); reject unsupported slash commands loudly. State initiation versus completion honestly and test real compaction on a sufficiently large fixture.
- An explicitly requested terminal-input mode for interactive commands, retaining the typing guard; ordinary messages always RPC. Update callers such as self-compact so intent is explicit.
- Extend the owned socket extension's command/acknowledgement protocol as part of this work, rather than add a sesh-only approximation of Pi's interactive command handling.

No production implementation or deployment yet. Full matrix not run; probe-only run reports 287 not-run, 2 existing justified N/A.
