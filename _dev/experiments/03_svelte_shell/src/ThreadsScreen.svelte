<script>
  import { api, glyph, stateLabel } from './api.js'
  import Terminal from './Terminal.svelte'
  import HeadlessChat from './HeadlessChat.svelte'
  import RpcChat from './RpcChat.svelte'
  import NewThreadModal from './NewThreadModal.svelte'

  let rows = $state([])
  let selected = $state(null)
  let err = $state(null)
  let mode = $state('auto')
  let showNew = $state(false)

  async function refresh() {
    try {
      const g = await api.grid()
      rows = g.rows || []
      if (selected) selected = rows.find(r => r.id === selected.id) || selected
      err = null
    } catch (e) { err = String(e) }
  }
  function select(r) { selected = r; mode = 'auto' }
  async function act(fn, r) { try { await fn(r.id); await refresh() } catch (e) { err = String(e) } }
  async function rename(r) {
    const name = prompt('Rename thread', r.name || '')
    if (name == null) return
    await act((id) => api.rename(id, name), r)
  }
  async function del(r) {
    if (!confirm(`Delete thread "${r.name || r.id.slice(0,8)}"? (record only)`)) return
    try { await api.del(r.id, r.head === 'headful'); if (selected?.id === r.id) selected = null; await refresh() }
    catch (e) { err = String(e) }
  }
  async function onCreated(id) { showNew = false; await refresh(); const r = rows.find(x => x.id === id); if (r) select(r) }

  $effect(() => { refresh(); const t = setInterval(refresh, 2500); return () => clearInterval(t) })

  let surface = $derived(
    !selected ? 'none'
    : mode === 'rpc' ? 'rpc' : mode === 'terminal' ? 'terminal' : mode === 'headless' ? 'headless'
    : selected.agent_kind === 'pi' ? 'rpc'
    : selected.head === 'headful' ? 'terminal' : 'headless'
  )
</script>

