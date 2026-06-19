<script>
  import { api } from './api.js'
  import { tick } from 'svelte'

  let { threadId, agentKind = 'pi' } = $props()
  let msgs = $state([])      // {role, text, thinking?}
  let draft = $state('')
  let sending = $state(false)
  let scroller

  // Per-agent transcript parsers — each agent writes a DIFFERENT JSONL schema (verified
  // live against real claude/codex/pi turns). A production app would put these server-side
  // behind a normalized endpoint; here they prove the three formats all render as bubbles.
  const PARSERS = {
    // pi: {type:"message", message:{role, content:[{type:"text"|"thinking", text/thinking}]}}
    pi(o) {
      if (o.type !== 'message' || !o.message) return null
      const c = o.message.content || []
      return mk(o.message.role,
        c.filter(x => x.type === 'text').map(x => x.text).join('\n'),
        c.filter(x => x.type === 'thinking').map(x => x.thinking).join('\n'))
    },
    // claude: top-level {type:"user"|"assistant", message:{role, content: string | [{type:"text",text}]}}
    claude(o) {
      if (o.type !== 'user' && o.type !== 'assistant') return null
      const content = o.message?.content
      if (typeof content === 'string') return mk(o.message.role, content)
      const arr = content || []
      return mk(o.message?.role,
        arr.filter(x => x.type === 'text').map(x => x.text).join('\n'),
        arr.filter(x => x.type === 'thinking').map(x => x.thinking).join('\n'))
    },
    // codex: {type:"response_item", payload:{type:"message", role, content:[{type:"input_text"|"output_text", text}]}}
    codex(o) {
      if (o.type !== 'response_item' || o.payload?.type !== 'message') return null
      const role = o.payload.role
      if (role !== 'user' && role !== 'assistant') return null   // skip developer/system
      const text = (o.payload.content || [])
        .filter(x => x.type === 'input_text' || x.type === 'output_text').map(x => x.text).join('\n')
      // Skip codex's system-injected env/permissions blocks (not real user turns).
      if (role === 'user' && /^\s*<(environment_context|permissions)/.test(text)) return null
      return mk(role, text)
    },
  }
  function mk(role, text, thinking) {
    if (!text && !thinking) return null
    return { role, text: text || '', thinking: thinking || '' }
  }
  function parse(lines) {
    const p = PARSERS[agentKind] || PARSERS.pi
    const out = []
    for (const raw of lines) {
      let o; try { o = JSON.parse(raw) } catch { continue }
      const m = p(o)
      if (m) out.push(m)
    }
    return out
  }

  async function load() {
    try {
      const t = await api.transcript(threadId, 200)
      msgs = parse(t.lines || [])
      await scrollDown()
    } catch (e) { console.error(e) }
  }
  async function scrollDown() { await tick(); scroller?.scrollTo(0, scroller.scrollHeight) }

  async function send() {
    const text = draft.trim()
    if (!text || sending) return
    sending = true
    msgs = [...msgs, { role: 'user', text }]
    draft = ''
    await scrollDown()
    try {
      await api.sendHeadless(threadId, text)
      // poll for completion, then reload the transcript for the full turn
      for (let i = 0; i < 120; i++) {
        const r = await api.headlessReply(threadId)
        if (!r.working && r.have_reply) break
        await new Promise(res => setTimeout(res, 1000))
      }
      await load()
    } catch (e) {
      msgs = [...msgs, { role: 'error', text: String(e) }]
    } finally { sending = false; await scrollDown() }
  }

  $effect(() => { threadId; load() })
</script>

<div class="chat">
  <div class="log" bind:this={scroller}>
    {#each msgs as m}
      <div class="bubble {m.role}">
        {#if m.thinking}<div class="thinking">{m.thinking}</div>{/if}
        <div class="text">{m.text}</div>
      </div>
    {/each}
    {#if sending}<div class="bubble assistant pending">…thinking</div>{/if}
  </div>
  <div class="composer">
    <textarea bind:value={draft} placeholder="Send a headless turn…"
      onkeydown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) send() }}></textarea>
    <button onclick={send} disabled={sending}>Send</button>
  </div>
</div>

<style>
  .chat { display: flex; flex-direction: column; height: 100%; }
  .log { flex: 1; overflow-y: auto; padding: 14px; display: flex; flex-direction: column; gap: 10px; }
  .bubble { max-width: 80%; padding: 8px 12px; border-radius: 10px; white-space: pre-wrap; font-size: 13px; line-height: 1.45; }
  .bubble.user { align-self: flex-end; background: #3d59a1; color: #fff; }
  .bubble.assistant { align-self: flex-start; background: #232433; color: #c0caf5; }
  .bubble.error { align-self: center; background: #5a2030; color: #ffb4c0; }
  .pending { opacity: 0.6; font-style: italic; }
  .thinking { opacity: 0.5; font-style: italic; font-size: 12px; margin-bottom: 6px; border-left: 2px solid #565f89; padding-left: 6px; }
  .composer { display: flex; gap: 8px; padding: 10px; border-top: 1px solid #1f2030; background: #16161e; }
  textarea { flex: 1; resize: none; height: 46px; background: #1a1b26; color: #c0caf5; border: 1px solid #2a2b3d; border-radius: 8px; padding: 8px; font-family: inherit; }
  button { background: #7aa2f7; color: #16161e; border: 0; border-radius: 8px; padding: 0 16px; font-weight: 600; cursor: pointer; }
  button:disabled { opacity: 0.5; }
</style>
