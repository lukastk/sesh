<script>
  import { api, TICKET_STATUSES } from './api.js'

  let entries = $state([])
  let err = $state(null)
  let unreachable = $state([])
  let sel = $state(null)        // selected ticket detail (full record)
  let creating = $state(false)
  let newName = $state('')

  async function refresh() {
    try {
      const r = await api.ticketsAll()
      entries = r.tickets || []
      unreachable = r.unreachable || []
      err = null
      if (sel) { const e = entries.find(x => x.ticket.id === sel.id); if (e) sel = { ...sel, ...e.ticket } }
    } catch (e) { err = String(e) }
  }
  $effect(() => { refresh(); const t = setInterval(refresh, 3000); return () => clearInterval(t) })

  const byStatus = (s) => entries.filter(e => e.ticket.status === s)

  async function setStatus(entry, status) {
    try {
      // active needs a bound thread; if none, keep its current thread (or block)
      await api.ticketSetStatus(entry.ticket.id, status, entry.ticket.thread_id)
      await refresh()
    } catch (e) { err = String(e) }
  }
  async function create() {
    if (!newName.trim()) return
    try { await api.ticketCreate(newName.trim(), ''); newName = ''; creating = false; await refresh() }
    catch (e) { err = String(e) }
  }
  async function open(entry) {
    try { const r = await api.ticketGet(entry.ticket.id); sel = r.ticket } catch (e) { err = String(e) }
  }
  async function savePrompt() {
    try { await api.ticketSet(sel.id, { prompt: sel.prompt }); await refresh() } catch (e) { err = String(e) }
  }
  async function sendPrompt() {
    try { await api.ticketSendPrompt(sel.id); alert('Prompt sent to bound thread.') } catch (e) { err = String(e) }
  }
  async function del() {
    if (!confirm('Delete this ticket?')) return
    try { await api.ticketDelete(sel.id); sel = null; await refresh() } catch (e) { err = String(e) }
  }

  const COLORS = { triage: '#565f89', ready: '#7aa2f7', active: '#e0af68', done: '#9ece6a', dropped: '#f7768e' }
</script>

