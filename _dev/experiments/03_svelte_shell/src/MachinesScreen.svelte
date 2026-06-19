<script>
  import { api } from './api.js'
  let machines = $state([])
  let err = $state(null)

  async function refresh() {
    try { const m = await api.mesh(); machines = m.machines || []; err = null }
    catch (e) { err = String(e) }
  }
  $effect(() => { refresh(); const t = setInterval(refresh, 3000); return () => clearInterval(t) })

  function ago(unix) {
    if (!unix) return 'never'
    const s = Math.max(0, Math.floor(Date.now() / 1000 - unix))
    if (s < 5) return 'live'
    if (s < 60) return `${s}s ago`
    if (s < 3600) return `${Math.floor(s/60)}m ago`
    return `${Math.floor(s/3600)}h ago`
  }
  const liveCount = (m) => (m.threads || []).filter(t => t.head === 'headful').length
</script>

<div class="machines">
  <div class="topbar"><span class="h">Machines</span>{#if err}<span class="err">{err}</span>{/if}</div>
  <div class="grid">
    {#each machines as m}
      <div class="card {m.reachable ? '' : 'offline'}">
        <div class="c-head">
          <span class="dot" class:on={m.reachable}></span>
          <span class="name">{m.machine}{m.self ? ' (this)' : ''}</span>
          <span class="fresh">{m.reachable ? ago(m.synced_at_unix) : 'OFFLINE'}</span>
        </div>
        <div class="stats">
          <span><b>{(m.threads || []).length}</b> threads</span>
          <span><b>{liveCount(m)}</b> live</span>
        </div>
        <div class="threads">
          {#each (m.threads || []).slice(0, 8) as t}
            <div class="t"><span class="tg">{t.head === 'headful' ? '●' : '◌'}</span>{t.name || '(nameless)'} <span class="ta">{t.agent_kind}</span></div>
          {/each}
          {#if (m.threads || []).length > 8}<div class="more">+{m.threads.length - 8} more</div>{/if}
          {#if (m.threads || []).length === 0}<div class="more">no threads</div>{/if}
        </div>
      </div>
    {/each}
    {#if machines.length === 0}<div class="empty">no machines in the mesh</div>{/if}
  </div>
</div>

<style>
  .machines { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .topbar { display: flex; align-items: center; gap: 10px; padding: 12px 16px; border-bottom: 1px solid #1f2030; background: #0e0f17; }
  .topbar .h { font-size: 16px; font-weight: 600; } .topbar .err { color: #ffb4c0; font-size: 11px; }
  .grid { flex: 1; overflow: auto; padding: 16px; display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 14px; align-content: start; }
  .card { background: #0e0f17; border: 1px solid #1f2030; border-radius: 11px; padding: 14px; }
  .card.offline { opacity: 0.55; }
  .c-head { display: flex; align-items: center; gap: 8px; }
  .dot { width: 9px; height: 9px; border-radius: 50%; background: #565f89; }
  .dot.on { background: #9ece6a; }
  .name { font-weight: 600; font-size: 14px; flex: 1; }
  .fresh { font-size: 11px; color: #565f89; }
  .stats { display: flex; gap: 16px; margin: 10px 0; font-size: 12px; color: #9aa5ce; }
  .stats b { color: #c0caf5; }
  .threads { display: flex; flex-direction: column; gap: 4px; }
  .t { font-size: 12px; color: #9aa5ce; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tg { color: #565f89; margin-right: 5px; }
  .ta { color: #565f89; font-size: 10px; }
  .more { font-size: 11px; color: #565f89; }
  .empty { color: #565f89; padding: 20px; }
</style>