<div class="screen">
  <aside>
    <div class="head"><span>Threads</span><button class="new" onclick={() => showNew = true}>+ New</button></div>
    {#if err}<div class="err">{err}</div>{/if}
    <div class="list">
      {#each rows as r}
        <button class="row {selected?.id === r.id ? 'sel' : ''}" onclick={() => select(r)}>
          <span class="g {r.busy === 'busy' ? 'busy' : ''}">{glyph(r)}</span>
          <span class="nm">{r.name || '(nameless)'}</span>
          <span class="agent">{r.agent_kind}{r.tickets_open ? ` · ${r.tickets_open}🎫` : ''}</span>
          <span class="st">{stateLabel(r)}</span>
        </button>
      {/each}
      {#if rows.length === 0}<div class="empty">no threads — click “+ New”.</div>{/if}
    </div>
  </aside>

  <main>
    {#if !selected}
      <div class="placeholder">Select a thread to chat with it.</div>
    {:else}
      <header>
        <div class="title">
          <span class="g {selected.busy === 'busy' ? 'busy' : ''}">{glyph(selected)}</span>
          {selected.name || '(nameless)'} <span class="sub">{selected.agent_kind} · {stateLabel(selected)} · {selected.cwd_rel || selected.cwd}</span>
        </div>
        <div class="actions">
          {#if selected.agent_kind === 'pi'}
            <div class="seg">
              <button class:on={surface==='rpc'} onclick={() => mode='rpc'}>RPC</button>
              {#if selected.head === 'headful'}<button class:on={surface==='terminal'} onclick={() => mode='terminal'}>Terminal</button>{/if}
              <button class:on={surface==='headless'} onclick={() => mode='headless'}>Transcript</button>
            </div>
          {:else if selected.head === 'headful'}
            <button onclick={() => mode = mode === 'headless' ? 'auto' : 'headless'}>{mode === 'headless' ? 'Terminal' : 'Headless'}</button>
          {/if}
          {#if selected.head === 'headful'}
            <button onclick={() => act(api.stop, selected)}>Stop</button>
          {:else}
            <button onclick={() => act(api.resume, selected)}>Resume</button>
          {/if}
          <button onclick={() => rename(selected)}>Rename</button>
          <button onclick={() => act((id) => api.archive(id, true), selected)}>Archive</button>
          <button class="danger" onclick={() => del(selected)}>Delete</button>
        </div>
      </header>
      <section class="surface">
        {#if surface === 'rpc'}{#key selected.id + mode}<RpcChat threadId={selected.id} />{/key}
        {:else if surface === 'terminal'}{#key selected.id}<Terminal threadId={selected.id} />{/key}
        {:else if surface === 'headless'}{#key selected.id}<HeadlessChat threadId={selected.id} agentKind={selected.agent_kind} />{/key}{/if}
      </section>
    {/if}
  </main>

  {#if showNew}<NewThreadModal onclose={() => showNew = false} oncreated={onCreated} />{/if}
</div>

<style>
  .screen { display: grid; grid-template-columns: 300px 1fr; height: 100%; min-height: 0; }
  aside { background: #0e0f17; border-right: 1px solid #1f2030; display: flex; flex-direction: column; }
  .head { display: flex; justify-content: space-between; align-items: center; padding: 12px 14px; font-weight: 600; }
  .head .new { background: #7aa2f7; color: #11121a; border: 0; border-radius: 6px; padding: 4px 10px; font-size: 12px; font-weight: 600; cursor: pointer; }
  .err { margin: 0 12px 10px; padding: 8px; background: #3a1c28; color: #ffb4c0; border-radius: 6px; font-size: 11px; white-space: pre-wrap; }
  .list { flex: 1; overflow-y: auto; }
  .row { width: 100%; text-align: left; display: grid; grid-template-columns: 22px 1fr auto; gap: 4px 8px; align-items: center; background: none; border: 0; color: inherit; padding: 9px 14px; cursor: pointer; border-left: 2px solid transparent; }
  .row:hover { background: #15161f; }
  .row.sel { background: #181a26; border-left-color: #7aa2f7; }
  .row .g { font-size: 14px; color: #565f89; }
  .row .g.busy { color: #e0af68; }
  .row .nm { font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .row .agent { grid-column: 2; font-size: 10px; color: #565f89; }
  .row .st { grid-row: 1 / span 2; grid-column: 3; font-size: 10px; color: #9aa5ce; }
  .empty, .placeholder { color: #565f89; padding: 20px; }
  main { display: flex; flex-direction: column; min-width: 0; }
  header { display: flex; justify-content: space-between; align-items: center; padding: 12px 16px; border-bottom: 1px solid #1f2030; background: #0e0f17; gap: 12px; }
  .title { font-size: 15px; font-weight: 600; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title .g { color: #e0af68; }
  .title .sub { font-size: 11px; color: #565f89; font-weight: 400; margin-left: 8px; }
  .actions { display: flex; gap: 8px; align-items: center; flex-shrink: 0; }
  .seg { display: flex; border: 1px solid #2a2b3d; border-radius: 6px; overflow: hidden; }
  .seg button { border: 0; border-radius: 0; background: #1a1b26; color: #9aa5ce; padding: 5px 11px; font-size: 12px; cursor: pointer; }
  .seg button.on { background: #e0af68; color: #16161e; font-weight: 600; }
  .actions button { background: #1a1b26; color: #c0caf5; border: 1px solid #2a2b3d; border-radius: 6px; padding: 5px 12px; cursor: pointer; font-size: 12px; }
  .actions button:hover { background: #232433; }
  .actions .danger { color: #ffb4c0; border-color: #5a2030; }
  .surface { flex: 1; min-height: 0; }
  .placeholder { display: flex; align-items: center; justify-content: center; height: 100%; font-size: 14px; }
</style>