<div class="board-wrap">
  <div class="topbar">
    <span class="h">Tickets</span>
    {#if creating}
      <input bind:value={newName} placeholder="ticket name" onkeydown={(e) => e.key === 'Enter' && create()} />
      <button class="primary" onclick={create}>Create</button>
      <button onclick={() => creating = false}>Cancel</button>
    {:else}
      <button class="primary" onclick={() => creating = true}>+ New ticket</button>
    {/if}
    {#if unreachable.length}<span class="warn">⚠ unreachable: {unreachable.join(', ')}</span>{/if}
    {#if err}<span class="err">{err}</span>{/if}
  </div>

  <div class="board">
    {#each TICKET_STATUSES as s}
      <div class="col">
        <div class="col-head" style="color:{COLORS[s]}">{s} <span>{byStatus(s).length}</span></div>
        <div class="cards">
          {#each byStatus(s) as e}
            <button class="card" onclick={() => open(e)} style="border-left-color:{COLORS[s]}">
              <div class="cname">{e.ticket.name || '(unnamed)'}</div>
              <div class="cmeta">
                <span class="machine">{e.machine}</span>
                {#if e.thread_name}<span class="thread">▸ {e.thread_name}{e.thread_archived ? ' (archived)' : ''}</span>{/if}
              </div>
            </button>
          {/each}
        </div>
      </div>
    {/each}
  </div>

  {#if sel}
    <div class="backdrop" onclick={() => sel = null} role="presentation">
      <div class="detail" onclick={(e) => e.stopPropagation()} role="dialog">
        <div class="d-head">
          <input class="d-name" bind:value={sel.name} onblur={() => api.ticketSet(sel.id, { name: sel.name })} />
          <span class="pill" style="background:{COLORS[sel.status]}">{sel.status}</span>
        </div>
        <div class="status-row">
          {#each TICKET_STATUSES as s}
            <button class:on={sel.status === s} onclick={() => setStatus({ ticket: sel }, s)}>{s}</button>
          {/each}
        </div>
        <label>Prompt</label>
        <textarea bind:value={sel.prompt} onblur={savePrompt} placeholder="ticket prompt…"></textarea>
        <div class="d-meta">id {sel.id.slice(0,8)} · created {new Date(sel.created_at_unix*1000).toLocaleString()}{sel.thread_id ? ` · bound ${sel.thread_id.slice(0,8)}` : ' · unbound'}</div>
        <div class="d-actions">
          <button onclick={sendPrompt} disabled={!sel.thread_id}>Send to thread</button>
          <button class="danger" onclick={del}>Delete</button>
          <button onclick={() => sel = null}>Close</button>
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  .board-wrap { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .topbar { display: flex; align-items: center; gap: 10px; padding: 12px 16px; border-bottom: 1px solid #1f2030; background: #0e0f17; }
  .topbar .h { font-size: 16px; font-weight: 600; }
  .topbar input { background: #1a1b26; color: #c0caf5; border: 1px solid #2a2b3d; border-radius: 6px; padding: 6px 9px; font-size: 13px; }
  .topbar button { background: #1a1b26; color: #c0caf5; border: 1px solid #2a2b3d; border-radius: 6px; padding: 5px 12px; cursor: pointer; font-size: 12px; }
  .topbar .primary { background: #7aa2f7; color: #11121a; border: 0; font-weight: 600; }
  .topbar .warn { color: #e0af68; font-size: 11px; } .topbar .err { color: #ffb4c0; font-size: 11px; }
  .board { flex: 1; display: grid; grid-template-columns: repeat(5, 1fr); gap: 10px; padding: 14px; overflow: auto; min-height: 0; }
  .col { background: #0e0f17; border: 1px solid #1f2030; border-radius: 10px; display: flex; flex-direction: column; min-height: 0; }
  .col-head { padding: 10px 12px; font-size: 12px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.04em; display: flex; justify-content: space-between; }
  .col-head span { color: #565f89; }
  .cards { flex: 1; overflow-y: auto; padding: 0 8px 8px; display: flex; flex-direction: column; gap: 7px; }
  .card { text-align: left; background: #16161e; border: 1px solid #232433; border-left: 3px solid; border-radius: 8px; padding: 9px 11px; cursor: pointer; color: inherit; }
  .card:hover { background: #1c1d2b; }
  .cname { font-size: 13px; font-weight: 500; }
  .cmeta { display: flex; gap: 8px; margin-top: 5px; font-size: 10px; color: #565f89; }
  .cmeta .thread { color: #7aa2f7; }
  .backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.55); display: flex; align-items: center; justify-content: center; z-index: 50; }
  .detail { background: #16161e; border: 1px solid #2a2b3d; border-radius: 12px; padding: 18px 20px; width: 540px; max-width: 90vw; display: flex; flex-direction: column; gap: 10px; }
  .d-head { display: flex; align-items: center; gap: 10px; }
  .d-name { flex: 1; background: #1a1b26; color: #c0caf5; border: 1px solid #2a2b3d; border-radius: 7px; padding: 8px; font-size: 15px; font-weight: 600; }
  .pill { color: #11121a; font-size: 11px; font-weight: 700; padding: 3px 9px; border-radius: 20px; text-transform: uppercase; }
  .status-row { display: flex; gap: 6px; flex-wrap: wrap; }
  .status-row button { background: #1a1b26; color: #9aa5ce; border: 1px solid #2a2b3d; border-radius: 6px; padding: 5px 11px; font-size: 12px; cursor: pointer; }
  .status-row button.on { background: #2a2b3d; color: #fff; }
  label { font-size: 11px; color: #565f89; }
  textarea { background: #1a1b26; color: #c0caf5; border: 1px solid #2a2b3d; border-radius: 8px; padding: 10px; min-height: 130px; font-family: ui-monospace, monospace; font-size: 13px; resize: vertical; }
  .d-meta { font-size: 11px; color: #565f89; }
  .d-actions { display: flex; justify-content: flex-end; gap: 8px; }
  .d-actions button { background: #1a1b26; color: #c0caf5; border: 1px solid #2a2b3d; border-radius: 7px; padding: 7px 14px; cursor: pointer; font-size: 13px; }
  .d-actions .danger { color: #ffb4c0; border-color: #5a2030; }
  .d-actions button:disabled { opacity: 0.4; }
</style>
