<script>
  // Streaming bubble chat for a pi thread over the daemon-shaped RPC relay
  // (prototype of GET /v1/threads/rpc?id=). Works for headful AND headless pi.
  import { onDestroy, tick } from 'svelte'

  let { threadId } = $props()
  let msgs = $state([])      // {role:'user'|'assistant'|'tool', text}
  let draft = $state('')
  let state = $state('connecting…')
  let streaming = $state(false)
  let ws, scroller, connectedFor = null, streamIdx = null

  function open() {
    // Straight to the REAL daemon endpoint GET /v1/threads/rpc (proxied to the daemon's
    // TCP API; the proxy injects the bearer token, so it's never held in the browser).
    ws = new WebSocket(`ws://${location.host}/v1/threads/rpc?id=${encodeURIComponent(threadId)}`)
    ws.onmessage = async (ev) => {
      let m; try { m = JSON.parse(ev.data) } catch { return }
      if (m.ok && m.state) { state = m.state.idle ? `idle · ${m.state.config?.model ?? 'pi'}` : 'busy'; return }
      if (m.error) { msgs = [...msgs, { role: 'tool', text: '⚠ ' + m.error }]; return }
      if (m.event === 'text_delta') {
        if (streamIdx === null) { msgs = [...msgs, { role: 'assistant', text: '' }]; streamIdx = msgs.length - 1; streaming = true }
        msgs = msgs.map((x, i) => i === streamIdx ? { ...x, text: x.text + m.delta } : x)
        await scrollDown()
      } else if (m.event === 'tool_execution_start') { streamIdx = null; msgs = [...msgs, { role: 'tool', text: '▶ ' + m.toolName }]
      } else if (m.event === 'tool_execution_end') { streamIdx = null; msgs = [...msgs, { role: 'tool', text: '■ ' + m.toolName }]
      } else if (m.event === 'agent_end') { streamIdx = null; streaming = false; state = 'idle' }
    }
    ws.onclose = () => state = 'closed'
    ws.onerror = () => state = 'error (not a live pi rpc thread?)'
  }
  async function scrollDown() { await tick(); scroller?.scrollTo(0, scroller.scrollHeight) }
  function send() {
    const text = draft.trim()
    if (!text || !ws || ws.readyState !== 1) return
    msgs = [...msgs, { role: 'user', text }]; draft = ''; streamIdx = null
    ws.send(JSON.stringify({ message: text }))
  }
  // Connect ONCE per thread. The parent re-polls every 2.5s (reassigning the selected
  // row); without this guard the effect would re-run, wipe msgs, and leak sockets.
  $effect(() => {
    const id = threadId
    if (id === connectedFor) return
    connectedFor = id
    msgs = []
    try { ws?.close() } catch {}
    open()
  })
  onDestroy(() => { try { ws?.close() } catch {} })
</script>

<div class="chat">
  <div class="ribbon">⚡ pi RPC · streaming · {state}</div>
  <div class="log" bind:this={scroller}>
    {#each msgs as m}<div class="bubble {m.role}">{m.text}{#if m.role === 'assistant' && streaming && m === msgs[msgs.length-1]}<span class="cursor"></span>{/if}</div>{/each}
  </div>
  <div class="composer">
    <input bind:value={draft} placeholder="Message the live pi agent (streams over RPC)…"
      onkeydown={(e) => e.key === 'Enter' && send()} />
    <button onclick={send}>Send</button>
  </div>
</div>

<style>
  .chat { display: flex; flex-direction: column; height: 100%; }
  .ribbon { padding: 5px 14px; font-size: 11px; color: #e0af68; background: #16161e; border-bottom: 1px solid #1f2030; font-family: ui-monospace, monospace; }
  .log { flex: 1; overflow-y: auto; padding: 14px; display: flex; flex-direction: column; gap: 9px; }
  .bubble { max-width: 80%; padding: 8px 12px; border-radius: 10px; white-space: pre-wrap; font-size: 14px; line-height: 1.5; }
  .user { align-self: flex-end; background: #3d59a1; color: #fff; }
  .assistant { align-self: flex-start; background: #1c1d2b; color: #c0caf5; }
  .tool { align-self: flex-start; font-size: 11px; color: #e0af68; font-family: ui-monospace, monospace; background: none; padding: 0 4px; }
  .cursor::after { content: '▋'; color: #7aa2f7; animation: blink 1s steps(2) infinite; }
  @keyframes blink { 50% { opacity: 0; } }
  .composer { display: flex; gap: 8px; padding: 10px; border-top: 1px solid #1f2030; background: #16161e; }
  input { flex: 1; background: #1a1b26; color: #c0caf5; border: 1px solid #2a2b3d; border-radius: 8px; padding: 9px; font-size: 14px; }
  button { background: #e0af68; color: #16161e; border: 0; border-radius: 8px; padding: 0 16px; font-weight: 600; cursor: pointer; }
</style>
