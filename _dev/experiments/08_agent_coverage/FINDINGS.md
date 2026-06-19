# 08 — claude & codex coverage: are the non-pi agents derisked? FINDINGS

Earlier experiments leaned almost entirely on **pi**. This one asks the honest question for **claude**
and **codex** and closes the gaps that were closable.

## Status per surface, per agent (after this experiment)

| Surface | pi | claude | codex |
|---|---|---|---|
| Grid / lifecycle / tickets / machines (agent-agnostic) | ✅ | ✅ | ✅ (+ sesh's own conformance matrix covers spawn/resume/send) |
| **Terminal endpoint** (`/v1/threads/terminal`) | ✅ Go test | ✅ **live smoke** | ✅ **live smoke** |
| **Headless chat** (`send-headless` + transcript→bubbles) | ✅ | ✅ **now parsed + rendered** | ✅ **now parsed + rendered** |
| **RPC streaming bubbles** (`/v1/threads/rpc`) | ✅ | — n/a (no live RPC socket) | — n/a |

## What was done

1. **Terminal on real claude + codex.** Spawned headed claude and codex threads and attached the real
   daemon terminal WebSocket to each: **claude streamed 4333 bytes, codex 4325 bytes, both with ANSI**
   — confirming the endpoint is agent-agnostic *in practice*, not just by design (it has zero
   agent-specific code; it's `tmux attach`). The committed Go test (`TestThreadTerminalWebSocket`) still
   runs with pi; claude/codex are covered by live smoke (a table-driven test across all three is the
   obvious follow-up but triples agent-spawn cost).

2. **Headless transcript parsing for claude + codex** — the real gap. Ran a real headless turn on each,
   **characterized the three distinct JSONL schemas**, and wrote per-agent parsers in `HeadlessChat.svelte`
   (`PARSERS[agentKind]`):
   - **pi**: `{type:"message", message:{role, content:[{type:"text"|"thinking"}]}}`
   - **claude**: top-level `{type:"user"|"assistant", message:{role, content: string | [{type:"text"}]}}`
     (skips `queue-operation`/`attachment`/`ai-title`/`last-prompt` noise).
   - **codex**: `{type:"response_item", payload:{type:"message", role, content:[{type:"input_text"|"output_text"}]}}`
     (skips `session_meta`/`event_msg`/`turn_context`, and **filters codex's system-injected
     `<environment_context>`/`<permissions>` user blocks** so only real turns render).
   Verified live in the shell: **both claude and codex headless chats render clean user/assistant
   bubbles** (`shots/hl-claude.png`, `hl-codex.png`).

## Honest caveats / remaining work

- **RPC streaming is pi-only by nature.** claude and codex expose no equivalent live-attach RPC socket,
  so their interactive chat is the **terminal** (headful) and their request/reply chat is the **headless
  transcript bubbles**. This is the intended progressive-enhancement design, not a defect — but it does
  mean "streaming bubble chat" is a pi-only luxury.
- **The transcript parsers are Q&A-grade.** They render text + thinking. Real coding transcripts also
  carry **tool calls / results / diffs** (claude `tool_use`/`tool_result`; codex `function_call`/
  `reasoning` items) which the prototype currently **skips**. A production headless chat must render or
  summarize those — the single biggest remaining per-agent piece, and the feature map's flagged "top
  correctness risk". (Best solved server-side: a normalized `/v1/threads/chat` that emits typed turns,
  so clients don't each reimplement three schemas.)
- **Terminal claude/codex is smoke-tested, not unit-tested** (the endpoint is agent-agnostic; pi has the
  committed Go test). Promote to a table-driven test if desired.
- **Codex needs its CODEX_HOME set up** (auth) or headless/headed turns fail loudly — a real deploy
  configures this; the experiment env.sh now symlinks the real auth (mirrors the harness).

## Verdict

claude and codex are **largely derisked**: every agent-agnostic surface works, the terminal works for
all three (smoked), and the headless chat now parses and renders all three formats. The honest residual
is **rich transcript rendering** (tool calls/diffs) for claude/codex headless chats, best done as a
server-side normalized turn stream — a known, scoped piece of work, not an unknown.

Artifacts: `03_svelte_shell/src/HeadlessChat.svelte` (the `PARSERS` table), `shots/hl-{claude,codex}.png`.
